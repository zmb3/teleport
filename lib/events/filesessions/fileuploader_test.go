/*
Copyright 2018-2020 Gravitational, Inc.

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

package filesessions

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/zmb3/teleport/lib/events"
	"github.com/zmb3/teleport/lib/events/test"
	"github.com/zmb3/teleport/lib/utils"
)

func TestMain(m *testing.M) {
	utils.InitLoggerForTests()
	os.Exit(m.Run())
}

// TestStreams tests various streaming upload scenarios
func TestStreams(t *testing.T) {
	dir := t.TempDir()

	handler, err := NewHandler(Config{
		Directory: dir,
	})
	require.Nil(t, err)
	defer handler.Close()

	t.Run("Stream", func(t *testing.T) {
		test.Stream(t, handler)
	})
	t.Run("Resume", func(t *testing.T) {
		var completeCount atomic.Uint64
		handler, err := NewHandler(Config{
			Directory: dir,
			OnBeforeComplete: func(ctx context.Context, upload events.StreamUpload) error {
				if completeCount.Add(1) <= 1 {
					return trace.ConnectionProblem(nil, "simulate failure %v", completeCount.Load())
				}
				return nil
			},
		})
		require.Nil(t, err)
		defer handler.Close()

		test.StreamResumeManyParts(t, handler)
	})
	t.Run("UploadDownload", func(t *testing.T) {
		test.UploadDownload(t, handler)
	})
	t.Run("DownloadNotFound", func(t *testing.T) {
		test.DownloadNotFound(t, handler)
	})
}
