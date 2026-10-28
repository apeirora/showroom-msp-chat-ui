# OCM Installation

Deploy the Chat UI Operator using the [Open Component Model](https://ocm.software/) (OCM) supply chain. The OCM controller resolves the component version from an OCI registry, extracts the Helm chart via a `Resource`, and hands it to a `FluxDeployer` that creates a Flux `HelmRelease`.

---

## Prerequisites

- Kubernetes cluster with:
  - [OCM Controller](https://github.com/open-component-model/open-component-model) installed
  - [Flux](https://fluxcd.io/) controllers (source-controller, helm-controller)
- `kubectl`, `helm` 3.12+, and `envsubst` for manual bootstrap
- `GITHUB_TOKEN` with read access to GHCR packages
- The [access-policy prerequisites](access-policy.md): verified HTTPS KCP/issuer
  endpoints, trusted ingress TLS, reviewed source-instance mappings, registered
  OIDC clients and delivery of their `client_secret` Secrets.

> **Note**: KRO is **not** required. The chart uses the OCM controller's built-in `FluxDeployer` to create the `HelmRelease` directly.

## Architecture

```mermaid
graph LR
    CV[ComponentVersion] --> R[Resource<br>Helm chart ref]
    R --> S[Snapshot]
    S --> FD[FluxDeployer]
    FD --> HR[Flux HelmRelease]
    HR --> Op[Chat UI Operator<br>in target namespace]
```

The Helm chart creates three OCM custom resources:

| Resource | Kind | Purpose |
|----------|------|---------|
| `chat-ui` | `ComponentVersion` | Points to the OCM component in the OCI registry |
| `chat-ui-chart` | `Resource` | Extracts the Helm chart artifact from the component |
| `chat-ui` | `FluxDeployer` | Creates a Flux `HelmRelease` from the extracted chart |

All three resources are created in the `ocm-system` namespace.

## Step 1: Create GHCR Credentials

The OCM controller needs pull access to `ghcr.io/apeirora`:

```sh
kubectl -n ocm-system create secret docker-registry ghcr-credentials \
  --docker-server=ghcr.io \
  --docker-username="apeirora" \
  --docker-password="$GITHUB_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -
```

If the operator image is also private, create the same secret in the target namespace:

```sh
kubectl create namespace chat-ui || true
kubectl -n chat-ui create secret docker-registry ghcr-credentials \
  --docker-server=ghcr.io \
  --docker-username="apeirora" \
  --docker-password="$GITHUB_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -
```

## Step 2: Configure Values

Create a `values.yaml` override file:

The wrapper's component pin follows the selected release. The example below
opts into `0.14.4-rc.1` only for development after the artifact checks and migration
prerequisites in [development-release.md](development-release.md). Do not apply
this RC to production. Existing installations should retain their current exact
stable pin, such as `0.14.3`, until their reviewed migration is ready; that hold
does not apply the security fixes.

Complete the [hostname migration](hostname-policy.md), inventory and schema
upgrade before activating the selected release and its policy. Fresh installs
also need a fixed API-server cutoff before creating instances; they may use an
empty entries list.
Also prepare `operator.accessPolicy` below. Use the controller/access-proxy image
digest from that exact reviewed release, not the OAuth2 Proxy image. Provision
its credential namespace separately from every instance namespace. The platform
owner must register clients with exact owned-host callback URIs and a KCP-accepted
audience, then deliver the referenced `client_secret` Secrets through existing
platform delivery or scoped ESO SecretStores. No secret values belong in this file.
Empty access entries permit manager startup but leave all instances inaccessible;
add reviewed MSP and KCP source UID mappings before browser acceptance.

Arrange TLS certificate delivery through the platform's existing certificate
management before enabling instances. Each managed Ingress lives in its
ChatUIInstance's **runtime namespace**, so the configured TLS Secret name must
exist in every such namespace. A Secret only in the operator's `chat-ui` namespace
does not satisfy this requirement. Verify the delivered Secret has type
`kubernetes.io/tls`, a current trusted certificate covering the owned hostnames,
and managed renewal. Keep private keys out of values files and terminal output.
This is separate from OIDC client-secret delivery in the credential namespace.

```yaml
component:
  semver: "0.14.4-rc.1" # Development opt-in after the artifact and migration checks above.

operator:
  hostnamePolicy:
    cutover: '' # Required fixed timestamp; an empty value refuses manager startup.
    entries: [] # Reviewed legacy mappings for an existing installation.
  accessPolicy:
    kcpServer: https://kcp.example.test # Actual verified HTTPS origin, without a path.
    kcpCA: '' # Optional PEM CA for KCP; otherwise system trust.
    credentialsNamespace: chat-ui-credentials
    proxyImage: ghcr.io/apeirora/chat-ui-controller@sha256:<reviewed-controller-image-digest>
    entries: [] # Reviewed MSP/source identities, issuer and client-secret references.
  clientSecrets: [] # Optional delivery through existing scoped ESO SecretStores.
  targetNamespace: chat-ui
  publicHost: chat-ui.example.com
  publicScheme: https
  tlsSecretName: "chat-ui-tls"
  imagePullSecretName: ghcr-credentials
  imageRepository: ghcr.io/apeirora/chat-ui-controller
  imageTag: "" # Use the selected component's chart default.
  ingressExtraAnnotations: {}
```

### All Values

These are the source-checkout defaults. Release packaging sets the installer's
`operator.imageRepository` and `operator.imageTag` to the published image and
version; any overrides must match the selected component release.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `component.name` | Name for the OCM resources | `chat-ui` |
| `component.namespace` | Namespace for OCM resources | `ocm-system` |
| `component.componentName` | OCM component identity | `ui.privatellms.msp/chat-ui` |
| `component.repositoryUrl` | OCI registry URL | `ghcr.io/apeirora/ocm` |
| `component.secretRefName` | Secret with registry credentials | `ghcr-credentials` |
| `component.semver` | Exact reviewed component version | Published release version, set by Release Please |
| `component.interval` | Reconciliation interval | `1m` |
| `operator.targetNamespace` | Namespace where the operator is deployed | `chat-ui` |
| `operator.publicHost` | Public hostname for Chat UI | `chat.localhost` |
| `operator.publicScheme` | `http` or `https` | `https` |
| `operator.tlsSecretName` | Required TLS Secret name delivered in every instance/runtime namespace | `""` |
| `operator.imagePullSecretName` | Image pull secret name | `ghcr-credentials` |
| `operator.imageRepository` | Operator image repository | `ghcr.io/apeirora/chat-ui-controller` |
| `operator.imageTag` | Operator image tag | `""` in source (uses chart default); release version in published packages |
| `operator.ingressExtraAnnotations` | Extra ingress annotations | `{}` |
| `operator.hostnamePolicy` | Required fixed cutoff and legacy mappings | Empty cutoff; refuses startup |
| `operator.accessPolicy` | Required KCP origin, credential namespace, pinned proxy image and mappings | Empty required fields; refuses startup |
| `operator.clientSecrets` | Optional delivery through existing scoped SecretStores | `[]` |

## Step 3: Install via Helm

```sh
helm template chat-ui charts/chat-ui-operator-ocm/ \
  -f charts/chat-ui-operator-ocm/values.yaml \
  -f ./values.yaml \
  | kubectl apply -f -
```

This creates three resources in `ocm-system`:

1. **ComponentVersion** `chat-ui` -- resolves the component from `ghcr.io/apeirora/ocm`
2. **Resource** `chat-ui-chart` -- extracts the `oci-helm-chart-chat-ui-operator` artifact
3. **FluxDeployer** `chat-ui` -- creates a `HelmRelease` targeting `chat-ui`

The OCM controller reconciles the chain: `ComponentVersion` -> `Resource` -> `Snapshot` -> `FluxDeployer` -> `HelmRelease` -> operator pods.

## Step 4: Verify

```sh
# 1. ComponentVersion should be Ready
kubectl get componentversion -n ocm-system

# 2. Resource should be Ready with a Snapshot
kubectl get resource -n ocm-system
kubectl get snapshot -n ocm-system

# 3. FluxDeployer should have created a HelmRelease
kubectl get fluxdeployer -n ocm-system
kubectl get helmrelease -n ocm-system

# 4. Operator pods should be running
kubectl get pods -n chat-ui
```

> **Tip**: If the `ComponentVersion` stays in a non-ready state, check that the `ghcr-credentials` secret exists in `ocm-system` and has valid credentials.

## Upgrading

Update `component.semver` in your values file and re-apply:

```sh
# Set component.semver to the exact reviewed version after completing the migration.
helm template chat-ui charts/chat-ui-operator-ocm/ \
  -f charts/chat-ui-operator-ocm/values.yaml \
  -f ./values.yaml \
  | kubectl apply -f -
```

The OCM controller detects the version change, resolves the new chart, and the `FluxDeployer` rolls out the upgrade via the `HelmRelease`.

## Manual Deployment via bootstrap.yaml

Before using `ocm/bootstrap.yaml`, complete the hostname/access inventory and
the runtime-namespace TLS delivery described above. Set `CHAT_UI_TLS_SECRET_NAME`
to the delivered Secret name. The bootstrap references it; it does not issue or
copy certificates. An empty value is not supported by this installation path.

Render locally with the required-input checks and the explicit substitution list:

```sh
(
  set -eu
  export GH_OWNER=apeirora
  # Set VERSION to the exact reviewed release and use the fixed inventory timestamp.
  : "${VERSION:?Set an exact reviewed release}"
  : "${HOSTNAME_POLICY_CUTOVER:?Set the reviewed fixed cutoff}"
  : "${CHAT_UI_KCP_SERVER:?Set the verified HTTPS KCP origin without a path}"
  : "${CHAT_UI_CONTROLLER_IMAGE_DIGEST:?Set the released controller image reference including @sha256 digest}"
  : "${CHAT_UI_TLS_SECRET_NAME:?Set the TLS Secret name delivered in every runtime namespace}"
  export VERSION HOSTNAME_POLICY_CUTOVER CHAT_UI_KCP_SERVER CHAT_UI_CONTROLLER_IMAGE_DIGEST CHAT_UI_TLS_SECRET_NAME

  envsubst '${GH_OWNER} ${VERSION} ${HOSTNAME_POLICY_CUTOVER} ${CHAT_UI_KCP_SERVER} ${CHAT_UI_CONTROLLER_IMAGE_DIGEST} ${CHAT_UI_TLS_SECRET_NAME}' \
    < "ocm/bootstrap.yaml" > "bootstrap-rendered.yaml"
)
```

After a successful render, review `bootstrap-rendered.yaml`. Edit the
`FluxDeployer`'s `.spec.helmReleaseTemplate.values.hostnamePolicy.entries`
to include every reviewed legacy mapping, and customize the operator settings.
Also fill `values.accessPolicy.entries` with reviewed identities and client-secret
references and arrange credential delivery. The bootstrap uses `chat-ui` as its
credential namespace; it must be separate from every instance namespace. Change
that operator-owned setting before applying if the target uses a different scope.
An empty hostname entries list is only sufficient for a fresh install with no legacy
objects; empty access entries never grant browser access.

Apply the reviewed file to the intended cluster only after those prerequisites
are verified:

```sh
kubectl apply -f "bootstrap-rendered.yaml"
```

This creates the same three resources (`ComponentVersion`, `Resource`, `FluxDeployer`).
Local rendering does not verify certificate delivery or the served HTTPS endpoint.

## Component Structure

The OCM component bundles the Helm chart and the controller image. This schematic
uses the same exact reviewed `$VERSION` selected for installation:

```yaml
name: ui.privatellms.msp/chat-ui
version: "${VERSION}"
resources:
  - name: oci-helm-chart-chat-ui-operator
    type: helmChart
    access:
      type: ociArtifact
      imageReference: ghcr.io/apeirora/charts/chat-ui-operator:${VERSION}

  - name: chat-ui-image
    type: ociImage
    access:
      type: ociArtifact
      imageReference: ghcr.io/apeirora/chat-ui-controller:${VERSION}
```

### Inspect the Component

```sh
# List available versions
ocm get componentversions oci://ghcr.io/apeirora/ocm//ui.privatellms.msp/chat-ui

# Show the exact reviewed component selected for installation.
: "${VERSION:?Set an exact reviewed release}"
ocm get resources "oci://ghcr.io/apeirora/ocm//ui.privatellms.msp/chat-ui:${VERSION}"
```

## Deploying Without OCM

If you prefer to skip OCM entirely and install the chart directly:

```sh
helm upgrade --install chat-ui-operator \
  oci://ghcr.io/apeirora/charts/chat-ui-operator \
  --namespace chat-ui --create-namespace \
  --version "$VERSION" \
  -f "hostname-policy-values.yaml" \
  -f "access-policy-values.yaml" \
  --set env.PUBLIC_HOST="chat-ui.example.com" \
  --set env.PUBLIC_SCHEME=https \
  --set env.TLS_SECRET_NAME="chat-ui-tls"
```

See [Helm Installation](installation-helm.md) for full details.

## Next Steps

- [Create your first ChatUIInstance](user-guide.md)
