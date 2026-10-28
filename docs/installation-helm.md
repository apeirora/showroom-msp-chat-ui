# Helm Installation

Install the Chat UI Operator on any Kubernetes cluster using Helm.

---

## Prerequisites

- Kubernetes 1.28+
- Helm 3.12+
- A Traefik ingress controller (the operator creates `Ingress` resources with `ingressClassName: traefik`)
- An OpenAI-compatible LLM backend (or credentials to one)
- A verified HTTPS KCP endpoint that accepts the organization's OIDC ID tokens,
  exact source-instance mappings, registered OIDC clients and valid ingress TLS;
  see [instance access policy](access-policy.md).

## Add the Chart Registry

The charts are hosted as OCI artifacts on GitHub Container Registry:

```bash
# No "helm repo add" needed for OCI registries.
# Helm pulls directly from oci://ghcr.io/apeirora/charts
```

## Install the Operator

Prepare `hostname-policy-values.yaml` with `hostnamePolicy.cutover` and the reviewed
`hostnamePolicy.entries` described in [hostname migration](hostname-policy.md).
For a fresh install, use a fixed API-server timestamp before creating instances
and an empty entries list. Never generate a new cutoff on each upgrade. For an
existing install, complete the stopped-controller inventory and CRD upgrade first.
The manager refuses to start with an empty policy. `VERSION` below must be the
exact reviewed release containing this change; older releases do not enforce it.

Set `VERSION=0.14.4-rc.1` only for an explicit development prerelease upgrade after the
artifact checks and migration prerequisites in
[development-release.md](development-release.md). Keep production at its current
reviewed stable pin, such as `0.14.3`; that hold does not apply the security fixes.
Use the same exact `VERSION` for every chart in the installation.

Also prepare `access-policy-values.yaml` using the schema in
[access-policy.md](access-policy.md). Replace the endpoint and image digest below
with reviewed values. `proxyImage` is the controller/access-proxy image from the
same release, not the OAuth2 Proxy image.

```yaml
accessPolicy:
  kcpServer: https://kcp.example.test
  kcpCA: '' # Optional PEM CA; otherwise use system trust.
  credentialsNamespace: chat-ui-credentials
  proxyImage: ghcr.io/apeirora/chat-ui-controller@sha256:<reviewed-controller-image-digest>
  entries: [] # Add reviewed MSP/source identities and OIDC client references.
clientSecrets: [] # Optional delivery through existing scoped ESO SecretStores.
```

Provision the credential namespace separately from every instance namespace.
Have the platform owner register each client with exact owned-host callback URIs
and a KCP-accepted audience, then deliver its `client_secret` Secret there before
enabling its mapping. Use existing platform delivery or the documented `clientSecrets`
integration; never put secret values in these policy files. An empty `entries` list
allows startup but leaves every instance inaccessible until its mapping is reviewed.

```bash
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

### Operator Configuration

| Value | Default | Description |
|-------|---------|-------------|
| `image.repository` | `ghcr.io/apeirora/chat-ui-controller` | Controller image |
| `image.tag` | Packaged chart release version | Use the exact reviewed release |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `image.imagePullSecrets` | `[]` | Image pull secrets |
| `manager.replicas` | `1` | Controller replicas |
| `manager.resources` | `{}` | Resource requests/limits |
| `hostnamePolicy.cutover` | `""` | Required fixed API-server inventory timestamp; empty refuses startup |
| `hostnamePolicy.entries` | `[]` | Reviewed UID/namespace/name legacy mappings |
| `accessPolicy` | Empty required fields | KCP origin, credential namespace, pinned proxy image and reviewed mappings |
| `clientSecrets` | `[]` | Optional client-secret delivery through existing scoped SecretStores |
| `env.PUBLIC_HOST` | `chat.localhost` | Base domain for instance URLs |
| `env.PUBLIC_SCHEME` | `https` | Authenticated access requires HTTPS |
| `env.TLS_SECRET_NAME` | `""` | TLS Secret name for Ingress |
| `env.INGRESS_EXTRA_ANNOTATIONS` | `'{}'` | JSON map of extra Ingress annotations |
| `rbac.create` | `true` | Create RBAC resources |
| `serviceAccount.create` | `true` | Create ServiceAccount |
| `metrics.enabled` | `true` | Enable metrics endpoint |

> **Tip**: For Gardener-managed clusters, set `INGRESS_EXTRA_ANNOTATIONS` to include DNS annotations:
> ```yaml
> env:
>   INGRESS_EXTRA_ANNOTATIONS: |
>     {"dns.gardener.cloud/class":"garden","dns.gardener.cloud/dnsnames":"*.chat-ui.example.com"}
> ```

## Verify the Installation

```bash
# Check the operator pod
kubectl -n chat-ui get pods -l app.kubernetes.io/name=chat-ui-operator

# Check CRD registration
kubectl get crd chatuiinstances.ui.privatellms.msp
```

## Create Your First Instance

**1. Create a credentials Secret**

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-llm-backend
  namespace: chat-ui
  labels:
    apeirora.eu/llm-api-compatibility: "openai"
type: Opaque
stringData:
  OPENAI_API_URL: "https://api.openai.com/v1"
  OPENAI_API_KEY: "sk-your-key-here"
```

**2. Create a ChatUIInstance**

```yaml
apiVersion: ui.privatellms.msp/v1alpha1
kind: ChatUIInstance
metadata:
  name: demo-chat
  namespace: chat-ui
spec:
  credentialsSecretRef:
    name: my-llm-backend
  replicas: 1
```

**3. Complete its access mapping, then verify readiness**

Read the actual MSP and KCP source identities through trusted access, add the exact
UIDs to `accessPolicy.entries`, and apply the reviewed policy update. Register its
owned HTTPS callback URI and deliver the referenced client Secret. Unmapped instances
remain unavailable. A standalone MSP object without a KCP source mapping cannot
grant browser access.

```bash
kubectl -n chat-ui get chatuiinstances -w
# Use the Ready URL after policy, credential, DNS and TLS checks pass.
```

## Install All Charts (Full Platform Mesh Setup)

For a complete Platform Mesh integration, install the charts:

```bash
# 1. Operator (on MSP cluster)
helm upgrade --install chat-ui-operator \
  oci://ghcr.io/apeirora/charts/chat-ui-operator \
  --namespace chat-ui --create-namespace \
  --version "$VERSION" \
  -f "hostname-policy-values.yaml" \
  -f "access-policy-values.yaml" \
  --set env.PUBLIC_HOST="chat-ui.example.com" \
  --set env.PUBLIC_SCHEME=https \
  --set env.TLS_SECRET_NAME="chat-ui-tls"

# 2. PM Integration (applied to KCP provider workspace)
helm upgrade --install chat-ui-pm-integration \
  oci://ghcr.io/apeirora/charts/chat-ui-pm-integration \
  --version "$VERSION" \
  --set publicHost="chat-ui.example.com" \
  --set publicScheme=https

# 3. Sync Agent (on MSP cluster)
helm upgrade --install chat-ui-sync-agent \
  oci://ghcr.io/apeirora/charts/chat-ui-sync-agent \
  --namespace chat-ui \
  --version "$VERSION" \
  --set publishedResources.namespace=api-syncagent \
  --set syncAgentOperator.kcpKubeconfig=pm-kcp-kubeconfig

# 4. Portal UI (on MSP cluster)
helm upgrade --install chat-ui-ui \
  oci://ghcr.io/apeirora/charts/chat-ui-ui \
  --namespace chat-ui \
  --version "$VERSION"
```

> **Note**: The sync agent chart depends on `chat-ui-pm-integration` being installed first (the APIExport must exist before the agent can connect to it).

## Uninstall

```bash
# Remove all ChatUIInstances first (owned resources will be garbage-collected)
kubectl delete chatuiinstances -A --all

# Uninstall charts
helm uninstall chat-ui-ui -n chat-ui
helm uninstall chat-ui-sync-agent -n chat-ui
helm uninstall chat-ui-pm-integration
helm uninstall chat-ui-operator -n chat-ui

# Clean up CRD if needed
kubectl delete crd chatuiinstances.ui.privatellms.msp
```
