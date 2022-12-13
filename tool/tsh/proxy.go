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

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/gravitational/trace"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/zmb3/teleport/api/client"
	"github.com/zmb3/teleport/api/client/proxy"
	"github.com/zmb3/teleport/api/client/webclient"
	"github.com/zmb3/teleport/api/constants"
	tracessh "github.com/zmb3/teleport/api/observability/tracing/ssh"
	"github.com/zmb3/teleport/api/types"
	"github.com/zmb3/teleport/api/utils/keys"
	libclient "github.com/zmb3/teleport/lib/client"
	"github.com/zmb3/teleport/lib/client/db/dbcmd"
	"github.com/zmb3/teleport/lib/defaults"
	"github.com/zmb3/teleport/lib/srv/alpnproxy"
	alpncommon "github.com/zmb3/teleport/lib/srv/alpnproxy/common"
	"github.com/zmb3/teleport/lib/utils"
)

// onProxyCommandSSH creates a local ssh proxy.
// In cases of TLS Routing the connection is established to the WebProxy with teleport-proxy-ssh ALPN protocol.
// and all ssh traffic is forwarded through the local ssh proxy.
//
// If proxy doesn't support TLS Routing the onProxyCommandSSH is used as ProxyCommand to remove proxy/site prefixes
// from destination node address to support multiple platform where 'cut -d' command is not provided.
// For more details please look at: Generate Windows-compatible OpenSSH config https://github.com/gravitational/teleport/pull/7848
func onProxyCommandSSH(cf *CLIConf) error {
	tc, err := makeClient(cf, false)
	if err != nil {
		return trace.Wrap(err)
	}

	err = libclient.RetryWithRelogin(cf.Context, tc, func() error {
		proxyParams, err := getSSHProxyParams(cf, tc)
		if err != nil {
			return trace.Wrap(err)
		}

		if len(tc.JumpHosts) > 0 {
			err := setupJumpHost(cf, tc, *proxyParams)
			if err != nil {
				return trace.Wrap(err)
			}
		}
		return trace.Wrap(sshProxy(cf.Context, tc, *proxyParams))
	})
	return trace.Wrap(err)
}

// sshProxyParams combines parameters for establishing an SSH proxy used
// as a ProxyCommand for SSH clients.
type sshProxyParams struct {
	// proxyHost is the Teleport proxy host name.
	proxyHost string
	// proxyPort is the Teleport proxy port.
	proxyPort string
	// targetHost is the target SSH node host name.
	targetHost string
	// targetPort is the target SSH node port.
	targetPort string
	// clusterName is the cluster where the SSH node resides.
	clusterName string
	// tlsRouting is true if the Teleport proxy has TLS routing enabled.
	tlsRouting bool
}

// getSSHProxyParams prepares parameters for establishing an SSH proxy connection.
func getSSHProxyParams(cf *CLIConf, tc *libclient.TeleportClient) (*sshProxyParams, error) {
	targetHost, targetPort, err := net.SplitHostPort(tc.Host)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// Without jump hosts, we will be connecting to the current Teleport client
	// proxy the user is logged into.
	if len(tc.JumpHosts) == 0 {
		proxyHost, proxyPort := tc.SSHProxyHostPort()
		if tc.TLSRoutingEnabled {
			proxyHost, proxyPort = tc.WebProxyHostPort()
		}
		return &sshProxyParams{
			proxyHost:   proxyHost,
			proxyPort:   strconv.Itoa(proxyPort),
			targetHost:  cleanTargetHost(targetHost, tc.WebProxyHost(), tc.SiteName),
			targetPort:  targetPort,
			clusterName: tc.SiteName,
			tlsRouting:  tc.TLSRoutingEnabled,
		}, nil
	}

	// When jump host is specified, we will be connecting to the jump host's
	// proxy directly. Call its ping endpoint to figure out the cluster details
	// such as cluster name, SSH proxy address, etc.
	ping, err := webclient.Find(&webclient.Config{
		Context:   cf.Context,
		ProxyAddr: tc.JumpHosts[0].Addr.Addr,
		Insecure:  tc.InsecureSkipVerify,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	sshProxyHost, sshProxyPort, err := ping.Proxy.SSHProxyHostPort()
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return &sshProxyParams{
		proxyHost:   sshProxyHost,
		proxyPort:   sshProxyPort,
		targetHost:  targetHost,
		targetPort:  targetPort,
		clusterName: ping.ClusterName,
		tlsRouting:  ping.Proxy.TLSRoutingEnabled,
	}, nil
}

// cleanTargetHost cleans the targetHost and remote site and proxy suffixes.
// Before the `cut -d` command was used for this purpose but to support multi-platform OpenSSH clients the logic
// it was moved tsh proxy ssh command.
// For more details please look at: Generate Windows-compatible OpenSSH config https://github.com/gravitational/teleport/pull/7848
func cleanTargetHost(targetHost, proxyHost, siteName string) string {
	targetHost = strings.TrimSuffix(targetHost, "."+proxyHost)
	targetHost = strings.TrimSuffix(targetHost, "."+siteName)
	return targetHost
}

// setupJumpHost configures the client for connecting to the jump host's proxy.
func setupJumpHost(cf *CLIConf, tc *libclient.TeleportClient, sp sshProxyParams) error {
	return tc.WithoutJumpHosts(func(tc *libclient.TeleportClient) error {
		// Fetch certificate for the leaf cluster. This allows users to log
		// in once into the root cluster and let the proxy handle fetching
		// certificates for leaf clusters automatically.
		err := tc.LoadKeyForClusterWithReissue(cf.Context, sp.clusterName)
		if err != nil {
			return trace.Wrap(err)
		}

		// We'll be connecting directly to the leaf cluster so make sure agent
		// loads correct host CA.
		tc.LocalAgent().UpdateCluster(sp.clusterName)
		return nil
	})
}

// sshProxy opens up a new SSH session connected to the Teleport Proxy's SSH proxy subsystem,
// This is the equivalent of `ssh -o 'ForwardAgent yes' -p port %r@host -s proxy:%h:%p`.
// If tls routing is enabled, the connection to RemoteProxyAddr is wrapped with TLS protocol.
func sshProxy(ctx context.Context, tc *libclient.TeleportClient, sp sshProxyParams) error {
	upstreamConn, err := dialSSHProxy(ctx, tc, sp)
	if err != nil {
		return trace.Wrap(err)
	}
	defer upstreamConn.Close()

	remoteProxyAddr := net.JoinHostPort(sp.proxyHost, sp.proxyPort)
	client, err := makeSSHClient(ctx, upstreamConn, remoteProxyAddr, &ssh.ClientConfig{
		User: tc.HostLogin,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeysCallback(tc.LocalAgent().Signers),
		},
		HostKeyCallback: tc.HostKeyCallback,
	})
	if err != nil {
		return trace.Wrap(err)
	}
	defer client.Close()

	sess, err := client.NewSession(ctx)
	if err != nil {
		return trace.Wrap(err)
	}
	defer sess.Close()

	err = agent.ForwardToAgent(client.Client, tc.LocalAgent())
	if err != nil {
		return trace.Wrap(err)
	}
	err = agent.RequestAgentForwarding(sess.Session)
	if err != nil {
		return trace.Wrap(err)
	}

	sshUserHost := fmt.Sprintf("%s:%s", sp.targetHost, sp.targetPort)
	if err = sess.RequestSubsystem(ctx, proxySubsystemName(sshUserHost, sp.clusterName)); err != nil {
		return trace.Wrap(err)
	}
	if err := proxySession(ctx, sess); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

// dialSSHProxy opens a net.Conn to the proxy on either the ALPN or SSH
// port, this connection can then be used to initiate a SSH client.
// If the HTTPS_PROXY is configured, then this is used to open the connection
// to the proxy.
func dialSSHProxy(ctx context.Context, tc *libclient.TeleportClient, sp sshProxyParams) (net.Conn, error) {
	// if sp.tlsRouting is true, remoteProxyAddr is the ALPN listener port.
	// if it is false, then remoteProxyAddr is the SSH proxy port.
	remoteProxyAddr := net.JoinHostPort(sp.proxyHost, sp.proxyPort)
	httpsProxy := proxy.GetProxyURL(remoteProxyAddr)

	// If HTTPS_PROXY is configured, we need to open a TCP connection via
	// the specified HTTPS Proxy, otherwise, we can just open a plain TCP
	// connection.
	var tcpConn net.Conn
	var err error
	if httpsProxy != nil {
		tcpConn, err = client.DialProxy(ctx, httpsProxy, remoteProxyAddr)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	} else {
		tcpConn, err = (&net.Dialer{}).DialContext(ctx, "tcp", remoteProxyAddr)
		if err != nil {
			return nil, trace.Wrap(err)
		}
	}

	// If TLS routing is not enabled, just return the TCP connection
	if !sp.tlsRouting {
		return tcpConn, nil
	}

	// Otherwise, we need to upgrade the TCP connection to a TLS connection.
	pool, err := tc.LocalAgent().ClientCertPool(sp.clusterName)
	if err != nil {
		tcpConn.Close()
		return nil, trace.Wrap(err)
	}

	tlsConfig := &tls.Config{
		RootCAs:            pool,
		NextProtos:         []string{string(alpncommon.ProtocolProxySSH)},
		InsecureSkipVerify: tc.InsecureSkipVerify,
		ServerName:         sp.proxyHost,
	}
	tlsConn := tls.Client(tcpConn, tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		tlsConn.Close()
		return nil, trace.Wrap(err)
	}

	return tlsConn, nil
}

func proxySubsystemName(userHost, cluster string) string {
	subsystem := fmt.Sprintf("proxy:%s", userHost)
	if cluster != "" {
		return fmt.Sprintf("%s@%s", subsystem, cluster)
	}
	return fmt.Sprintf("proxy:%s", userHost)
}

func makeSSHClient(ctx context.Context, conn net.Conn, addr string, cfg *ssh.ClientConfig) (*tracessh.Client, error) {
	cc, chs, reqs, err := tracessh.NewClientConn(ctx, conn, addr, cfg)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return tracessh.NewClient(cc, chs, reqs), nil
}

func proxySession(ctx context.Context, sess *tracessh.Session) error {
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return trace.Wrap(err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		return trace.Wrap(err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return trace.Wrap(err)
	}

	errC := make(chan error, 3)
	go func() {
		defer sess.Close()
		_, err := io.Copy(os.Stdout, stdout)
		errC <- err
	}()
	go func() {
		defer sess.Close()
		_, err := io.Copy(stdin, os.Stdin)
		errC <- err
	}()
	go func() {
		defer sess.Close()
		_, err := io.Copy(os.Stderr, stderr)
		errC <- err
	}()
	var errs []error
	for i := 0; i < 3; i++ {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errC:
			if err != nil && !utils.IsOKNetworkError(err) {
				errs = append(errs, err)
			}
		}
	}
	return trace.NewAggregate(errs...)
}

// formatCommand formats command making it suitable for the end user to copy the command and paste it into terminal.
func formatCommand(cmd *exec.Cmd) string {
	// environment variables
	env := strings.Join(cmd.Env, " ")

	var args []string
	for _, arg := range cmd.Args {
		// escape the potential quotes within
		arg = strings.Replace(arg, `"`, `\"`, -1)

		// if there is whitespace within, surround with quotes
		if strings.IndexFunc(arg, unicode.IsSpace) != -1 {
			args = append(args, fmt.Sprintf(`"%s"`, arg))
		} else {
			args = append(args, arg)
		}
	}

	argsfmt := strings.Join(args, " ")

	if len(env) > 0 {
		return fmt.Sprintf("%s %s", env, argsfmt)
	}

	return argsfmt
}

func onProxyCommandDB(cf *CLIConf) error {
	client, err := makeClient(cf, false)
	if err != nil {
		return trace.Wrap(err)
	}
	profile, err := libclient.StatusCurrent(cf.HomePath, cf.Proxy, cf.IdentityFileIn)
	if err != nil {
		return trace.Wrap(err)
	}
	routeToDatabase, db, err := getDatabaseInfo(cf, client, cf.DatabaseService)
	if err != nil {
		return trace.Wrap(err)
	}
	if err := maybeDatabaseLogin(cf, client, profile, routeToDatabase); err != nil {
		return trace.Wrap(err)
	}

	if routeToDatabase.Protocol == defaults.ProtocolSnowflake && !cf.LocalProxyTunnel {
		return trace.BadParameter("Snowflake proxy works only in the tunnel mode. Please add --tunnel flag to enable it")
	}

	rootCluster, err := client.RootClusterName(cf.Context)
	if err != nil {
		return trace.Wrap(err)
	}

	addr := "localhost:0"
	randomPort := true
	if cf.LocalProxyPort != "" {
		randomPort = false
		addr = fmt.Sprintf("127.0.0.1:%s", cf.LocalProxyPort)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return trace.Wrap(err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			log.WithError(err).Warnf("Failed to close listener.")
		}
	}()

	proxyOpts, err := prepareLocalProxyOptions(&localProxyConfig{
		cliConf:          cf,
		teleportClient:   client,
		profile:          profile,
		routeToDatabase:  routeToDatabase,
		listener:         listener,
		localProxyTunnel: cf.LocalProxyTunnel,
		rootClusterName:  rootCluster,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	lp, err := mkLocalProxy(cf.Context, proxyOpts)
	if err != nil {
		return trace.Wrap(err)
	}
	go func() {
		<-cf.Context.Done()
		lp.Close()
	}()

	if cf.LocalProxyTunnel {
		addr, err := utils.ParseAddr(lp.GetAddr())
		if err != nil {
			return trace.Wrap(err)
		}
		var opts = []dbcmd.ConnectCommandFunc{
			dbcmd.WithLocalProxy("localhost", addr.Port(0), ""),
			dbcmd.WithNoTLS(),
			dbcmd.WithLogger(log),
			dbcmd.WithPrintFormat(),
		}
		if opts, err = maybeAddDBUserPassword(db, opts); err != nil {
			return trace.Wrap(err)
		}

		commands, err := dbcmd.NewCmdBuilder(client, profile, routeToDatabase, rootCluster,
			opts...,
		).GetConnectCommandAlternatives()
		if err != nil {
			return trace.Wrap(err)
		}

		// shared template arguments
		templateArgs := map[string]any{
			"database":   routeToDatabase.ServiceName,
			"type":       defaults.ReadableDatabaseProtocol(routeToDatabase.Protocol),
			"cluster":    client.SiteName,
			"address":    listener.Addr().String(),
			"randomPort": randomPort,
		}

		tmpl := chooseProxyCommandTemplate(templateArgs, commands)
		err = tmpl.Execute(os.Stdout, templateArgs)
		if err != nil {
			return trace.Wrap(err)
		}

	} else {
		err = dbProxyTpl.Execute(os.Stdout, map[string]any{
			"database":   routeToDatabase.ServiceName,
			"address":    listener.Addr().String(),
			"ca":         profile.CACertPathForCluster(rootCluster),
			"cert":       profile.DatabaseCertPathForCluster(cf.SiteName, routeToDatabase.ServiceName),
			"key":        profile.KeyPath(),
			"randomPort": randomPort,
		})
		if err != nil {
			return trace.Wrap(err)
		}
	}

	defer lp.Close()
	if err := lp.Start(cf.Context); err != nil {
		return trace.Wrap(err)
	}
	return nil
}

func maybeAddDBUserPassword(db types.Database, opts []dbcmd.ConnectCommandFunc) ([]dbcmd.ConnectCommandFunc, error) {
	if db != nil && db.GetProtocol() == defaults.ProtocolCassandra && db.IsAWSHosted() {
		// Cassandra client always prompt for password, so we need to provide it
		// Provide an auto generated random password to skip the prompt in case of
		// connection to AWS hosted cassandra.
		password, err := utils.CryptoRandomHex(16)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return append(opts, dbcmd.WithPassword(password)), nil
	}
	return opts, nil
}

type templateCommandItem struct {
	Description string
	Command     string
}

func chooseProxyCommandTemplate(templateArgs map[string]any, commands []dbcmd.CommandAlternative) *template.Template {
	// there is only one command, use plain template.
	if len(commands) == 1 {
		templateArgs["command"] = formatCommand(commands[0].Command)
		return dbProxyAuthTpl
	}

	// multiple command options, use a different template.

	var commandsArg []templateCommandItem
	for _, cmd := range commands {
		commandsArg = append(commandsArg, templateCommandItem{cmd.Description, formatCommand(cmd.Command)})
	}

	templateArgs["commands"] = commandsArg
	return dbProxyAuthMultiTpl
}

type localProxyOpts struct {
	proxyAddr               string
	listener                net.Listener
	protocols               []alpncommon.Protocol
	insecure                bool
	certs                   []tls.Certificate
	rootCAs                 *x509.CertPool
	alpnConnUpgradeRequired bool
	middleware              alpnproxy.LocalProxyMiddleware
}

// protocol returns the first protocol or string if configuration doesn't contain any protocols.
func (l *localProxyOpts) protocol() string {
	if len(l.protocols) == 0 {
		return ""
	}
	return string(l.protocols[0])
}

func mkLocalProxy(ctx context.Context, opts *localProxyOpts) (*alpnproxy.LocalProxy, error) {
	alpnProtocol, err := alpncommon.ToALPNProtocol(opts.protocol())
	if err != nil {
		return nil, trace.Wrap(err)
	}
	address, err := utils.ParseAddr(opts.proxyAddr)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	protocols := append([]alpncommon.Protocol{alpnProtocol}, opts.protocols...)
	if alpncommon.HasPingSupport(alpnProtocol) {
		protocols = append(alpncommon.ProtocolsWithPing(alpnProtocol), protocols...)
	}

	lp, err := alpnproxy.NewLocalProxy(alpnproxy.LocalProxyConfig{
		InsecureSkipVerify:      opts.insecure,
		RemoteProxyAddr:         opts.proxyAddr,
		Protocols:               protocols,
		Listener:                opts.listener,
		ParentContext:           ctx,
		SNI:                     address.Host(),
		Certs:                   opts.certs,
		RootCAs:                 opts.rootCAs,
		ALPNConnUpgradeRequired: opts.alpnConnUpgradeRequired,
		Middleware:              opts.middleware,
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return lp, nil
}

func mkLocalProxyCerts(certFile, keyFile string) ([]tls.Certificate, error) {
	if certFile == "" && keyFile == "" {
		return []tls.Certificate{}, nil
	}
	if (certFile == "" && keyFile != "") || (certFile != "" && keyFile == "") {
		return nil, trace.BadParameter("both --cert-file and --key-file are required")
	}
	cert, err := keys.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return []tls.Certificate{cert}, nil
}

func alpnProtocolForApp(app types.Application) alpncommon.Protocol {
	if app.IsTCP() {
		return alpncommon.ProtocolTCP
	}
	return alpncommon.ProtocolHTTP
}

func onProxyCommandApp(cf *CLIConf) error {
	tc, err := makeClient(cf, false)
	if err != nil {
		return trace.Wrap(err)
	}

	appCerts, err := loadAppCertificate(tc, cf.AppName)
	if err != nil {
		return trace.Wrap(err)
	}

	app, err := getRegisteredApp(cf, tc)
	if err != nil {
		return trace.Wrap(err)
	}

	address, err := utils.ParseAddr(tc.WebProxyAddr)
	if err != nil {
		return trace.Wrap(err)
	}

	addr := "localhost:0"
	if cf.LocalProxyPort != "" {
		addr = fmt.Sprintf("127.0.0.1:%s", cf.LocalProxyPort)
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return trace.Wrap(err)
	}

	lp, err := alpnproxy.NewLocalProxy(alpnproxy.LocalProxyConfig{
		Listener:           listener,
		RemoteProxyAddr:    tc.WebProxyAddr,
		Protocols:          []alpncommon.Protocol{alpnProtocolForApp(app)},
		InsecureSkipVerify: cf.InsecureSkipVerify,
		ParentContext:      cf.Context,
		SNI:                address.Host(),
		Certs:              []tls.Certificate{appCerts},
	})
	if err != nil {
		if cerr := listener.Close(); cerr != nil {
			return trace.NewAggregate(err, cerr)
		}
		return trace.Wrap(err)
	}

	fmt.Printf("Proxying connections to %s on %v\n", cf.AppName, lp.GetAddr())
	if cf.LocalProxyPort == "" {
		fmt.Println("To avoid port randomization, you can choose the listening port using the --port flag.")
	}

	go func() {
		<-cf.Context.Done()
		lp.Close()
	}()

	defer lp.Close()
	if err = lp.Start(cf.Context); err != nil {
		log.WithError(err).Errorf("Failed to start local proxy.")
	}

	return nil
}

// onProxyCommandAWS creates local proxes for AWS apps.
func onProxyCommandAWS(cf *CLIConf) error {
	awsApp, err := pickActiveAWSApp(cf)
	if err != nil {
		return trace.Wrap(err)
	}

	err = awsApp.StartLocalProxies()
	if err != nil {
		return trace.Wrap(err)
	}

	defer func() {
		if err := awsApp.Close(); err != nil {
			log.WithError(err).Error("Failed to close AWS app.")
		}
	}()

	envVars, err := awsApp.GetEnvVars()
	if err != nil {
		return trace.Wrap(err)
	}

	templateData := map[string]interface{}{
		"envVars":     envVars,
		"address":     awsApp.GetForwardProxyAddr(),
		"endpointURL": awsApp.GetEndpointURL(),
		"format":      cf.Format,
		"randomPort":  cf.LocalProxyPort == "",
	}

	template := awsHTTPSProxyTemplate
	if cf.AWSEndpointURLMode {
		template = awsEndpointURLProxyTemplate
	}

	if err = template.Execute(os.Stdout, templateData); err != nil {
		return trace.Wrap(err)
	}

	<-cf.Context.Done()
	return nil
}

// loadAppCertificate loads the app certificate for the provided app.
func loadAppCertificate(tc *libclient.TeleportClient, appName string) (tls.Certificate, error) {
	key, err := tc.LocalAgent().GetKey(tc.SiteName, libclient.WithAppCerts{})
	if err != nil {
		return tls.Certificate{}, trace.Wrap(err)
	}
	cert, ok := key.AppTLSCerts[appName]
	if !ok {
		return tls.Certificate{}, trace.NotFound("please login into the application first. 'tsh app login'")
	}

	tlsCert, err := key.TLSCertificate(cert)
	if err != nil {
		return tls.Certificate{}, trace.Wrap(err)
	}

	expiresAt, err := getTLSCertExpireTime(tlsCert)
	if err != nil {
		return tls.Certificate{}, trace.WrapWithMessage(err, "invalid certificate - please login to the application again. 'tsh app login'")
	}
	if time.Until(expiresAt) < 5*time.Second {
		return tls.Certificate{}, trace.BadParameter(
			"application %s certificate has expired, please re-login to the app using 'tsh app login'",
			appName)
	}
	return tlsCert, nil
}

// getTLSCertExpireTime returns the certificate NotAfter time.
func getTLSCertExpireTime(cert tls.Certificate) (time.Time, error) {
	x509cert, err := utils.TLSCertToX509(cert)
	if err != nil {
		return time.Time{}, trace.Wrap(err)
	}
	return x509cert.NotAfter, nil
}

// dbProxyTpl is the message that gets printed to a user when a database proxy is started.
var dbProxyTpl = template.Must(template.New("").Parse(`Started DB proxy on {{.address}}
{{if .randomPort}}To avoid port randomization, you can choose the listening port using the --port flag.
{{end}}
Use following credentials to connect to the {{.database}} proxy:
  ca_file={{.ca}}
  cert_file={{.cert}}
  key_file={{.key}}
`))

// dbProxyAuthTpl is the message that's printed for an authenticated db proxy.
var dbProxyAuthTpl = template.Must(template.New("").Parse(
	`Started authenticated tunnel for the {{.type}} database "{{.database}}" in cluster "{{.cluster}}" on {{.address}}.
{{if .randomPort}}To avoid port randomization, you can choose the listening port using the --port flag.
{{end}}
Use the following command to connect to the database or to the address above using other database GUI/CLI clients:
  $ {{.command}}
`))

// dbProxyAuthMultiTpl is the message that's printed for an authenticated db proxy if there are multiple command options.
var dbProxyAuthMultiTpl = template.Must(template.New("").Parse(
	`Started authenticated tunnel for the {{.type}} database "{{.database}}" in cluster "{{.cluster}}" on {{.address}}.
{{if .randomPort}}To avoid port randomization, you can choose the listening port using the --port flag.
{{end}}
Use one of the following commands to connect to the database or to the address above using other database GUI/CLI clients:
{{range $item := .commands}}
  * {{$item.Description}}:

  $ {{$item.Command}}
{{end}}
`))

const (
	envVarFormatText                 = "text"
	envVarFormatUnix                 = "unix"
	envVarFormatWindowsCommandPrompt = "command-prompt"
	envVarFormatWindowsPowershell    = "powershell"
)

var envVarFormats = []string{
	envVarFormatUnix,
	envVarFormatWindowsCommandPrompt,
	envVarFormatWindowsPowershell,
	envVarFormatText,
}

func envVarFormatFlagDescription() string {
	return fmt.Sprintf(
		"Optional format to print the commands for setting environment variables, one of: %s.",
		strings.Join(envVarFormats, ", "),
	)
}

func envVarDefaultFormat() string {
	if runtime.GOOS == constants.WindowsOS {
		return envVarFormatWindowsPowershell
	}
	return envVarFormatUnix
}

// envVarCommand returns the command to set environment variables based on the
// format.
//
// https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-envvars.html
func envVarCommand(format, key, value string) (string, error) {
	switch format {
	case envVarFormatUnix:
		return fmt.Sprintf("export %s=%s", key, value), nil

	case envVarFormatWindowsCommandPrompt:
		return fmt.Sprintf("set %s=%s", key, value), nil

	case envVarFormatWindowsPowershell:
		return fmt.Sprintf("$Env:%s=\"%s\"", key, value), nil

	case envVarFormatText:
		return fmt.Sprintf("%s=%s", key, value), nil

	default:
		return "", trace.BadParameter("unsupported format %q", format)
	}
}

var awsTemplateFuncs = template.FuncMap{
	"envVarCommand": envVarCommand,
}

// awsHTTPSProxyTemplate is the message that gets printed to a user when an
// HTTPS proxy is started.
var awsHTTPSProxyTemplate = template.Must(template.New("").Funcs(awsTemplateFuncs).Parse(
	`Started AWS proxy on {{.envVars.HTTPS_PROXY}}.
{{if .randomPort}}To avoid port randomization, you can choose the listening port using the --port flag.
{{end}}
Use the following credentials and HTTPS proxy setting to connect to the proxy:
  {{ envVarCommand .format "AWS_ACCESS_KEY_ID" .envVars.AWS_ACCESS_KEY_ID}}
  {{ envVarCommand .format "AWS_SECRET_ACCESS_KEY" .envVars.AWS_SECRET_ACCESS_KEY}}
  {{ envVarCommand .format "AWS_CA_BUNDLE" .envVars.AWS_CA_BUNDLE}}
  {{ envVarCommand .format "HTTPS_PROXY" .envVars.HTTPS_PROXY}}
`))

// awsEndpointURLProxyTemplate is the message that gets printed to a user when an
// AWS endpoint URL proxy is started.
var awsEndpointURLProxyTemplate = template.Must(template.New("").Funcs(awsTemplateFuncs).Parse(
	`Started AWS proxy which serves as an AWS endpoint URL at {{.endpointURL}}.
{{if .randomPort}}To avoid port randomization, you can choose the listening port using the --port flag.
{{end}}
In addition to the endpoint URL, use the following credentials to connect to the proxy:
  {{ envVarCommand .format "AWS_ACCESS_KEY_ID" .envVars.AWS_ACCESS_KEY_ID}}
  {{ envVarCommand .format "AWS_SECRET_ACCESS_KEY" .envVars.AWS_SECRET_ACCESS_KEY}}
  {{ envVarCommand .format "AWS_CA_BUNDLE" .envVars.AWS_CA_BUNDLE}}
`))
