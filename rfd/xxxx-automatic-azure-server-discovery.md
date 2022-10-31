---
authors: Andrew Burke (andrew.burke@goteleport.com)
state: draft
---

# RFD X - Automatic discovery of Azure servers

## Required Approvers
Engineering: @jakule && @r0mant
Product: @klizhentas && @xinding33
Security: @reedloden

## What

Teleport discovery services will be able to automatically discover and enroll Azure virtual machine
instances. See [RFD 57](https://github.com/gravitational/teleport/blob/master/rfd/0057-automatic-aws-server-discovery.md)
(Automatic discovery and enrollment of AWS servers) for the same feature implemented
for AWS.

## Why

RFD 57 replaced the process of manually installing Teleport on EC2 servers, which
could be slow for large numbers of instances. This RFD does the same for Azure
instances.

## Details

### Discovery

Azure discovery will be handled by the new [discovery service](https://github.com/gravitational/teleport/blob/master/rfd/0057-automatic-aws-server-discovery.md#discovery) described in RFD 57.

```yaml
discovery_service:
  enabled: "yes"
  azure:
    - types: ["vm"]
      subscriptions: ["<subscription id>"]
      resource_groups: ["<resource group"]
      regions: ["westcentralus"]
      tags:
        "teleport": "yes"
      install:
        join_params:
          nodename: ${SUBSCRIPTION_ID}_${VM_ID}  # default value
        script_name: "installer"  # default value
```

The Teleport discovery service will need a [service principal](https://learn.microsoft.com/en-us/cli/azure/create-an-azure-service-principal-azure-cli?view=azure-cli-latest) with a role that includes the `Microsoft.Compute/virtualMachines/read`
permission to list virtual machines via the Go Azure SDK.

As with AWS database discover and EC2 discover, new Azure nodes will be discovered
periodically on a 60 second timer, as new nodes are found they will be added to the
teleport cluster.

In order to avoid attempting to reinstall Teleport on top of an instance where it is
already present, the generated Teleport config will match against the node name using
the Azure subscription ID and the virtual machine ID by default. This can be overridden
by specifying a node name in the join params.

```json
{
  "kind": "node",
  "version": "v2",
  "metadata": {
    "name": "${SUBSCRIPTION}-${VM_ID}",
    "labels": {
      "env": "example",
      "teleport.dev/discovered-node": "yes",
      "teleport.dev/discovered-by": "${DISCOVER_NODE_UUID}",
      "teleport.dev/origin": "cloud",
      "teleport.dev/region": "westcentralus",
      "teleport.dev/subscriptionId": "88888888-8888-8888-8888-888888888888"
    }
  },
  "spec": {
    "public_addr": "...",
    "hostname": "azurexyz"
  }
}
```

### Agent installation

In order to install the Teleport agent on Azure virtual machines, Teleport will serve an
install script at `/webapi/scripts/{installer-resource-name}`. Installer scripts will
be editable as a resource.

example:
```yaml
kind: installer
metadata:
  name: "installer" # default value
spec:
  # shell script that will be downloaded and run by the virtual machine
  script: |
    #!/bin/sh
    curl https://.../teleport-pubkey.asc ...
    echo "deb [signed-by=... stable main" | tee ... > /dev/null
    apt-get update
    apt-get install teleport
    teleport node configure --auth-agent=...
  # Any resource in Teleport can automatically expire.
  expires: 0001-01-01T00:00:00Z
```

Unless overridden by a user, a default teleport installer command will be
generated that is appropriate for the current running version and operating
system initially supporting DEB and RPM based distros that Teleport already
provides packages for.

To run commands, the agent's service principal will require the `Microsoft.Compute/virtualMachines/runCommand/action` permission.

#### Action vs Managed run commands

Azure virtual machines can run scripts via either [Action Run Commands](https://learn.microsoft.com/en-us/azure/virtual-machines/linux/run-command) or [Managed Run Commands](https://learn.microsoft.com/en-us/azure/virtual-machines/linux/run-command-managed). Managed Run Commands are generally preferred for non-trivial installation. Unfortunately, Managed Run Commands are still in Preview, so Teleport will use Actoin Run Commands. We may consider switching to Managed Run Commands when they are fully released.

### teleport.yaml generation

The `teleport node configure` subcommand will be used to generate a
new /etc/teleport.yaml file:
```sh
teleport node configure
    --auth-server=auth-server.example.com [auth server that is being connected to]
    --token="$1" # passed via parameter from run-command
    --labels="teleport.dev/subscriptionId=${SUBSCRIPTION},teleport.dev/resource-group=${RESOURCE_GROUP},teleport.dev/region=${REGION}" # sourced from instance metadata
```

This will generate a file with the following contents:
```yaml
teleport:
  nodename: "${SUBSCRIPTION}-${VM_ID}"
  auth_servers:
    - "auth-server.example.com:3025"
  join_params:
    token_name: token
  # ...
ssh_service:
  enabled: "yes"
  labels:
    teleport.dev/subscriptionId: "${SUBSCRIPTION}"
    teleport.dev/resource-group: "${RESOURCE_GROUP}"
    teleport.dev/region: "${REGION}"
```

## UX

### User has 1 account to discover servers on

#### Teleport config

Discovery server:
```yaml
teleport:
  ...
auth_service:
  enabled: "yes"
discovery_service:
  enabled: "yes"
  azure:
    - types: ["vm"]
      subscriptions: ["<subscription id>"]
      resource_groups: ["<resource group"]
      regions: ["westcentralus"]
      tags:
        "teleport": "yes"
      install:
        # Use default values
```

The discovery node's service principal should have permission to list virtual machines and run commands:
```json
{
  "Name": "teleport discover role",
  "Id": "88888888-8888-8888-8888-888888888888",
  "IsCustom": true,
  "Description": "Role for Teleport node discovery service",
  "Actions": [
    "Microsoft.Compute/virtualMachines/read",
    "Microsoft.Compute/virtualMachines/runCommand/action"
  ],
  "NotActions": [],
  "DataActions": [],
  "NotDataActions": [],
  "AssignableScopes": [
    "/subscriptions/{subscriptionId1}",
  ]
}
```
