/*
Copyright 2021 Gravitational, Inc.

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

package common

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gravitational/teleport/lib/client"
	"github.com/gravitational/teleport/lib/client/identityfile"
	"github.com/gravitational/trace"
)

type CertStore interface {
	// Save stores signed keys and certificates.
	Save(*client.Key) error
	// Type returns whether the store holds user or host certs.
	Type() storeType
	// Expiration returns the time when the current certificates will expire.
	Expiration() time.Time
	// SubscribeRefresh returns a channel to signal when the
	// Cert Store's certificates have been refreshed.
	SubscribeRefresh(ctx context.Context) <-chan struct{}
	// Done signals that the CertStore is closed.
	Done() <-chan struct{}
	// Close closes open channels.
	Close() error
}

type storeType string

const (
	typeHostStore storeType = "host"
	typeUserStore storeType = "user"
)

func NewCertStore(ctx context.Context, format identityfile.Format, path string) (CertStore, error) {
	switch format {
	case identityfile.FormatOpenSSH:
		return newOpenSSHStore(ctx, path), nil
	default:
		return nil, trace.BadParameter("unsupported format provided: %v", format)
	}
}

type store struct {
	done           chan struct{}
	refresh        chan struct{}
	subscribersMux *sync.RWMutex
	subscribers    []chan struct{}
}

func newStore(ctx context.Context) *store {
	store := &store{
		done:    make(chan struct{}),
		refresh: make(chan struct{}),
	}
	go store.watchRefresh(ctx)
	return store
}

func (s *store) watchRefresh(ctx context.Context) {
	for {
		select {
		case <-s.refresh:
			s.notifyRefresh()
		case <-s.done:
			return
		case <-ctx.Done():
			s.Close()
			return
		}
	}
}

func (s *store) SubscribeRefresh(ctx context.Context) <-chan struct{} {
	s.subscribersMux.Lock()
	defer s.subscribersMux.Unlock()

	refresh := make(chan struct{})
	s.subscribers = append(s.subscribers, refresh)
	return refresh
}

func (s *store) notifyRefresh() {
	s.subscribersMux.RLock()
	defer s.subscribersMux.RUnlock()
	for _, s := range s.subscribers {
		select {
		case s <- struct{}{}:
		default:
		}
	}
}

func (s *store) Close() {
	close(s.done)
	close(s.refresh)
}

func (s *store) Done() <-chan struct{} {
	return s.done
}

type openSSHStore struct {
	*store
	// path is the path to the public key. It is also used
	// as a prefix for the SSH cert and User CA cert paths.
	path string
}

func newOpenSSHStore(ctx context.Context, path string) *openSSHStore {
	store := &openSSHStore{
		store: newStore(ctx),
		path:  path,
	}
	return store
}

func (s *openSSHStore) Load() (*client.Key, error) {
	return identityfile.Read(identityfile.Readconfig{
		Path:   s.path,
		Format: identityfile.FormatOpenSSH,
	})
}

func (s *openSSHStore) Save(key *client.Key) error {
	filesWritten, err := identityfile.Write(identityfile.WriteConfig{
		Key:                  key,
		OutputPath:           s.path,
		Format:               identityfile.FormatOpenSSH,
		OverwriteDestination: true,
	})
	if err != nil {
		return trace.Wrap(err)
	}
	fmt.Printf("wrote certs to %v\n", filesWritten)
	s.refresh <- struct{}{}
	return nil
}

func (s *openSSHStore) Expiration() time.Time {
	// TODO get expiration time from cert
	return time.Time{}
}

func (s *openSSHStore) Close() error {
	return nil
}

func (s *openSSHStore) Type() storeType {
	return typeHostStore
}
