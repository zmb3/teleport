/*
Copyright 2022 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package sftp handles file transfers client-side via SFTP
package sftp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gravitational/trace"
	"github.com/pkg/sftp"
	"github.com/schollz/progressbar/v3"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/lib/sshutils/scp"
)

// Options control aspects of a file transfer
type Options struct {
	// Recursive indicates recursive file transfer
	Recursive bool
	// PreserveAttrs preserves access and modification times
	// from the original file
	PreserveAttrs bool
}

type homeDirRetriever func() (string, error)

// Config describes the settings of a file transfer
type Config struct {
	srcPaths []string
	dstPath  string
	srcFS    FileSystem
	dstFS    FileSystem
	opts     Options

	// getHomeDir returns the home directory of the remote user of the
	// SSH session
	getHomeDir homeDirRetriever

	// ProgressWriter is a callback to return a writer for printing the progress
	// (used only on the client)
	ProgressWriter func(fileInfo os.FileInfo) io.Writer
	// Log optionally specifies the logger
	Log log.FieldLogger
}

// FileSystem describes file operations to be done either locally or over SFTP
type FileSystem interface {
	// Type returns whether the filesystem is "local" or "remote"
	Type() string
	// Stat returns info about a file
	Stat(ctx context.Context, path string) (os.FileInfo, error)
	// ReadDir returns information about files contained within a directory
	ReadDir(ctx context.Context, path string) ([]os.FileInfo, error)
	// Open opens a file
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	// Create creates a new file
	Create(ctx context.Context, path string, mode os.FileMode) (io.WriteCloser, error)
	// MkDir creates a directory
	// sftp.Client.Mkdir does not take an os.FileMode, so this can't either
	Mkdir(ctx context.Context, path string, mode os.FileMode) error
	// Chmod sets file permissions
	Chmod(ctx context.Context, path string, mode os.FileMode) error
	// Chtimes sets file access and modification time
	Chtimes(ctx context.Context, path string, atime, mtime time.Time) error
}

// CreateUploadConfig returns a Config ready to upload files
func CreateUploadConfig(src []string, dst string, opts Options) (*Config, error) {
	for _, srcPath := range src {
		if srcPath == "" {
			return nil, trace.BadParameter("source path is empty")
		}
	}
	if dst == "" {
		return nil, trace.BadParameter("destination path is empty")
	}

	c := &Config{
		srcPaths: src,
		dstPath:  dst,
		srcFS:    &localFS{},
		dstFS:    &remoteFS{},
		opts:     opts,
	}
	c.setDefaults()

	return c, nil
}

// CreateDownloadConfig returns a Config ready to download files
func CreateDownloadConfig(src, dst string, opts Options) (*Config, error) {
	if src == "" {
		return nil, trace.BadParameter("source path is empty")
	}
	if dst == "" {
		return nil, trace.BadParameter("destination path is empty")
	}

	c := &Config{
		srcPaths: []string{src},
		dstPath:  dst,
		srcFS:    &remoteFS{},
		dstFS:    &localFS{},
		opts:     opts,
	}
	c.setDefaults()

	return c, nil
}

// setDefaults sets default values
func (c *Config) setDefaults() {
	logger := c.Log
	if logger == nil {
		logger = log.StandardLogger()
	}
	c.Log = logger.WithFields(log.Fields{
		trace.Component: "SFTP",
		trace.ComponentFields: log.Fields{
			"SrcPaths":      c.srcPaths,
			"DstPath":       c.dstPath,
			"Recursive":     c.opts.Recursive,
			"PreserveAttrs": c.opts.PreserveAttrs,
		},
	})
}

// TransferFiles transfers files from the configured source paths to the
// configured destination path over SFTP
func (c *Config) TransferFiles(ctx context.Context, sshClient *ssh.Client) error {
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return trace.Wrap(err)
	}
	if err := c.initFS(ctx, sshClient, sftpClient); err != nil {
		return trace.Wrap(err)
	}

	transferErr := c.transfer(ctx)
	closeErr := sftpClient.Close()
	if transferErr != nil {
		return trace.Wrap(transferErr)
	}

	return trace.Wrap(closeErr)
}

// initFS ensures the source and destination filesystems are ready to transfer
func (c *Config) initFS(ctx context.Context, sshClient *ssh.Client, client *sftp.Client) error {
	var haveRemoteFS bool

	srcFS, srcOK := c.srcFS.(*remoteFS)
	if srcOK {
		srcFS.c = client
		haveRemoteFS = true
	}
	dstFS, dstOK := c.dstFS.(*remoteFS)
	if dstOK {
		dstFS.c = client
		haveRemoteFS = true
	}
	// this will only happen in tests
	if !haveRemoteFS {
		return nil
	}

	if c.getHomeDir == nil {
		c.getHomeDir = func() (_ string, err error) {
			return getRemoteHomeDir(sshClient)
		}
	}

	return trace.Wrap(c.expandPaths(srcOK, dstOK))

}

func (c *Config) expandPaths(srcIsRemote, dstIsRemote bool) (err error) {
	if srcIsRemote {
		for i, srcPath := range c.srcPaths {
			c.srcPaths[i], err = expandPath(srcPath, c.getHomeDir)
			if err != nil {
				return trace.Wrap(err)
			}
		}
	}
	if dstIsRemote {
		c.dstPath, err = expandPath(c.dstPath, c.getHomeDir)
	}

	return trace.Wrap(err)
}

func expandPath(path string, getHomeDir homeDirRetriever) (string, error) {
	if !needsExpansion(path) {
		return path, nil
	}

	homeDir, err := getHomeDir()
	if err != nil {
		return "", trace.Wrap(err)
	}

	// this is safe because we verified that all paths are non-empty
	// in CreateUploadConfig/CreateDownloadConfig
	return filepath.Join(homeDir, path[1:]), nil
}

// needsExpansion returns true if path is '~', '~/', or '~\' on Windows
func needsExpansion(path string) bool {
	if len(path) == 1 {
		return path == "~"
	}

	// allow '~\' or '~/' on Windows since '\' is the canonical path
	// separator but some users may use '/' instead
	if runtime.GOOS == "windows" && strings.HasPrefix(path, `~\`) {
		return true
	}
	return strings.HasPrefix(path, "~/")
}

// getRemoteHomeDir returns the home directory of the remote user of
// the SSH connection
func getRemoteHomeDir(sshClient *ssh.Client) (string, error) {
	s, err := sshClient.NewSession()
	if err != nil {
		return "", trace.Wrap(err)
	}
	defer s.Close()
	if err := s.RequestSubsystem(teleport.GetHomeDirSubsystem); err != nil {
		return "", trace.Wrap(err)
	}
	r, err := s.StdoutPipe()
	if err != nil {
		return "", trace.Wrap(err)
	}

	var homeDirBuf bytes.Buffer
	if _, err := io.Copy(&homeDirBuf, r); err != nil {
		return "", trace.Wrap(err)
	}

	return homeDirBuf.String(), nil
}

// transfer preforms file transfers
func (c *Config) transfer(ctx context.Context) error {
	var dstIsDir bool
	dstInfo, err := c.dstFS.Stat(ctx, c.dstPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return trace.NotFound("error accessing %s path %q: %v", c.dstFS.Type(), c.dstPath, err)
		}
		// if there are multiple source paths and the destination path
		// doesn't exist, create it as a directory
		if len(c.srcPaths) > 1 {
			if err := c.dstFS.Mkdir(ctx, c.dstPath, teleport.SharedDirMode); err != nil {
				return trace.Errorf("error creating %s directory %q: %w", c.dstFS.Type(), c.dstPath, err)
			}
			dstIsDir = true
		}
	} else if len(c.srcPaths) > 1 && !dstInfo.IsDir() {
		// if there are multiple source paths, ensure the destination path
		// is a directory
		return trace.BadParameter("%s file %q is not a directory, but multiple source files were specified",
			c.dstFS.Type(),
			c.dstPath,
		)
	} else if dstInfo.IsDir() {
		dstIsDir = true
	}

	// get info of source files and ensure appropriate options were passed
	fileInfos := make([]os.FileInfo, len(c.srcPaths))
	for i := range c.srcPaths {
		fi, err := c.srcFS.Stat(ctx, c.srcPaths[i])
		if err != nil {
			return trace.Errorf("could not access %s path %q: %v", c.srcFS.Type(), c.srcPaths[i], err)
		}
		if fi.IsDir() && !c.opts.Recursive {
			// Note: using any other error constructor (e.g. BadParameter)
			// might lead to relogin attempt and a completely obscure
			// error message
			return trace.BadParameter("%q is a directory, but the recursive option was not passed", c.srcPaths[i])
		}
		fileInfos[i] = fi
	}

	for i, fi := range fileInfos {
		dstPath := c.dstPath
		if dstIsDir || fi.IsDir() {
			dstPath = path.Join(dstPath, fi.Name())
		}

		if fi.IsDir() {
			if err := c.transferDir(ctx, dstPath, c.srcPaths[i], fi); err != nil {
				return trace.Wrap(err)
			}
		} else {
			if err := c.transferFile(ctx, dstPath, c.srcPaths[i], fi); err != nil {
				return trace.Wrap(err)
			}
		}
	}

	return nil
}

// transferDir transfers a directory
func (c *Config) transferDir(ctx context.Context, dstPath, srcPath string, srcFileInfo os.FileInfo) error {
	err := c.dstFS.Mkdir(ctx, dstPath, srcFileInfo.Mode())
	if err != nil && !errors.Is(err, os.ErrExist) {
		return trace.Errorf("error creating %s directory %q: %w", c.dstFS.Type(), dstPath, err)
	}

	infos, err := c.srcFS.ReadDir(ctx, srcPath)
	if err != nil {
		return trace.Errorf("error reading %s directory %q: %w", c.srcFS.Type(), srcPath, err)
	}

	for _, info := range infos {
		dstSubPath := path.Join(dstPath, info.Name())
		lSubPath := path.Join(srcPath, info.Name())

		if info.IsDir() {
			if err := c.transferDir(ctx, dstSubPath, lSubPath, info); err != nil {
				return trace.Wrap(err)
			}
		} else {
			if err := c.transferFile(ctx, dstSubPath, lSubPath, info); err != nil {
				return trace.Wrap(err)
			}
		}
	}

	// set modification and access times last so creating sub dirs/files
	// doesn't update the times
	if c.opts.PreserveAttrs {
		err := c.dstFS.Chtimes(ctx, dstPath, getAtime(srcFileInfo), srcFileInfo.ModTime())
		if err != nil {
			return trace.Errorf("error changing times of %s directory %q: %w", c.dstFS.Type(), dstPath, err)
		}
	}

	return nil
}

// transferFile transfers a file
func (c *Config) transferFile(ctx context.Context, dstPath, srcPath string, srcFileInfo os.FileInfo) error {
	srcFile, err := c.srcFS.Open(ctx, srcPath)
	if err != nil {
		return trace.Errorf("error opening %s file %q: %w", c.srcFS.Type(), srcPath, err)
	}
	defer srcFile.Close()

	dstFile, err := c.dstFS.Create(ctx, dstPath, srcFileInfo.Mode())
	if err != nil {
		return trace.Errorf("error creating %s file %q: %w", c.dstFS.Type(), dstPath, err)
	}
	defer dstFile.Close()

	// write to canceler first so if the context is canceled the transferring
	// can stop immediately
	var writer io.Writer
	canceler := &cancelWriter{
		ctx: ctx,
	}
	// if a progress writer was set, write file transfer progress
	if c.ProgressWriter != nil {
		writer = io.MultiWriter(canceler, dstFile, c.ProgressWriter(srcFileInfo))
	} else {
		writer = io.MultiWriter(canceler, dstFile)
	}

	n, err := io.Copy(writer, srcFile)
	if err != nil {
		return trace.Errorf("error copying %s file %q to %s file %q: %w",
			c.srcFS.Type(),
			srcPath,
			c.dstFS.Type(),
			dstPath,
			err,
		)
	}
	if n != srcFileInfo.Size() {
		return trace.Errorf("short write: written %v, expected %v", n, srcFileInfo.Size())
	}

	if c.opts.PreserveAttrs {
		err := c.dstFS.Chtimes(ctx, dstPath, getAtime(srcFileInfo), srcFileInfo.ModTime())
		if err != nil {
			return trace.Errorf("error changing times of %s file %q: %w", c.dstFS.Type(), dstPath, err)
		}
	}

	return nil
}

func getAtime(fi os.FileInfo) time.Time {
	s := fi.Sys()
	if s == nil {
		return time.Time{}
	}

	if sftpfi, ok := fi.Sys().(*sftp.FileStat); ok {
		return time.Unix(int64(sftpfi.Atime), 0)
	}

	return scp.GetAtime(fi)
}

type cancelWriter struct {
	ctx context.Context
}

func (c *cancelWriter) Write(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return len(b), nil
}

// NewProgressBar returns a new progress bar that writes to writer.
func NewProgressBar(size int64, desc string, writer io.Writer) *progressbar.ProgressBar {
	// this is necessary because progressbar.DefaultBytes doesn't allow
	// the caller to specify a writer
	return progressbar.NewOptions64(
		size,
		progressbar.OptionSetDescription(desc),
		progressbar.OptionSetWriter(writer),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(10),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(writer, "\n")
		}),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionFullWidth(),
		progressbar.OptionSetRenderBlankState(true),
	)
}
