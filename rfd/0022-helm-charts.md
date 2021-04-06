---
authors: Gus Luxton (gus@goteleport.com)
state: draft
---

# RFD 22 - Helm chart improvements

## What

This RFD proposes improvements to Teleport's Helm charts.

## Why

The current [teleport](https://github.com/gravitational/teleport/tree/master/examples/chart/teleport) Helm chart is confusing to use:

- The experience with deploying the chart out of the box from the Helm chart repo is subpar, essentially requiring a full copy/paste of the values.yaml file from the Github repo to get a deployment working.
- The chart's documentation (both in its README.md and on https://goteleport.com/teleport/docs/) does not adequately explain how to use the chart to configure a fully working, production-ready Teleport cluster.
- More scalable use cases such as deploying into EKS using DynamoDB and deploying into GKE using Firestore are not well supported.

## Proposed changes

The existing [teleport](https://github.com/gravitational/teleport/tree/master/examples/chart/teleport) chart will be placed into "maintenance mode":

- The Helm chart will remain available in the Github repository, but its README will be edited to state that it will no longer be updated.
  - Instructions to use the newer, supported [teleport-cluster](https://github.com/gravitational/teleport/tree/master/examples/chart/teleport-cluster) and [teleport-kube-agent](https://github.com/gravitational/teleport/tree/master/examples/chart/teleport-kube-agent) charts will be added.
- The current version of this chart will remain available in the [Teleport Helm chart repo](https://charts.releases.teleport.dev/) but will have "deprecated" added to its description with a link to the guide in Teleport docs detailing how to migrate to a newer chart.

### Charts

Support will be added to [teleport-cluster](https://github.com/gravitational/teleport/tree/master/examples/chart/teleport-cluster) for the following use cases:

- Setting up a standalone Teleport cluster using the `teleport-cluster` chart with ACME certificates and a `PersistentVolumeClaim` for storage
- Setting up an HA Teleport cluster in EKS using DynamoDB and S3
- Setting up an HA Teleport cluster in GKE using Firestore and Google storage

An "escape hatch" allowing a user to mount their own Teleport config in YAML format will also be provided.

### Documentation

**Teleport docs**

Detailed guides will be added to https://goteleport.com/teleport/docs/ for the three supported scenarios:

- "How to set up a standalone Teleport cluster using the `teleport-cluster` chart with ACME certificates and a `PersistentVolumeClaim` for storage"
- "How to set up an HA Teleport cluster in EKS using DynamoDB and S3"
- "How to set up an HA Teleport cluster in GKE using Firestore and Google storage"

A guide will also be added detailing how to migrate from the old `teleport` chart to the new `teleport-cluster` chart. This guide will:

- support the deployment scenarios detailed above
- detail how to get the Teleport config in use on a current cluster and use the escape hatch to mount this config
- advise to contact Teleport support for further assistance if their use case is still not covered

This will enable us to collect feedback on usage of the new charts and give details on desired use cases which are not already covered.

**README**

Basic examples will be added to the README.

- How to install the chart
- Explanation of all supported variables in `values.yaml`
