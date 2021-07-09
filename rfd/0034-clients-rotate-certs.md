---
authors: Brian Joerger (bjoerger@goteleport.com)
state: draft
---

# RFD 34 - Clients Certificate Rotation

## What

Provide a way for long living external clients and servers to retrieve new Teleport certificates as needed.

## Why

Presently, there is no good way to keep client/server certificates secure and valid for long durations without them being in direct contact with the Teleport Auth server, like a Teleport Node.

In order for a client or server outside of a Teleport Cluster to gain access for an extended period of time, they must retrieve a set of long lived certificates. There are a few security and UX issues with this approach.

- Long lived certificates are less secure than short lived certificates.
- The Teleport CAs may be rotated due to a security breach or a scheduled rotation. When this happens, all certificates will lose access, after a grace period, and need to be manually refreshed.
- Eventually, long lived certificates will expire after their TTL. Then they need to be manually refreshed, which puts a strain on administrators and could lead to downtime if neglected or forgotten.

It should be possible to set up an external client or server with auto-rotating certificates, similar to a Teleport Node.

## Details

Teleport Cert Bot will be a new Teleport service that will maintain cluster membership similarly to a Teleport Node.

Cert Bot will be responsible for rotating and renewing a set or sets of certificates as needed, thereby granting the users of such certificates pseudo-membership to the Teleport Cluster.

Cert Bot will also be responsible for providing a way for the Teleport cluster to monitor its certificates and providing a pathway to the auth server for revoking access of a set of certificate, likely utilizing a [Lock](https://github.com/gravitational/teleport/pull/7286).

### Use Cases

Cert Bot can do two things:

1. Provide user certificates and the Teleport Host CA certificate to a client.
 - The Teleport API client can make use of both x509 and SSH certificates to make API requests to the Teleport Auth server.
 - ??? A Teleport Application Access CLI can use x509 certificates.
 - ??? Automation tooling like Ansible can make use of SSH certificates.

2. Provide host certificates and the Teleport User CA certificate to a server.
 - ??? Application Access CLI can use x509 certificates.
 - OpenSSH servers can use SSH certificates to maintain membership to a Teleport Cluster with short lived certificates.

### UX

#### tbot binary

`tbot start` can be run from the command line in a similar fashion to `teleport start`. `tbot` will start up a Cert Bot, connect it to the Teleport Auth server as a new Node, and begin refreshing certs specified by the `--cert` flag.

```bash
# exactly the same syntax as `teleport start`
$ tbot start --token=[token] --auth-server=proxy.example.com
    --cert="tls,/var/lib/nginx,nginx -s reload" \
    --cert="openssh,/etc/ssh/,systemctl reload sshd"
```

`--cert` is a one-liner tuple (format, target, reload command) that must be provided at least once.
 - `format` (required) is a certificate format, such as `file`, `tls`, `openssh`, `kubernetes`, or `db` (matches `tctl auth sign` formats).
 - `target` (required) is a directory, file, or other location where certs are stored, depending on the `format`. `tbot` will read the certs from the `target` and rotate or refresh them using the data already stored in them, meaning the certs must be generated with `tctl auth sign` first.
 - `reload command` (optional) is a bash command to run after Cert Bot successfully rotates or renews a set of certificates.

#### User flow

1. Create certificates for tbot to manage
  ```bash
  $ tctl auth sign --format=openssh --host=ssh.example.com --out=/etc/ssh/ssh.example.com --ttl=1h
  The credentials have been written to ssh.example.com, ssh.example.com-cert.pub
  $ tctl auth sign --format=file --user=api-client --out=/etc/api-client-identity --ttl=1h
  The credentials have been written to api-client-identity
  ```

2. Create a `tbot` join token
  ```bash
  $ tctl tokens add --type=tbot
  The invite token: f68d2ccab54708afd06a00c4a044f323
  This token will expire in 60 minutes 
  ```

3. Start `tbot`
  ```bash
  $ tbot start --token=f68d2ccab54708afd06a00c4a044f323 --auth-server=proxy.example.com
    --cert="openssh,/etc/ssh/ssh.example.com,systemctl reload sshd"
    --cert="file,/etc/api-client-identity" # The underlying API client will automatically reload
  ```

4. Use the certificates in an external client/server.
  - The ssh server is already running and will automatically reload with `systemctl reload sshd` when the SSH or CA certificate is updated.
  - ```go
    // Create clt, it will automatically detect changes to "/etc/api-client-identity" and reload its connection
    clt, err := client.New(ctx, client.Config{
      Addrs: []string{"auth.example.com:3025"},
      Credentials: client.LoadIdentity("/etc/api-client-identity"),
    })
    ```

### Implementation

Add a new built in role "CertAgent" which can generate user certs with impersonation, has * impersonation.

### Monitoring

An open question, how will we track certificates intended to be used by a bot? You can do tctl nodes ls to get a list of all nodes and tctl users ls to get a list of all users. Will certificates issued to the Certificate Bot behave differently? Probably, for example, for host certificates we probably want to register that there is an active certificate but don't allow tsh ssh to connect to that host (because OpenSSH won't be heartbeating information about where its alive).

### Implementation Details


#### Cert Bot client

Teleport Cert Bot will need access to the following API endpoints in order to watch for rotation state changes and update certificates.

```go
type Client interface {
  // Generates TLS and SSH certificates for the user in the request
  GenerateUserCerts(ctx context.Context, req proto.UserCertsRequest) (*proto.Certs, error)
  // GenerateHostCerts will be converted to gRPC and updated to match GenerateUserCerts
  GenerateHostCerts(ctx context.Context, req proto.HostCertsRequest) (*proto.Certs, error)
  // Can be used to watch CA rotation state changes on types.CertAuthority
  NewWatcher(ctx context.Context, watch types.Watch) (types.Watcher, error)
}
```

### API Client 












## Why



## Details



### Cert Store

The agent will interact with certificates through its Cert Store.

A Cert Store can be used to:
 - authenticate a client
 - store renewed certificates
 - alert when the store's certificates are altered
 - provide its certificates expiration time

```go
type Store interface {
  // extends client.Credentials to authenticate a client
  client.Credentials
  // store signed client key
	Save(key Key) error
  // when current certs expire (with offset)
	Expires(offset int) time.Time
  // TTL defines how long new certs should live for. Note that it
  // will be limited on the server side by the user's max_session_ttl
  TTL() time.Duration
  // signal that these certificates have been changed
  Refresh() <- chan struct{}
  // Close associated resources, including Refresh channel
  Close() error
}

// Key describes a complete (signed) client key
// pulled from lib/client/interfaces
type Key struct {
  // Priv is a PEM encoded private key
  Priv []byte `json:"Priv,omitempty"`
  // Pub is a public key used to sign certs
  Pub []byte `json:"Pub,omitempty"`
  // Cert is an SSH client certificate
  Cert []byte `json:"Cert,omitempty"`
  // TLSCert is a PEM encoded client TLS x509 certificate.
  // It's used to authenticate to the Teleport APIs.
  TLSCert []byte `json:"TLSCert,omitempty"`
  // KubeTLSCerts are TLS certificates (PEM-encoded) for individual
  // kubernetes clusters. Map key is a kubernetes cluster name.
  KubeTLSCerts map[string][]byte `json:"KubeCerts,omitempty"`
  // DBTLSCerts are PEM-encoded TLS certificates for database access.
  // Map key is the database service name.
  DBTLSCerts map[string][]byte `json:"DBCerts,omitempty"`
  // AppTLSCerts are TLS certificates for application access.
  // Map key is the application name.
  AppTLSCerts map[string][]byte `json:"AppCerts,omitempty"`
  // TrustedCA is a list of trusted certificate authorities
  TrustedCA []auth.TrustedCerts
}
```

#### Formats

The `Store` interface can be used to support a wide variety of certificate storage formats. 

Initially, the following Cert Stores will be implemented:
 - `PathStore` (uses direct cert paths, successor of `KeyPairCredentials`)
 - `ProfileStore` (tsh profile)
 - `IdentityFileStore`

More stores can be added in the future to support:
 - db/app/kube certs in tsh profile
 - kubernetes secrets 

#### Refresh

The Cert Store will watch it's certificates for updates, whether it's a file change or something else. When this occurs, it will send a message on its `refresh` channel.

Refresh only uses a single channel, so the `Store` can only be used by a single Cert Bot. Additional Cert Bots will be prevented from using a used `Store` object.

### Cert Bot

The Agent will:
 - hold a Cert Store 
 - hold a Client that is authenticated by the Cert Store
 - watch for upcoming certificate expiration events (CA rotation or TTL expiration)
 - renew the Cert Store's certificates before expiration events

```go
type Agent struct {
  // client used to renew certificates and watch for expiration events
  Client Client
  // the agent's certificates store
  CertStore Store
}
```

Note that the agent's Cert Store will also be used to authenticate the agent's own client, meaning the agent will act on behalf of the certificates' user.

The Agent *could* be expanded to hold a list of other stores to maintain, but this would add some complications.
 - external certificates would be signed through impersonation. This could limit the use of the certificates, and would require the agent to have a variety of impersonation rules, which could be a major security hazard.
 - external certificates could be from other clusters, meaning they can't be signed through the Agent's client. 
 - The agent would be able to renew any certificates, regardless of that certificates' user's access controls (see RBAC section)

For these reasons, this idea will be saved for a future discussion.

#### Cert Client

The agent only needs access to a few client methods in order to perform its job.



#### Watch for certificate expiration

The agent will use its `Store`'s `Expires` method to see when the certificates need to be renewed. The agent will wait until the expiration is in `1/6*agent.TTL` before attempting to renew the certificates. If the renewal fails the first time, the agent will retry 9 more times in equal intervals. This is notably arbitrary and can be improved by using more advanced techniques, such as a backoff mechanism, a jittery ticker, etc.

##### Watch CA rotation state

The agent will use `client.NewWatcher` to watch for updates to the certificate authority's rotation state. The agent will keep track of the current rotation state, and if an event is received where the rotation state is changed to `update_clients`, the agent will refresh the store's certificates and CA certificates.

##### Retrieve new certificates

The agent will use `client.GenerateUserCerts` to retrieve newly signed certificates for its `Store`. The Store's `PublicKey` and username will be provided in order for the Auth server to sign new certificates. The certificate's expiration will also be derived from the agent's `TTL` field.

##### Refresh Client connection

The agent will watch its store's `Refresh` channel in order to refresh the client connection when needed. This may be caused by the Cert Bot writing to the `Store` itself, or by an external actor.

### Monitoring

It is possible that one, some, or all of the Cert Stores that the Cert Bot is managing have become invalid. For example, this could happen due to the certs being invalid before the Cert Bot is started up or because there was significant server downtime. Whatever the case, it should be simple for a user to find out that there is an issue and resolve it.

This can be done with a simple alerting mechanism via prometheus.

### RBAC restriction

Certificate Agent is intended for automating procedures, not for short-cutting authentication measures already in place. For example, `tsh login` should continue to be the standard authentication method for users logging into the system.

Therefore, new certificates will only be issued if the associated user has the new `reissue_certificates` role option enabled. This option will be false by default and should only be set to true for automation user roles.

Additionally, the CA rotation state can only be watched if the Certificate Agent is allowed perform `read` or `readnosecrets` actions on `cert_authority`.

```yaml
kind: role
version: v3
metadata: 
  name: cert-agent
spec:
  options:
    reissue_certificates: true
  allow:
    rules:
      - resources: ['cert_authority']
        verbs: ['readnosecrets']
```

### Integrations

In addition to being a standalone client, Certificate Agent can be integrated into the API Client, `tctl`, and `tsh` to meet a variety of automation use cases.

#### API Client

The API Client needs a new method to allow the agent to refresh its client's connection.

```go
func (c *Client) RefreshConnection(creds Credentials) error {
  // Attempt to connect the client using the updated credentials. 
  // If it fails, fallback to the former client connection and return the error.
}
```

Now, a new client can simply start up a Cert Bot to automatically keep its credentials refreshed. This can be added to the end of the client constructor.

```go
// connect client to server
store, ok := client.creds.(Store)
if ok && config.RunCertAgent {
  agent := Agent{
    Client: client,
    Store: store,
  }
  // agent will automatically refresh the store with updated certificates,
  // refresh the client connection, and close as soon as the client is closed.
  go agent.Run()
}
```

#### tsh and tctl

If desired, `tsh` and `tctl` could integrate Cert Bot functionality. This could be useful for Teleport users who orchestrate `tsh` and `tctl` to run automated processes.

`tctl auth sign --certbot` could be used to generate new certificates and automatically start up a new Certificate Agent Service using the certificates as a Store. All `--format` options would be supported. 

`tsh login` and `tsh [db|app|kube] login` could also support the `--certbot` flag for all available formats.

Note that the `reissue_certificates` role option would need to be enabled, so normal users won't be able to run a Cert Bot to refresh their `tsh login` credentials.
