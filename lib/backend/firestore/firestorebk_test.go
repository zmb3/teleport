// Copyright 2021 Gravitational, Inc
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package firestore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	adminpb "google.golang.org/genproto/googleapis/firestore/admin/v1"
	firestorepb "google.golang.org/genproto/googleapis/firestore/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zmb3/teleport/lib/backend"
	"github.com/zmb3/teleport/lib/backend/test"
	"github.com/zmb3/teleport/lib/utils"
)

func TestMain(m *testing.M) {
	utils.InitLoggerForTests()
	os.Exit(m.Run())
}

// TestMarshal tests index operation metadata marshal and unmarshal
// to verify backwards compatibility. Gogoproto is incompatible with ApiV2 protoc-gen-go code.
//
// Track the issue here: https://github.com/gogo/protobuf/issues/678
func TestMarshal(t *testing.T) {
	meta := adminpb.IndexOperationMetadata{}
	data, err := proto.Marshal(&meta)
	require.NoError(t, err)
	out := adminpb.IndexOperationMetadata{}
	err = proto.Unmarshal(data, &out)
	require.NoError(t, err)
}

func firestoreParams() backend.Params {
	// Creating the indices on - even an empty - live Firestore collection
	// can take 5 minutes, so we re-use the same project and collection
	// names for each test.

	return map[string]interface{}{
		"collection_name":                   "tp-cluster-data-test",
		"project_id":                        "tp-testproj",
		"endpoint":                          "localhost:8618",
		"purgeExpiredDocumentsPollInterval": time.Second,
	}
}

func ensureTestsEnabled(t *testing.T) {
	const varName = "TELEPORT_FIRESTORE_TEST"
	if os.Getenv(varName) == "" {
		t.Skipf("Firestore tests are disabled. Enable by defining the %v environment variable", varName)
	}
}

func ensureEmulatorRunning(t *testing.T, cfg map[string]interface{}) {
	con, err := net.Dial("tcp", cfg["endpoint"].(string))
	if err != nil {
		t.Skip("Firestore emulator is not running, start it with: gcloud beta emulators firestore start --host-port=localhost:8618")
	}
	require.NoError(t, con.Close())
}

func TestFirestoreDB(t *testing.T) {
	cfg := firestoreParams()
	ensureTestsEnabled(t)
	ensureEmulatorRunning(t, cfg)

	newBackend := func(options ...test.ConstructionOption) (backend.Backend, clockwork.FakeClock, error) {
		testCfg, err := test.ApplyOptions(options)
		if err != nil {
			return nil, nil, trace.Wrap(err)
		}

		if testCfg.MirrorMode {
			return nil, nil, test.ErrMirrorNotSupported
		}

		// This would seem to be a bad thing for firestore to omit
		if testCfg.ConcurrentBackend != nil {
			return nil, nil, test.ErrConcurrentAccessNotSupported
		}

		clock := clockwork.NewFakeClock()

		uut, err := New(context.Background(), cfg, Options{Clock: clock})
		if err != nil {
			return nil, nil, trace.Wrap(err)
		}

		return uut, clock, nil
	}

	test.RunBackendComplianceSuite(t, newBackend)
}

// newBackend creates a self-closing firestore backend
func newBackend(t *testing.T, cfg map[string]interface{}) *Backend {
	clock := clockwork.NewFakeClock()

	uut, err := New(context.Background(), cfg, Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uut.Close()) })

	return uut
}

func TestReadLegacyRecord(t *testing.T) {
	cfg := firestoreParams()
	ensureTestsEnabled(t)
	ensureEmulatorRunning(t, cfg)

	uut := newBackend(t, cfg)

	item := backend.Item{
		Key:     []byte("legacy-record"),
		Value:   []byte("foo"),
		Expires: uut.clock.Now().Add(time.Minute).Round(time.Second).UTC(),
		ID:      uut.clock.Now().UTC().UnixNano(),
	}

	// Write using legacy record format, emulating data written by an older
	// version of this backend.
	ctx := context.Background()
	rl := legacyRecord{
		Key:       string(item.Key),
		Value:     string(item.Value),
		Expires:   item.Expires.UTC().Unix(),
		Timestamp: uut.clock.Now().UTC().Unix(),
		ID:        item.ID,
	}
	_, err := uut.svc.Collection(uut.CollectionName).Doc(uut.keyToDocumentID(item.Key)).Set(ctx, rl)
	require.NoError(t, err)

	// Read the data back and make sure it matches the original item.
	got, err := uut.Get(ctx, item.Key)
	require.NoError(t, err)
	require.Equal(t, item.Key, got.Key)
	require.Equal(t, item.Value, got.Value)
	require.Equal(t, item.ID, got.ID)
	require.Equal(t, item.Expires, got.Expires)

	// Read the data back using a range query too.
	gotRange, err := uut.GetRange(ctx, item.Key, item.Key, 1)
	require.NoError(t, err)
	require.Len(t, gotRange.Items, 1)

	got = &gotRange.Items[0]
	require.Equal(t, item.Key, got.Key)
	require.Equal(t, item.Value, got.Value)
	require.Equal(t, item.ID, got.ID)
	require.Equal(t, item.Expires, got.Expires)
}

type mockFirestoreServer struct {
	// Embed for forward compatibility.
	// Tests will keep working if more methods are added
	// in the future.
	firestorepb.FirestoreServer

	reqs []proto.Message

	// If set, Commit returns this error.
	commitErr error
}

func (s *mockFirestoreServer) Commit(ctx context.Context, req *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if xg := md["x-goog-api-client"]; len(xg) == 0 || !strings.Contains(xg[0], "gl-go/") {
		return nil, fmt.Errorf("x-goog-api-client = %v, expected gl-go key", xg)
	}

	if len(req.Writes) > commitLimit {
		return nil, status.Errorf(codes.InvalidArgument, "too many writes in a transaction")
	}

	s.reqs = append(s.reqs, req)
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	return &firestorepb.CommitResponse{
		WriteResults: []*firestorepb.WriteResult{{
			UpdateTime: timestamppb.Now(),
		}},
	}, nil
}

func TestDeleteDocuments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		assertion   require.ErrorAssertionFunc
		responseErr error
		commitErr   error
		documents   int
	}{
		{
			name:      "failed to commit",
			assertion: require.Error,
			commitErr: errors.New("failed to commit documents"),
			documents: 1,
		},
		{
			name:      "commit less than limit",
			assertion: require.NoError,
			documents: commitLimit - 123,
		},
		{
			name:      "commit limit",
			assertion: require.NoError,
			documents: commitLimit,
		},
		{
			name:      "commit more than limit",
			assertion: require.NoError,
			documents: (commitLimit * 3) + 173,
		},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			docs := make([]*firestore.DocumentSnapshot, 0, tt.documents)
			for i := 0; i < tt.documents; i++ {
				docs = append(docs, &firestore.DocumentSnapshot{
					Ref: &firestore.DocumentRef{
						Path: fmt.Sprintf("projects/test-project/databases/test-db/documents/test/%d", i+1),
					},
					CreateTime: time.Now(),
					UpdateTime: time.Now(),
				})
			}

			mockFirestore := &mockFirestoreServer{
				commitErr: tt.commitErr,
			}
			srv := grpc.NewServer()
			firestorepb.RegisterFirestoreServer(srv, mockFirestore)

			lis, err := net.Listen("tcp", "localhost:0")
			require.NoError(t, err)

			errCh := make(chan error, 1)
			go func() { errCh <- srv.Serve(lis) }()
			t.Cleanup(func() {
				srv.Stop()
				require.NoError(t, <-errCh)
			})

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			conn, err := grpc.Dial(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, err)

			client, err := firestore.NewClient(ctx, "test-project", option.WithGRPCConn(conn))
			require.NoError(t, err)

			b := &Backend{
				svc:           client,
				Entry:         utils.NewLoggerForTests().WithFields(logrus.Fields{trace.Component: BackendName}),
				clock:         clockwork.NewFakeClock(),
				clientContext: ctx,
				clientCancel:  cancel,
				backendConfig: backendConfig{
					Config: Config{
						CollectionName: "test-collection",
					},
				},
			}

			tt.assertion(t, b.deleteDocuments(docs))

			if tt.documents == 0 {
				return
			}

			var committed int
			for _, req := range mockFirestore.reqs {
				switch r := req.(type) {
				case *firestorepb.CommitRequest:
					committed += len(r.Writes)
				}
			}

			require.Equal(t, tt.documents, committed)

		})
	}

}
