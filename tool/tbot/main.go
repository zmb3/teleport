package main

import (
	"context"
	"crypto/x509"
	"os"
	"time"

	"github.com/gravitational/teleport"
	api "github.com/gravitational/teleport/api/client"
	apidefaults "github.com/gravitational/teleport/api/defaults"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/auth/native"
	"github.com/gravitational/teleport/lib/defaults"
	"github.com/gravitational/teleport/lib/renew"
	"github.com/gravitational/teleport/lib/srv"
	"github.com/gravitational/teleport/lib/tlsca"
	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
)

// TODO: CLI arguments for all of these
var (
	token      = "d01285c4dc18a0462506bf8d58a2b249"
	authServer = "localhost:3025"

	nodeName = "test3"

	dest = "dir:/Users/tim/certs"
)

var log = logrus.WithFields(logrus.Fields{
	trace.Component: teleport.ComponentTSH,
})

const (
	authServerEnvVar  = "TELEPORT_AUTH_SERVER"
	clusterNameEnvVar = "TELEPORT_CLUSTER_NAME"
	tokenEnvVar       = "TELEPORT_BOT_TOKEN"
)

// TODO: need to store the bot's host ID and the name of the cluster
// we're connecting to - should we just dump that in the store?

// func main() {
// 	if err := mainUserCerts(); err != nil {
// 		//if err := mainHostCerts(); err != nil {
// 		log.Fatalf("error: %s", trace.DebugReport(err))
// 	}
//}

type CLIConf struct {
	Debug       bool
	AuthServer  string
	ClusterName string
	DataDir     string
	// CAPins is a list of pinned SKPI hashes of trusted auth server CAs, used
	// only on first connect.
	CAPins []string
	// CAPath is the path to the auth server CA certificate, if available. Used
	// only on first connect.
	CAPath string

	Token string
	Name  string
}

func main() {
	if err := Run(os.Args[1:]); err != nil {
		utils.FatalError(err)
		trace.DebugReport(err)
	}
}

func Run(args []string) error {
	var cf CLIConf
	utils.InitLogger(utils.LoggingForDaemon, logrus.InfoLevel)

	app := utils.InitCLIParser("tbot", "tbot: Teleport Credential Bot").Interspersed(false)
	app.Flag("auth-server", "Specify the Teleport auth server host").Short('a').Envar(authServerEnvVar).Required().StringVar(&cf.AuthServer)
	app.Flag("cluster-name", "Specify the Teleport cluster name").Short('c').Envar(clusterNameEnvVar).Required().StringVar(&cf.ClusterName)
	app.Flag("debug", "Verbose logging to stdout").Short('d').BoolVar(&cf.Debug)

	startCmd := app.Command("start", "Starts the renewal bot.")
	startCmd.Flag("name", "The bot name.").StringVar(&cf.Name)
	startCmd.Flag("token", "A bot join token, if attempting to onboard a new bot; used on first connect.").Envar(tokenEnvVar).StringVar(&cf.Token)
	startCmd.Flag("ca-pin", "A repeatable auth server CA hash to pin; used on first connect.").StringsVar(&cf.CAPins)
	startCmd.Arg("data-dir", "Directory in which to write certificate files.").Required().StringVar(&cf.DataDir)

	configCmd := app.Command("config", "Generate application-specific configuration.")

	command, err := app.Parse(args)
	if err != nil {
		return trace.Wrap(err)
	}

	// While in debug mode, send logs to stdout.
	if cf.Debug {
		utils.InitLogger(utils.LoggingForDaemon, logrus.DebugLevel)
	}

	log.Debugf("args: %+v", cf)

	switch command {
	case startCmd.FullCommand():
		err = onStart(&cf)
	case configCmd.FullCommand():
		err = onConfig(&cf)
	default:
		// This should only happen when there's a missing switch case above.
		err = trace.BadParameter("command %q not configured", command)
	}

	return err
}

func onStart(cf *CLIConf) error {
	log.Info("onStart()")

	// TODO: for now, destination is always dir
	dest, err := renew.NewDestination(&renew.DestinationSpec{
		Type:     renew.DestinationDir,
		Location: cf.DataDir,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	addr, err := utils.ParseAddr(cf.AuthServer)
	if err != nil {
		return trace.WrapWithMessage(err, "invalid auth server address %+v", cf.AuthServer)
	}

	// First, attempt to load an identity from the given destination
	ident, err := renew.LoadIdentity(dest)
	if err == nil {
		log.Infof("succesfully loaded identity %+v", ident)
	} else {
		// If the identity can't be loaded, assume we're starting fresh and
		// need to generate our initial identity from a token

		// TODO: validate that errors from LoadIdentity are sanely typed; we
		// actually only want to ignore NotFound errors

		// If no token is present, we can't continue.
		if cf.Token == "" {
			return trace.Errorf("unable to start: no identity could be loaded and no token present")
		}

		tlsPrivateKey, sshPublicKey, tlsPublicKey, err := generateKeys()
		if err != nil {
			return trace.WrapWithMessage(err, "unable to generate new keypairs")
		}

		params := RegisterParams{
			Token:        cf.Token,
			Servers:      []utils.NetAddr{*addr}, // TODO: multiple servers?
			PrivateKey:   tlsPrivateKey,
			PublicTLSKey: tlsPublicKey,
			PublicSSHKey: sshPublicKey,
			CipherSuites: utils.DefaultCipherSuites(),
			CAPins:       cf.CAPins,
			CAPath:       cf.CAPath,
			Clock:        clockwork.NewRealClock(),
		}

		log.Info("attempting to generate new identity from token")
		ident, err = newIdentityViaAuth(params)
		if err != nil {
			return trace.Wrap(err)
		}

		// TODO: consider `memory` dest type for testing / ephemeral use / etc

		log.Infof("storing new identity to destination: %+v", dest)
		if err := renew.SaveIdentity(ident, dest); err != nil {
			return trace.WrapWithMessage(err, "unable to save generated identity back to destination")
		}
	}

	// TODO: handle cases where an identity exists on disk but we might _not_
	// want to use it:
	//  - identity has expired
	//  - user provided a new token
	//  - ???

	authClient, err := authenticatedUserClientFromIdentity(ident, cf.AuthServer)
	if err != nil {
		return trace.Wrap(err)
	}

	// dummy to test auth api
	name, err := authClient.GetClusterName()
	if err != nil {
		return trace.WrapWithMessage(err, "could not use auth api")
	}

	log.Infof("name: %s", name)

	// TODO: obviously the sshPublicKey public key needs to be persisted
	// tlsPrivateKey, sshPublicKey, _, err := generateKeys()
	// if err != nil {
	// 	return trace.Wrap(err)
	// }

	// // TODO: borrow CA loading logic from auth.Register flow; this is totally
	// // insecure
	// client, err := insecureUserClient(cf.AuthServer)
	// if err != nil {
	// 	return trace.WrapWithMessage(err, "Could not create an insecure auth client")
	// }

	// certs, err := client.GenerateInitialRenewableUserCerts(auth.RenewableCertsRequest{
	// 	Token:     cf.Token,
	// 	PublicKey: sshPublicKey,
	// })
	// if err != nil {
	// 	return trace.WrapWithMessage(err, "Could not generate initial user certificates")
	// }

	//fmt.Printf("certs: %+v\n", certs)

	// decodedCert, _ := pem.Decode(certs.TLS)
	// tlsCert, err := x509.ParseCertificate(decodedCert.Bytes)
	// if err != nil {
	// 	return trace.Wrap(err)
	// }

	// log.Printf("cert: %+v", tlsCert)

	// log.Println("attempting to create authenticated client")
	// client, err = authenticatedUserClient(cf.AuthServer, cf.ClusterName, tlsPrivateKey, certs)
	// if err != nil {
	// 	return trace.Wrap(err)
	// }

	// log.Println("attempting to renew user certs")
	// certs, err = client.GenerateUserCerts(context.Background(), proto.UserCertsRequest{
	// 	PublicKey: sshPublicKey,
	// 	Username:  tlsCert.Subject.CommonName,
	// 	Expires:   time.Now().Add(time.Hour),
	// })
	// if err != nil {
	// 	return trace.Wrap(err)
	// }

	// decodedCert, _ = pem.Decode(certs.TLS)
	// tlsCert, err = x509.ParseCertificate(decodedCert.Bytes)
	// if err != nil {
	// 	return trace.Wrap(err)
	// }
	// log.Printf("renewed cert: %+v", tlsCert)

	return nil
}

// newIdentityViaAuth contacts the auth server directly to exchange a token for
// a new set of user certificates.
func newIdentityViaAuth(params RegisterParams) (*renew.Identity, error) {
	var client *auth.Client
	var rootCAs []*x509.Certificate
	var err error

	// Build a client to the Auth Server. If a CA pin is specified require the
	// Auth Server is validated. Otherwise attempt to use the CA file on disk
	// but if it's not available connect without validating the Auth Server CA.
	switch {
	case len(params.CAPins) != 0:
		client, rootCAs, err = pinAuthClient(params)
	default:
		// TODO: need to handle an empty list of root CAs in this case. Should
		// we just trust on first connect and save the root CA?
		client, rootCAs, err = insecureAuthClient(params)
	}
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer client.Close()
	if err != nil {
		return nil, trace.WrapWithMessage(err, "Could not create an unauthenticated auth client")
	}

	// Note: GenerateInitialRenewableUserCerts will fetch _only_ the cluster's
	// user CA cert. However, to communicate with the auth server, we'll also
	// need to fetch the cluster's host CA cert, which we should have fetched
	// earlier while initializing the auth client.
	certs, err := client.GenerateInitialRenewableUserCerts(auth.RenewableCertsRequest{
		Token:     params.Token,
		PublicKey: params.PublicSSHKey,
	})
	if err != nil {
		return nil, trace.WrapWithMessage(err, "Could not generate initial user certificates")
	}

	// Append any additional root CAs we receieved as part of the auth process
	// (i.e. the host CA cert)
	for _, cert := range rootCAs {
		log.Debugf("appending additional root ca: %+v", cert)
		pemBytes, err := tlsca.MarshalCertificatePEM(cert)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		certs.TLSCACerts = append(certs.TLSCACerts, pemBytes)
	}

	return renew.ReadIdentityFromKeyPair(params.PrivateKey, certs)
}

func onConfig(cf *CLIConf) error {
	log.Info("onConfig()")
	return trace.Errorf("not implemented")
}

func authenticatedUserClientFromIdentity(id *renew.Identity, authServer string) (*auth.Client, error) {
	tlsConfig, err := id.TLSConfig([]uint16{})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	log.Debugf("tlsConfig: %+v", tlsConfig)

	//tlsConfig.InsecureSkipVerify = true

	addr, err := utils.ParseAddr(authServer)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	log.Infof("addr: %+v", addr)

	return auth.NewClient(api.Config{
		Addrs: utils.NetAddrsToStrings([]utils.NetAddr{*addr}),
		Credentials: []api.Credentials{
			api.LoadTLS(tlsConfig),
		},
	})
}

// func mainHostCerts() error {
// 	addr := utils.MustParseAddr(authServer)

// 	ds, err := renew.ParseDestinationSpec(dest)
// 	if err != nil {
// 		return trace.Wrap(err)
// 	}

// 	store, err := renew.NewDestination(ds)
// 	if err != nil {
// 		return trace.Wrap(err)
// 	}

// 	id, err := renew.LoadIdentity(store)
// 	if err != nil {
// 		log.Println("could not load identity, starting new registration", err)
// 		privateKey, sshPublicKey, tlsPublicKey, err := generateKeys()
// 		if err != nil {
// 			return trace.Wrap(err)
// 		}
// 		hostID := uuid.New().String()
// 		id, err = auth.Register(auth.RegisterParams{
// 			Token: token,
// 			ID: auth.IdentityID{
// 				Role:     types.RoleNode,
// 				HostUUID: hostID,
// 				NodeName: nodeName,
// 			},
// 			Servers: []utils.NetAddr{*addr},
// 			CAPins:  []string{}, // TODO

// 			DNSNames:             nil,
// 			AdditionalPrincipals: nil,

// 			GetHostCredentials: client.HostCredentials,

// 			PrivateKey:   privateKey,
// 			PublicTLSKey: tlsPublicKey,
// 			PublicSSHKey: sshPublicKey,
// 		})
// 		if err != nil {
// 			return trace.WrapWithMessage(err, "could not register")
// 		}

// 		log.Println("registered with auth server, saving certs to disk!")

// 		if err := renew.SaveIdentity(id, store); err != nil {
// 			return trace.Wrap(err)
// 		}
// 	} else {
// 		// TODO: handle case where these certs are too old..
// 		log.Println("connecting to auth server with existing certificates")
// 	}

// 	tc, err := id.TLSConfig(nil)
// 	if err != nil {
// 		return trace.Wrap(err)
// 	}

// 	client, err := api.New(context.Background(), api.Config{
// 		Addrs:                    []string{authServer},
// 		Credentials:              []api.Credentials{api.LoadTLS(tc)},
// 		InsecureAddressDiscovery: true,
// 	})
// 	if err != nil {
// 		return trace.Wrap(err)
// 	}

// 	if err := startServiceHeartbeat(client, id.ID.HostUUID); err != nil {
// 		return trace.Wrap(err)
// 	}

// 	// log.Println("generating user certs")
// 	// userCerts, err := client.GenerateUserCerts(context.Background(), proto.UserCertsRequest{
// 	// 	PublicKey: id.KeySigner.PublicKey().Marshal(),
// 	// 	Username:  "test3",
// 	// 	Expires:   time.Now().UTC().Add(4 * time.Hour),
// 	// 	Usage:     proto.UserCertsRequest_All, // TODO: allow pinning to a specific node with NodeName
// 	// })
// 	// if err != nil {
// 	// 	log.Fatalln("could not generate user certs", err)
// 	// }

// 	//log.Println("generated user certs!")
// 	//log.Println("SSH:", string(userCerts.SSH))

// 	// log.Println("waiting for signals: ^C to rotate, ^\\ to exit")
// 	// ch := make(chan os.Signal, 1)
// 	// signal.Notify(ch, os.Interrupt)

// 	// for {
// 	// 	select {
// 	// 	case <-ch:
// 	// 		log.Println("rotating due to signal")
// 	// 	}
// 	// }

// 	return nil
// }

func rotate(client auth.ClientI, hostID string) error {
	priv, ssh, tls, err := generateKeys()
	if err != nil {
		return err
	}

	id, err := auth.ReRegister(auth.ReRegisterParams{
		Client: client,
		ID: auth.IdentityID{
			Role:     types.RoleNode,
			HostUUID: hostID,
			NodeName: nodeName,
		},
		PrivateKey:           priv,
		PublicSSHKey:         ssh,
		PublicTLSKey:         tls,
		Rotation:             types.Rotation{}, // todo
		DNSNames:             nil,
		AdditionalPrincipals: nil,
	})
	if err != nil {
		return err
	}

	_ = id
	return nil
}

func startServiceHeartbeat(c *api.Client, hostID string) error {
	heartbeat, err := srv.NewHeartbeat(srv.HeartbeatConfig{
		Context:   context.Background(),
		Component: teleport.ComponentBot,
		Mode:      srv.HeartbeatModeBot,
		Announcer: announcerAdapter{c},
		GetServerInfo: func() (types.Resource, error) {
			bot := &types.BotV3{
				ResourceHeader: types.ResourceHeader{
					Metadata: types.Metadata{
						Name:      nodeName,
						Namespace: apidefaults.Namespace,
					},
					Version: types.V3,
					Kind:    types.KindBot,
				},
				Spec: types.BotSpecV3{
					HostID: hostID,
				},
			}
			bot.SetExpiry(time.Now().UTC().Add(apidefaults.ServerAnnounceTTL))
			return bot, nil
		},
		KeepAlivePeriod: apidefaults.ServerKeepAliveTTL,
		AnnouncePeriod:  apidefaults.ServerAnnounceTTL/2 + utils.RandomDuration(apidefaults.ServerAnnounceTTL/10),
		CheckPeriod:     defaults.HeartbeatCheckPeriod,
		ServerTTL:       apidefaults.ServerAnnounceTTL,
		OnHeartbeat: func(err error) {
			log.Println("heartbeat completed with error", err)
		},
	})
	if err != nil {
		return trace.Wrap(err)
	}
	go func() {
		if err := heartbeat.Run(); err != nil {
			log.Println("heartbeat ended with error")
		}
	}()
	return nil
}

func generateKeys() (private, sshpub, tlspub []byte, err error) {
	privateKey, publicKey, err := native.GenerateKeyPair("")
	if err != nil {
		return nil, nil, nil, err
	}

	sshPrivateKey, err := ssh.ParseRawPrivateKey(privateKey)
	if err != nil {
		return nil, nil, nil, err
	}

	tlsPublicKey, err := tlsca.MarshalPublicKeyFromPrivateKeyPEM(sshPrivateKey)
	if err != nil {
		return nil, nil, nil, err
	}

	return privateKey, publicKey, tlsPublicKey, nil
}

// API client can't upsert core components like auth servers and proxies,
// so just nop those calls

type announcerAdapter struct{ *api.Client }

func (a announcerAdapter) UpsertAuthServer(s types.Server) error { return nil }
func (a announcerAdapter) UpsertProxy(s types.Server) error      { return nil }

// API client doesn't implement all of ClientI

type clientiAdapter struct{ *api.Client }
