---
authors: Andrew LeFevre (andrew.lefevre@goteleport.com)
state: draft
---

# RFD 98 - Registered OpenSSH Nodes

## Required approvers

* Engineering: @jakule && @r0mant
* Product: @klizhentas
* Security: @reedloden

## What

Allow OpenSSH nodes to be registered in a Teleport cluster.

## Why

[Agentless EC2 discovery mode](https://github.com/gravitational/teleport/issues/17865) will discover and configure OpenSSH nodes so they can authenticate with a cluster. But those OpenSSH nodes aren't registered as `node` resources in the backend. We need a way to register agentless OpenSSH nodes as `node` resources so they can be viewed and managed by users. RBAC and session recording should function correctly with registered OpenSSH nodes as well.

## Details

### Manually registering nodes

OpenSSH nodes can already be manually registered using `tctl create --force /path/to/node.yml`, though some changes should be made to make the process more straightforward for users. Agentless EC2 discovery mode will registered discovered OpenSSH nodes without needing user intervention, but if automatically registering an OpenSSH node fails a user may want to register a node manually. Currently nodes cannot be created with `tctl`, only upserted. This limitation should be removed so users can create nodes without having to pass `--force` to `tctl create`.

Furthermore, `tctl` should not require as many fields to be set when creating nodes. This is an example `node` resource that will work with `tctl create` today:

```yaml
kind: node
metadata:
  name: 5da56852-2adb-4540-a37c-80790203f6a9
spec:
  addr: 1.2.3.4:22
  hostname: agentless-node
version: v2
```

`tctl create` will auto-generate `metadata.name` if it is not already set so users don't have to generate GUIDs themselves.

### RBAC

When OpenSSH nodes are registered currently, RBAC checks for those nodes are not preformed. Even setting `auth_service.session_recording` to `proxy` in an Auth Server's config file does not help. RBAC logic will have to be updated so RBAC checks for registered OpenSSH nodes are preformed.

Currently RBAC checks are done on Teleport agent nodes, unless the Proxy is in `proxy` session recording mode, then the Proxy handles all RBAC checks. To avoid overloading the Proxy when many nodes are registered, Teleport agent nodes handle RBAC checks whenever possible. The Proxy should always handle RBAC checks for registered OpenSSH nodes, so the question is how will registered OpenSSH nodes be identified vs. Teleport agent nodes?

#### Detecting OpenSSH nodes

A new sub-kind to the `node` resource would be introduced. The name isn't important, but for now let's call this sub-kind `openssh`. When OpenSSH nodes are registered, this `openssh` sub-kind would have to be present in the resource. Then when a Proxy connects to a registered OpenSSH node it could lookup the sub-kind of the node, preforming the RBAC check appropriately. The absence of a `node` resource sub-kind would imply that the node is a Teleport agent node, making this change backwards compatible.

### Session recording

Currently session recording is required to be set be in `proxy` mode for it to work with OpenSSH nodes. That is not going to change, but ideally this requirement could be lifted when Teleport agent nodes and registered OpenSSH nodes are both in a single cluster. When establishing an SSH connection inside a cluster, one of the options listed above could be use to detect what kind of node is attempted to be connected to. Then depending on what the session recording mode is set to, the appropriate type of session recording would begin.

If session recording is in `node` or `node-sync` mode:

- If the node is a Teleport Agent node, the node would record the session and upload it as normal.
- If the node is a registered OpenSSH node, the Proxy would terminate and record the SSH session and upload it.

If session recording is in `proxy` or `proxy-sync` mode, behavior would be unaffected. The Proxy would terminate, record and upload the session. This mode will still be required if users of a cluster wish to connect to unregistered OpenSSH nodes.

I propose that `proxy` or `proxy-sync` session recording modes continue to be required when connecting to any OpenSSH node through Teleport, *at first*. When registering OpenSSH nodes and preforming RBAC checks on them is completed and possibly released, then work could be done to streamline session recording with registered OpenSSH nodes.

### Security

Both RBAC checks and session recording for registered OpenSSH nodes require that users connect through a Proxy. But if users are able to connect to registered OpenSSH nodes directly, that is a security issue. Teleport already tackles this issue through the use of certificates and CAs. Users are issued certificates by the cluster's User CA that are used for authentication with a Teleport cluster. OpenSSH nodes are configured to only accept certificates signed by Teleport's Host CA, which users do not have access to. In order for users to directly connect to an OpenSSH node, they would have to extract the cluster's Host CA and use it to create and sign a certificate to authenticate with.

### UX

The following Teleport node features won't work with registered OpenSSH nodes:

- Enhanced session recording and restricted networking
- Host user provisioning
- Session recording without SSH session termination
- Dynamic labels
- Outbound persistent tunnels to Proxies
