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
	"log"
	"time"

	"github.com/gravitational/teleport/api/client"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/auth/native"
	libclient "github.com/gravitational/teleport/lib/client"
	"github.com/gravitational/teleport/lib/tlsca"
	"github.com/gravitational/teleport/lib/utils"

	"github.com/gravitational/trace"
	"github.com/pborman/uuid"
	"golang.org/x/crypto/ssh"
)

type CertBot struct {
	cfg    Config
	clt    *auth.Client
	stores []CertStore
	done   chan struct{}
}

func NewCertBot(ctx context.Context, cfg Config) (*CertBot, error) {
	id, err := Register(cfg.Addrs, cfg.AuthToken)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	tlsConfig, err := id.TLSConfig(utils.DefaultCipherSuites())
	if err != nil {
		return nil, trace.Wrap(err)
	}

	clt, err := auth.NewClient(client.Config{
		Addrs: utils.NetAddrsToStrings(cfg.Addrs),
		Credentials: []client.Credentials{
			client.LoadTLS(tlsConfig),
		},
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	for _, params := range cfg.CertStoreParams {
		store, err := NewCertStore(ctx, params.format, params.path)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		if params.reload != nil {
			go func(reload func() error) {
				refreshed := store.SubscribeRefresh(ctx)
				for {
					select {
					case <-refreshed:
						if err := reload(); err != nil {
							// TODO Log error and exponential backoff
						}
					}
				}
			}(params.reload)
		}
	}

	return &CertBot{
		cfg: cfg,
		clt: clt,
	}, nil
}

func (cb *CertBot) Close() error {
	close(cb.done)
	cb.clt.Close()
	for _, store := range cb.stores {
		store.Close()
	}
	return nil
}

func (cb *CertBot) Done() <-chan struct{} {
	return cb.done
}

func Register(addrs []utils.NetAddr, token string) (*auth.Identity, error) {
	privPEM, pubSSH, err := native.GenerateKeyPair("")
	if err != nil {
		return nil, trace.Wrap(err)
	}
	privateKey, err := ssh.ParseRawPrivateKey(privPEM)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	pubTLS, err := tlsca.MarshalPublicKeyFromPrivateKeyPEM(privateKey)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return auth.Register(auth.RegisterParams{
		ID: auth.IdentityID{
			Role:     types.RoleNode,
			HostUUID: uuid.New(),
			NodeName: "tbot",
		},
		Token:        token,
		Servers:      addrs,
		PrivateKey:   privPEM,
		PublicTLSKey: pubTLS,
		PublicSSHKey: pubSSH,
	})
}

func (cb *CertBot) Run(ctx context.Context) error {
	cb.watchCARotation(ctx)
	cb.watchExpiration(ctx)
	select {
	case <-ctx.Done():
		defer cb.Close()
		return ctx.Err()
	case <-cb.Done():
		return nil
	}
}

func (cb *CertBot) watchCARotation(ctx context.Context) {
	watcher, err := cb.clt.NewWatcher(ctx, types.Watch{
		Kinds: []types.WatchKind{{
			Kind: types.KindCertAuthority,
		}},
	})
	if err != nil {
		return
	}

	go func() {
		for event := range watcher.Events() {
			if event.Type == types.OpPut {
				ca, ok := event.Resource.(*types.CertAuthorityV2)
				if !ok {
					log.Printf("expected event resource to be types.CertAuthorityV2, got %T", event.Resource)
				}
				switch ca.Spec.Rotation.Phase {
				case types.RotationPhaseUpdateClients:
					fmt.Println("user CA rotation detected")
					for _, store := range cb.stores {
						if store.Type() == typeUserStore {
							go cb.renewCerts(ctx, store)
						}
					}
				case types.RotationPhaseUpdateServers:
					fmt.Println("host CA rotation detected")
					for _, store := range cb.stores {
						if store.Type() == typeHostStore {
							go cb.renewCerts(ctx, store)
						}
					}
				default:
					fmt.Println("Rotation Phase detected: ", ca.Spec.Rotation.Phase)
				}
			}
		}
	}()
}

func (cb *CertBot) watchExpiration(ctx context.Context) {
	for _, store := range cb.stores {
		go func(ctx context.Context, store CertStore) {
			refreshed := store.SubscribeRefresh(ctx)
			for {
				select {
				case <-time.After(time.Until(store.Expiration().Add(time.Minute * -10))):
					fmt.Println("Expiration detected")
					go cb.renewCerts(ctx, store)
				case <-refreshed:
					// reset select statement to get updated expiration time
				case <-store.Done():
					log.Printf("store closed")
					return
				case <-ctx.Done():
					log.Printf("ctx closed")
					return
				}
			}
		}(ctx, store)
	}
}

// renewCerts renews a Cert Store's certificates, retrying with exponential backoff upon error
func (cb *CertBot) renewCerts(ctx context.Context, store CertStore) {
	switch store.Type() {
	case types.RotationPhaseUpdateClients:
		// TODO cb.clt.GenerateUserCerts(ctx, proto.UserCertsRequest{})
	case types.RotationPhaseUpdateServers:
		keys, err := cb.generateHostCerts()
		if err != nil {
			// TODO try again after exponential backoff? Send retry signal?
		}

		if err = store.Save(keys); err != nil {
			// TODO try again after exponential backoff? Send retry signal?
		}
	}
	fmt.Println("Renewed store certificates")
}

func (cb *CertBot) generateHostCerts() (*libclient.Key, error) {
	// // split up comma separated list
	// principals := strings.Split(a.genHost, ",")

	// generate a keypair
	key, err := libclient.NewKey()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	cn, err := cb.clt.GetClusterName()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	clusterName := cn.GetClusterName()

	key.Cert, err = cb.clt.GenerateHostCert(
		key.Pub, "", "", nil,
		clusterName, types.SystemRoles{types.RoleNode}, 0,
	)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	hostCAs, err := cb.clt.GetCertAuthorities(types.HostCA, false)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	key.TrustedCA = auth.AuthoritiesToTrustedCerts(hostCAs)

	return key, nil
}

// keys, err := cb.clt.GenerateServerKeys(auth.GenerateServerKeysRequest{
// 	// HostID is a unique ID of the host
// 	HostID string `json:"host_id"`
// 	// NodeName is a user friendly host name
// 	NodeName string `json:"node_name"`
// 	// Roles is a list of roles assigned to node
// 	Roles types.SystemRoles `json:"roles"`
// 	// AdditionalPrincipals is a list of additional principals
// 	// to include in OpenSSH and X509 certificates
// 	AdditionalPrincipals []string `json:"additional_principals"`
// 	// DNSNames is a list of DNS names
// 	// to include in the x509 client certificate
// 	DNSNames []string `json:"dns_names"`
// 	// PublicTLSKey is a PEM encoded public key
// 	// used for TLS setup
// 	PublicTLSKey []byte `json:"public_tls_key"`
// 	// PublicSSHKey is a SSH encoded public key,
// 	// if present will be signed as a return value
// 	// otherwise, new public/private key pair will be generated
// 	PublicSSHKey []byte `json:"public_ssh_key"`
// 	// RemoteAddr is the IP address of the remote host requesting a host
// 	// certificate. RemoteAddr is used to replace 0.0.0.0 in the list of
// 	// additional principals.
// 	RemoteAddr string `json:"remote_addr"`
// 	// Rotation allows clients to send the certificate authority rotation state
// 	// expected by client of the certificate authority backends, so auth servers
// 	// can avoid situation when clients request certs assuming one
// 	// state, and auth servers issue another
// 	Rotation *types.Rotation `json:"rotation,omitempty"`
// 	// NoCache is argument that only local callers can supply to bypass cache
// 	NoCache bool `json:"-"`
// })
