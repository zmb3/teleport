---
authors: Andrew Burke (andrew.burke@goteleport.com)
state: draft
---

# RFD X - Automatic discovery of Azure servers

## What

Teleport SSH services will be able to automatically discover Azure virtual machine
instances. See [RFD 57](https://github.com/gravitational/teleport/blob/master/rfd/0057-automatic-aws-server-discovery.md)
(Automatic discovery and enrollment of AWS servers) for the same feature implemented
for AWS.

Note: unlike RFD 57, this RFD covers discovery only. Azure node joining will be added
later.

## Why

RFD 57 replaced the process of manually installing Teleport on EC2 servers, which
could be slow for large numbers of instances. This RFD replaces the process on Azure
as well.

## Discovery

Azure discovery will be handled by the new [discovery service](https://github.com/gravitational/teleport/blob/master/rfd/0057-automatic-aws-server-discovery.md#discovery) described in RFD 57.

```yaml
discovery_service:
  enabled: "yes"
  azure:
    - types: ["vm"]
      regions: ["westcentralus"]
      tags:
        "teleport": "yes"
```

The Teleport SSH service will need a [service principal](https://learn.microsoft.com/en-us/cli/azure/create-an-azure-service-principal-azure-cli?view=azure-cli-latest) with a role that includes the `Microsoft.Compute/virtualMachines/read`
permission to list virtual machines via the Go Azure SDK.

As with AWS database discover and EC2 discover, new Azure nodes will be discovered
periodically on a 60 second timer, as new nodes are found they will be added to the
teleport cluster.

In order to avoid attempting to reinstall Teleport on top of an instance where it is
already present, the generated Teleport config will match against the node name using
the Azure subscription ID and the virtual machine ID.

```json
{
  "kind": "node",
  "version": "v2",
  "metadata": {
    "name": "${AZURE_SUBSCRIPTION_ID}-${VM_ID}",
    "labels": {
      "env": "example",
      "teleport.dev/discovered-node": "yes"
    }
  },
  "spec": {
    "public_addr": "...",
    "hostname": "azurexyz"
  }
}
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
      regions: ["westcentralus"]
      tags:
        "teleport": "yes"
```

The discovery node's service principal should have permission to list virtual machines:
```json
{
  "Name": "teleport discover role",
  "Id": "88888888-8888-8888-8888-888888888888",
  "IsCustom": true,
  "Description": "Role for Teleport node discovery service",
  "Actions": [
    "Microsoft.Compute/virtualMachines/read",
  ],
  "NotActions": [],
  "DataActions": [],
  "NotDataActions": [],
  "AssignableScopes": [
    "/subscriptions/{subscriptionId1}",
  ]
}
```
