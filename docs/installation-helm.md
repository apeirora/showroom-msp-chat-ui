# Installation: Helm

Install the Chat UI Operator on any Kubernetes cluster using Helm.

---

## Prerequisites

- Kubernetes 1.28+
- Helm 3.12+
- A Traefik ingress controller (the operator creates `Ingress` resources with `ingressClassName: traefik`)
- An OpenAI-compatible LLM backend (or credentials to one)

## Add the Chart Registry

The charts are hosted as OCI artifacts on GitHub Container Registry:

```bash
# No "helm repo add" needed for OCI registries.
# Helm pulls directly from oci://ghcr.io/apeirora/charts
```

## Install the Operator

```bash
helm upgrade --install chat-ui-operator \
  oci://ghcr.io/apeirora/charts/chat-ui-operator \
  --namespace chat-ui --create-namespace \
  --version 0.8.0 \
  --set env.PUBLIC_HOST="chat-ui.example.com" \
  --set env.PUBLIC_SCHEME=https \
  --set env.TLS_SECRET_NAME="chat-ui-tls"
```

### Operator Configuration

| Value | Default | Description |
|-------|---------|-------------|
| `image.repository` | `ghcr.io/apeirora/chat-ui-controller` | Controller image |
| `image.tag` | `0.8.0` | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `image.imagePullSecrets` | `[]` | Image pull secrets |
| `manager.replicas` | `1` | Controller replicas |
| `manager.resources` | `{}` | Resource requests/limits |
| `env.PUBLIC_HOST` | `chat.localhost` | Base domain for instance URLs |
| `env.PUBLIC_SCHEME` | `http` | URL scheme (`http` or `https`) |
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

**3. Watch it come up**

```bash
kubectl -n chat-ui get chatuiinstances -w
# NAME        SECRET           PHASE          URL
# demo-chat   my-llm-backend   Provisioning   https://abc123.chat-ui.example.com
# demo-chat   my-llm-backend   Ready          https://abc123.chat-ui.example.com
```

## Install All Four Charts (Full Platform Mesh Setup)

For a complete Platform Mesh integration, install all four charts:

```bash
# 1. Operator (on MSP cluster)
helm upgrade --install chat-ui-operator \
  oci://ghcr.io/apeirora/charts/chat-ui-operator \
  --namespace chat-ui --create-namespace \
  --set env.PUBLIC_HOST="chat-ui.example.com" \
  --set env.PUBLIC_SCHEME=https

# 2. Sync Agent (on MSP cluster)
helm upgrade --install chat-ui-sync-agent \
  oci://ghcr.io/apeirora/charts/chat-ui-sync-agent \
  --namespace chat-ui \
  --set publishedResources.namespace=chat-ui \
  --set syncAgentOperator.kcpKubeconfig=pm-kubeconfig

# 3. PM Integration (applied to KCP provider workspace)
helm upgrade --install chat-ui-pm-integration \
  oci://ghcr.io/apeirora/charts/chat-ui-pm-integration \
  --set publicHost="chat-ui.example.com" \
  --set publicScheme=https

# 4. Portal UI (on MSP cluster)
helm upgrade --install chat-ui-ui \
  oci://ghcr.io/apeirora/charts/chat-ui-ui \
  --namespace chat-ui
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
