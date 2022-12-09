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

[Agentless EC2 discovery mode](https://github.com/gravitational/teleport/issues/17865) will discover and configure OpenSSH nodes so they can authenticate with a cluster. But those OpenSSH nodes aren't registered as `node` resources in the backend. We need a way to register agentless OpenSSH nodes as `node` resources so they can be viewed and managed by users.

## Details

### Registering nodes

OpenSSH nodes can already be registered using `tctl create -f /path/to/node.yml`, though some changes should be made to make the process more straightforward for users. Currently nodes cannot be created with `tctl`, only upserted. This limitation should be removed so users can create nodes without having to pass `-f` to `tctl create`.

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

`metadata.name` should be auto-generated if it is not already set so user's don't have to generate GUIDs themselves.

### RBAC

When OpenSSH nodes are registered RBAC does not work as expected. Even when `auth_service.session_recording` is set to `proxy` in an Auth Server's config file, checking RBAC rules is not done correctly. RBAC logic will have to be updated to correctly handle OpenSSH nodes. Currently RBAC checks are done on Teleport agent nodes, unless the Proxy is in `proxy` session recording mode, then the Proxy handles all RBAC checks. To avoid overloading the Proxy when many nodes are registered, Teleport agent nodes should continue to handle RBAC checks whenever possible. The Proxy will still have to handle RBAC checks for registered OpenSSH nodes, so the question is how will registered OpenSSH nodes be identified vs. Teleport agent nodes?

#### Detecting OpenSSH nodes

##### Option 1 - add information to SSH host certs

When SSH host certificates are created for OpenSSH nodes, we set add an extension `x-teleport-role` that specifies what Teleport component the certificate is for. We could add another role type for OpenSSH nodes, so that when a Proxy connects to an OpenSSH node and reviews it's certificate, it could see from the `x-teleport-role` extension that the node is a registered OpenSSH node and preform the RBAC check appropriately.

This would require adding a new `api/types.SystemRole` constant which would be backwards compatible.

Pros:

- Potentially easier implementation, setting and checking an SSH certificate extension is very easy.
- Certificate fields couldn't be spoofed without access to a cluster's CAs, so the Proxy/Auth servers could be reasonably sure what type of node is what so RBAC checks wouldn't be skipped.

Cons:

- Old Teleport components may reject certificates with an unknown value for the `x-teleport-role` extension. `(api/types.SystemRoles).Check` will return with an error if it does not recognize the `api/type.SystemRole` constant.

##### Option 2 - add a `node` resource sub-kind

A new sub-kind to the `node` resource would be introduced. The name isn't important, but for now let's call this sub-kind `openssh`. When OpenSSH nodes are registered, this `openssh` sub-kind would have to be present in the resource. Then when a Proxy connects to a registered OpenSSH node it could lookup the sub-kind of the node, preforming the RBAC check appropriately. The absence of a `node` resource sub-kind would imply that the node is a Teleport agent node, making this change backwards compatible.

Pros:

- Node certificate generation doesn't need to be changed at all, so this change won't affect any files on remote nodes.

Cons:

- `lib/srv.Authhandlers` might need to be given access to the backend database in order to lookup the sub-kind of `node` resources so it can decide if the RBAC check can be done on the node or not.

##### Option choice: 1

I believe Option 1 is the better option, as it should be easier to implement and won't involve certificate validation code needing to access the backend database. Option 2 is certainly viable though, I honestly had a hard time deciding which option was the better one.

### Session recording

Currently session recording is required to be set be in `proxy` mode for it to work with OpenSSH nodes. That is not going to change, but ideally this requirement could be lifted when Teleport agent nodes and registered OpenSSH nodes are both in a single cluster. When establishing an SSH connection inside a cluster, one of the options listed above could be use to detect what kind of node is attempted to be connected to. Then depending on what the session recording mode is set to, the appropriate type of session recording would begin.

If session recording is in `node` or `node-sync` mode:

- If the node is a Teleport Agent node, the node would record the session and upload it as normal.
- If the node is a registered OpenSSH node, the Proxy would terminate and record the SSH session and upload it.

If session recording is in `proxy` or `proxy-sync` mode, behavior would be unaffected. The Proxy would terminate, record and upload the session. This mode will still be required if users of a cluster wish to connect to unregistered OpenSSH nodes.

I propose that `proxy` or `proxy-sync` session recording modes continue to be required when connecting to any OpenSSH node through Teleport, *at first*. When registering OpenSSH nodes and preforming RBAC checks on them is completed and possibly released, then work could be done to streamline session recording with registered OpenSSH nodes.
