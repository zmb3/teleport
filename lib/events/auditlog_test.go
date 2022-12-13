/*
Copyright 2015-2018 Gravitational, Inc.

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

package events

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	apidefaults "github.com/zmb3/teleport/api/defaults"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/types/events"
	"github.com/zmb3/teleport/lib/events/eventstest"
	"github.com/zmb3/teleport/lib/session"
	"github.com/zmb3/teleport/lib/utils"
)

func TestMain(m *testing.M) {
	utils.InitLoggerForTests()
	os.Exit(m.Run())
}

func TestNew(t *testing.T) {
	alog := makeLog(t, clockwork.NewFakeClock())

	// Close twice.
	require.NoError(t, alog.Close())
	require.NoError(t, alog.Close())
}

// TestLogRotation makes sure that logs are rotated
// on the day boundary and symlinks are created and updated
func TestLogRotation(t *testing.T) {
	start := time.Date(1984, time.April, 4, 0, 0, 0, 0, time.UTC)
	clock := clockwork.NewFakeClockAt(start)

	// create audit log, write a couple of events into it, close it
	alog := makeLog(t, clock)
	defer func() {
		require.NoError(t, alog.Close())
	}()

	for _, duration := range []time.Duration{0, time.Hour * 25} {
		// advance time and emit audit event
		now := start.Add(duration)
		clock.Advance(duration)

		// emit regular event:
		event := &events.Resize{
			Metadata:     events.Metadata{Type: "resize", Time: now},
			TerminalSize: "10:10",
		}
		err := alog.EmitAuditEvent(context.TODO(), event)
		require.NoError(t, err)
		logfile := alog.localLog.file.Name()

		// make sure that file has the same date as the event
		dt, err := parseFileTime(filepath.Base(logfile))
		require.NoError(t, err)
		require.Equal(t, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), dt)

		// read back what's been written:
		bytes, err := os.ReadFile(logfile)
		require.NoError(t, err)
		contents, err := json.Marshal(event)
		contents = append(contents, '\n')
		require.NoError(t, err)
		require.Equal(t, string(bytes), string(contents))

		// read back the contents using symlink
		bytes, err = os.ReadFile(filepath.Join(alog.localLog.SymlinkDir, SymlinkFilename))
		require.NoError(t, err)
		require.Equal(t, string(bytes), string(contents))

		found, _, err := alog.SearchEvents(now.Add(-time.Hour), now.Add(time.Hour), apidefaults.Namespace, nil, 0, types.EventOrderAscending, "")
		require.NoError(t, err)
		require.Len(t, found, 1)
	}
}

func TestConcurrentStreaming(t *testing.T) {
	uploader := NewMemoryUploader()
	alog, err := NewAuditLog(AuditLogConfig{
		DataDir:       t.TempDir(),
		Clock:         clockwork.NewFakeClock(),
		ServerID:      "remote",
		UploadHandler: uploader,
	})
	require.NoError(t, err)
	t.Cleanup(func() { alog.Close() })

	ctx := context.Background()
	sid := session.ID("abc123")

	// upload a bogus session so that we can try to stream its events
	// (this is not valid protobuf, so the stream is not expected to succeed)
	_, err = uploader.Upload(ctx, sid, io.NopCloser(strings.NewReader(`asdfasdfasdfasdfasdef`)))
	require.NoError(t, err)

	// run multiple concurrent streams, which forces the second one to wait
	// on the download that the first one started
	streams := 2
	errors := make(chan error, streams)
	for i := 0; i < streams; i++ {
		go func() {
			eventsC, errC := alog.StreamSessionEvents(ctx, sid, 0)
			for {
				select {
				case err := <-errC:
					errors <- err
				case _, ok := <-eventsC:
					if !ok {
						errors <- nil
						return
					}
				}
			}
		}()
	}

	// This test just verifies that the streamer does not panic when multiple
	// concurrent streams are waiting on the same download to complete.
	for i := 0; i < streams; i++ {
		<-errors
	}
}

func TestExternalLog(t *testing.T) {
	m := &mockAuditLog{
		emitter: eventstest.MockEmitter{},
	}

	fakeClock := clockwork.NewFakeClock()
	alog, err := NewAuditLog(AuditLogConfig{
		DataDir:       t.TempDir(),
		Clock:         fakeClock,
		ServerID:      "remote",
		UploadHandler: NewMemoryUploader(),
		ExternalLog:   m,
	})
	require.NoError(t, err)
	defer alog.Close()

	evt := &events.SessionConnect{}
	require.NoError(t, alog.EmitAuditEvent(context.Background(), evt))

	require.Len(t, m.emitter.Events(), 1)
	require.Equal(t, m.emitter.Events()[0], evt)
}

func makeLog(t *testing.T, clock clockwork.Clock) *AuditLog {
	alog, err := NewAuditLog(AuditLogConfig{
		DataDir:       t.TempDir(),
		ServerID:      "server1",
		Clock:         clock,
		UIDGenerator:  utils.NewFakeUID(),
		UploadHandler: NewMemoryUploader(),
	})
	require.NoError(t, err)

	return alog
}
