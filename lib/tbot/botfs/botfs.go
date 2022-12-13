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

package botfs

import (
	"io/fs"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"syscall"

	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"

	"github.com/zmb3/teleport"
	"github.com/zmb3/teleport/api/constants"
)

var log = logrus.WithFields(logrus.Fields{
	trace.Component: teleport.ComponentTBot,
})

// SymlinksMode is an enum type listing various symlink behavior modes.
type SymlinksMode string

const (
	// SymlinksInsecure does allow resolving symlink paths and does not issue
	// any symlink-related warnings.
	SymlinksInsecure SymlinksMode = "insecure"

	// SymlinksTrySecure attempts to write files securely and avoid symlink
	// attacks, but falls back with a warning if the necessary OS / kernel
	// support is missing.
	SymlinksTrySecure SymlinksMode = "try-secure"

	// SymlinksSecure attempts to write files securely and fails with an error
	// if the operation fails. This should be the default on systems where we
	// expect it to be supported.
	SymlinksSecure SymlinksMode = "secure"
)

// ACLMode is an enum type listing various ACL behavior modes.
type ACLMode string

const (
	// ACLOff disables ACLs
	ACLOff ACLMode = "off"

	// ACLTry attempts to use ACLs but falls back to no ACLs with a warning if
	// unavailable.
	ACLTry ACLMode = "try"

	// ACLRequired enables ACL support and fails if ACLs are unavailable.
	ACLRequired ACLMode = "required"
)

// OpenMode is a mode for opening files.
type OpenMode int

const (
	// DefaultMode is the preferred permissions mode for bot files.
	DefaultMode fs.FileMode = 0600

	// DefaultDirMode is the preferred permissions mode for bot directories.
	// Directories need the execute bit set for most operations on their
	// contents to succeed.
	DefaultDirMode fs.FileMode = 0700

	// ReadMode is the mode with which files should be opened for reading and
	// writing.
	ReadMode OpenMode = OpenMode(os.O_CREATE | os.O_RDONLY)

	// WriteMode is the mode with which files should be opened specifically
	// for writing.
	WriteMode OpenMode = OpenMode(os.O_CREATE | os.O_WRONLY | os.O_TRUNC)
)

// ACLOptions contains parameters needed to configure ACLs
type ACLOptions struct {
	// BotUser is the bot user that should have write access to this entry
	BotUser *user.User

	// ReaderUser is the user that should have read access to the file. This
	// may be nil if the reader user is not known.
	ReaderUser *user.User
}

// openStandard attempts to open the given path for reading and writing with
// O_CREATE set.
func openStandard(path string, mode OpenMode) (*os.File, error) {
	file, err := os.OpenFile(path, int(mode), DefaultMode)
	if err != nil {
		return nil, trace.ConvertSystemError(err)
	}

	return file, nil
}

// createStandard creates an empty file or directory at the given path without
// attempting to prevent symlink attacks.
func createStandard(path string, isDir bool) error {
	if isDir {
		if err := os.Mkdir(path, DefaultDirMode); err != nil {
			return trace.ConvertSystemError(err)
		}

		return nil
	}

	f, err := openStandard(path, WriteMode)
	if err != nil {
		return trace.Wrap(err)
	}

	if err := f.Close(); err != nil {
		log.Warnf("Failed to close file at %q: %+v", path, err)
	}

	return nil
}

// GetOwner attempts to retrieve the owner of the given file. This is not
// supported on all platforms and will return a trace.NotImplemented in that
// case.
func GetOwner(fileInfo fs.FileInfo) (*user.User, error) {
	info, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, trace.NotImplemented("Cannot verify file ownership on this platform.")
	}

	user, err := user.LookupId(strconv.Itoa(int(info.Uid)))
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return user, nil
}

// IsOwnedBy checks that the file at the given path is owned by the given user.
// Returns a trace.NotImplemented() on unsupported platforms.
func IsOwnedBy(fileInfo fs.FileInfo, user *user.User) (bool, error) {
	if runtime.GOOS == constants.WindowsOS {
		// no-op on windows
		return false, trace.NotImplemented("Cannot verify file ownership on this platform.")
	}

	info, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return false, trace.NotImplemented("Cannot verify file ownership on this platform.")
	}

	// Our files are 0600, so don't bother checking gid.
	return strconv.Itoa(int(info.Uid)) == user.Uid, nil
}
