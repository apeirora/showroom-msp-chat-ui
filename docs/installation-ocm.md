# OCM Installation

Install the Chat UI Operator using the Open Component Model for supply-chain-aware deployments.

> **Warning**: The OCM publish step is currently commented out in the CI release workflow. No OCM components are being published to the registry yet. The instructions below are provided for reference and will work once OCM publishing is re-enabled.

---

## Overview

The Chat UI Operator is published as an OCM component that bundles both the controller image and the Helm chart into a single, versioned, signed artifact.

**Component name**: `ui.privatellms.msp/chat-ui`
**OCM repository**: `oci://ghcr.io/apeirora/ocm`

## Component Structure

```yaml
name: ui.privatellms.msp/chat-ui
version: 0.8.0
resources:
  - name: oci-helm-chart-chat-ui-operator
    type: helmChart
    access:
      type: ociArtifact
      imageReference: ghcr.io/apeirora/charts/chat-ui-operator:0.8.0

  - name: chat-ui-image
    type: ociImage
    access:
      type: ociArtifact
      imageReference: ghcr.io/apeirora/chat-ui-controller:0.8.0
```

## Prerequisites

- [OCM CLI](https://ocm.software/) installed
- Access to the `ghcr.io/apeirora` registry
- A target Kubernetes cluster

## Inspect the Component

```bash
# List available versions
ocm get componentversions oci://ghcr.io/apeirora/ocm//ui.privatellms.msp/chat-ui

# Show component details
ocm get resources oci://ghcr.io/apeirora/ocm//ui.privatellms.msp/chat-ui:0.8.0
```

## Deploy via OCM Tooling

If you are using OCM's deployment tooling (e.g., with Flux OCM controllers or `ocm deploy`), reference the component in your deployment configuration:

```yaml
apiVersion: delivery.ocm.software/v1alpha1
kind: ComponentSubscription
metadata:
  name: chat-ui
  namespace: ocm-system
spec:
  component:
    name: ui.privatellms.msp/chat-ui
    registry:
      url: ghcr.io/apeirora/ocm
    version:
      semver: ">=0.8.0 <1.0.0"
```

## Platform Mesh Integration

In the ApeiroRA Platform Mesh, OCM components flow through the following path:

```
OCM Repository (ghcr.io/apeirora/ocm)
  └─► Flux on MCP cluster detects new version
       └─► HelmRelease references chart from OCI registry
            └─► Deploys to MSP cluster
```

The `showroom-msp-cluster-infra` repository already includes Flux `HelmRelease` objects that pin the chart version range:

```yaml
chart:
  spec:
    chart: chat-ui-operator
    version: ">=0.8.0 <1.0.0"
    sourceRef:
      kind: HelmRepository
      name: helm-showroom-repository
      namespace: flux-system
```

> **Note**: The OCM pipeline is currently commented out in the release workflow. Charts are pushed directly to the OCI registry. When OCM publishing is enabled, the component will also be available at the OCM repository path.

## Extract Resources Manually

You can also extract resources from the OCM component for manual deployment:

```bash
# Download the Helm chart from the component
ocm download resources oci://ghcr.io/apeirora/ocm//ui.privatellms.msp/chat-ui:0.8.0 \
  -r oci-helm-chart-chat-ui-operator \
  -O chat-ui-operator.tgz

# Install with Helm
helm install chat-ui-operator ./chat-ui-operator.tgz \
  --namespace chat-ui --create-namespace \
  --set env.PUBLIC_HOST="chat-ui.example.com"
```
