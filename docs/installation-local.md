# Local Development

Set up a local development environment to build, test, and run the Chat UI Operator.

---

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Docker | 20+ | Build container images, run Kind |
| Kind | 0.20+ | Local Kubernetes cluster |
| kubectl | 1.28+ | Interact with the cluster |
| Helm | 3.12+ | Chart installation |
| [kubectl-kcp](https://github.com/kcp-dev/kcp/releases) | latest | `kubectl ws` for KCP workspace management |
| Go | 1.23+ | Build from source (optional) |
| Make | -- | Build automation (optional) |

The Quick Start requires:
- [Platform Mesh local-setup](https://github.com/platform-mesh/helm-charts/tree/main/local-setup) running (`task local-setup`)
- [Private LLM operator installed locally](https://github.com/apeirora/showroom-msp-private-llm/blob/main/docs/installation-local.md) (steps 1–6)

See the [Private LLM local docs](https://github.com/apeirora/showroom-msp-private-llm/blob/main/docs/installation-local.md) for `kubectl-kcp` installation instructions and `/etc/hosts` setup.

> **Private registry:** The operator image is hosted on `ghcr.io/apeirora` (private). If you get `ImagePullBackOff`, either run `docker login ghcr.io` before creating the Kind cluster or create a pull secret:
>
> ```bash
> kubectl -n chat-ui-system create secret docker-registry ghcr-creds \
>   --docker-server=ghcr.io \
>   --docker-username="$GH_OWNER" \
>   --docker-password="$GITHUB_TOKEN"
> ```
>
> Then add `--set 'image.imagePullSecrets[0].name=ghcr-creds'` to the Helm install command.

## Key Concepts

If you're new to Platform Mesh, these resources explain the core concepts used in this guide:

- [**KCP Workspaces**](https://docs.kcp.io/kcp/main/concepts/workspaces/) — multi-tenant control plane that hosts provider and consumer workspaces
- [**APIExport & APIBinding**](https://docs.kcp.io/kcp/main/concepts/apis/) — how providers expose APIs and consumers bind to them
- [**API Sync Agent & PublishedResource**](https://docs.kcp.io/api-syncagent/) — how CRs created in KCP are synced to workload clusters
- [**Architecture overview**](./architecture.md) — how the operator, sync agent, and KCP fit together

## Quick Start

Follow the [Private LLM local installation](https://github.com/apeirora/showroom-msp-private-llm/blob/main/docs/installation-local.md) steps 1–6 first. Once the Private LLM operator, sync agent, and a test LLMInstance are running, continue here to add Chat UI.

### 1. Install the Chat UI operator

Make sure `KUBECONFIG` targets the Kind cluster (not KCP):

```bash
unset KUBECONFIG  # target the Kind cluster

helm install chat-ui-operator charts/chat-ui-operator \
  --namespace chat-ui-system --create-namespace \
  --set env.PUBLIC_HOST=localhost \
  --set env.PUBLIC_SCHEME=http
```

### 2. Create the Chat UI [provider workspace](https://docs.kcp.io/kcp/main/concepts/apis/) in KCP

```bash
export KUBECONFIG=.secret/kcp/admin.kubeconfig
export KCP_URL="https://kcp.api.portal.dev.local:8443"

kubectl ws create chat-ui --type=root:provider \
  --server="$KCP_URL/clusters/root:providers"
```

### 3. Install the PM integration chart in KCP

```bash
helm upgrade --install chat-ui-pm charts/chat-ui-pm-integration \
  --namespace default \
  --kubeconfig .secret/kcp/admin.kubeconfig \
  --kube-apiserver "$KCP_URL/clusters/root:providers:chat-ui" \
  --set publicHost=localhost \
  --set publicScheme=http
```

### 4. Install the Chat UI [sync agent](https://docs.kcp.io/api-syncagent/)

Switch back to the Kind kubeconfig:

```bash
unset KUBECONFIG  # target the Kind cluster

# Create KCP kubeconfig secret (skip if already created for Private LLM)
kubectl create namespace api-syncagent --dry-run=client -o yaml | kubectl apply -f -
kubectl -n api-syncagent create secret generic pm-kcp-kubeconfig \
  --from-file=kubeconfig=.secret/kcp/admin.kubeconfig \
  --dry-run=client -o yaml | kubectl apply -f -

# Install the sync agent
helm upgrade --install chat-ui-sync-agent charts/chat-ui-sync-agent \
  --namespace api-syncagent \
  --dependency-update \
  --set syncAgentOperator.enabled=true \
  --set syncAgentOperator.apiExportName=ui.privatellms.msp \
  --set syncAgentOperator.agentName=chat-ui-agent \
  --set syncAgentOperator.kcpKubeconfig=pm-kcp-kubeconfig \
  --set publishedResources.enabled=true \
  --set publishedResources.namespace=api-syncagent
```

Verify it's running:

```bash
kubectl -n api-syncagent logs deploy/chat-ui-agent --tail=10
```

### 5. Bind and create a ChatUIInstance via KCP

Assumes the `root:orgs:demo` workspace and LLM APIBinding already exist from the Private LLM setup.

```bash
export KUBECONFIG=.secret/kcp/admin.kubeconfig
export KCP_URL="https://kcp.api.portal.dev.local:8443"

# Bind to the Chat UI APIExport (see https://docs.kcp.io/kcp/main/concepts/apis/)
kubectl apply --server="$KCP_URL/clusters/root:orgs:demo" -f - <<'EOF'
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: chat-ui-binding
spec:
  reference:
    export:
      path: root:providers:chat-ui
      name: ui.privatellms.msp
EOF

# Switch to Kind to find the LLM service endpoint
unset KUBECONFIG
LLM_NS=$(kubectl get llminstances -A -o jsonpath='{.items[0].metadata.namespace}')
LLM_SVC="demo-llm-llama.$LLM_NS.svc.cluster.local"

# Switch back to KCP to create resources
export KUBECONFIG=.secret/kcp/admin.kubeconfig

# Create a credentials Secret pointing to the LLM backend
kubectl apply --server="$KCP_URL/clusters/root:orgs:demo" -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: demo-llm-creds
  labels:
    apeirora.eu/llm-api-compatibility: "openai"
type: Opaque
stringData:
  OPENAI_API_URL: "http://$LLM_SVC:8000/v1"
  OPENAI_API_KEY: "not-needed"
EOF

# Create a ChatUIInstance
kubectl apply --server="$KCP_URL/clusters/root:orgs:demo" -f - <<'EOF'
apiVersion: ui.privatellms.msp/v1alpha1
kind: ChatUIInstance
metadata:
  name: demo-chat
spec:
  credentialsSecretRef:
    name: demo-llm-creds
  replicas: 1
EOF

kubectl get chatuiinstances --server="$KCP_URL/clusters/root:orgs:demo" -w
```

> **Key detail:** The `OPENAI_API_URL` uses the in-cluster DNS name (`<service>.<namespace>.svc.cluster.local:<port>/v1`). Chat UI makes server-side calls to the LLM, so the URL does not need to be reachable from your browser.

### 6. Access the Chat UI

```bash
unset KUBECONFIG  # target the Kind cluster

CHAT_NS=$(kubectl get chatuiinstances -A -o jsonpath='{.items[0].metadata.namespace}')
kubectl port-forward -n "$CHAT_NS" svc/demo-chat-chatui 8080:8080
```

Open http://localhost:8080 in your browser. The TinyLlama model appears in the model selector.

The full flow: **KCP workspace** → sync agent → **Kind cluster** → operator reconciles → status synced back → **KCP / Portal UI**.

## Development Mode

For rapid iteration on controller code without the full Platform Mesh stack.

### 1. Create a Kind cluster

```bash
kind delete cluster --name chat-ui-dev || true
kind create cluster --name chat-ui-dev
```

### 2. Install the CRD

```bash
make install
```

### 3. Run the operator

```bash
make run

# Or with custom settings
PUBLIC_HOST=chat.localhost PUBLIC_SCHEME=http go run ./cmd/main.go
```

### 4. Apply sample resources

In a separate terminal:

```bash
kubectl apply -f config/samples/ui_v1alpha1_chatuiinstance.yaml
kubectl get chatuiinstances -w
```

> **Note**: When running locally, the readiness checks (HTTP probe, DNS resolution) will fail because there is no Ingress controller or DNS. The instance stays in `Provisioning` phase. This is expected — the reconciliation logic still works, and the Open WebUI pod is accessible via `kubectl port-forward`.

### Build and test from source

```bash
# Build the image
make docker-build IMG=chat-ui-controller:dev

# Load into Kind
kind load docker-image chat-ui-controller:dev --name chat-ui-dev

# Install via Helm
helm install chat-ui-operator charts/chat-ui-operator \
  --namespace chat-ui --create-namespace \
  --set image.repository=chat-ui-controller \
  --set image.tag=dev \
  --set image.pullPolicy=Never \
  --set env.PUBLIC_HOST=localhost
```

## Generate CRDs and RBAC

After modifying API types:

```bash
make manifests generate
make chart
```

## Run Tests

```bash
make test       # Unit tests with envtest
make lint       # golangci-lint
make helm-lint  # Helm chart linting
```

## Makefile Targets Reference

| Target | Description |
|--------|-------------|
| `make build` | Build the manager binary |
| `make run` | Run controller locally |
| `make test` | Run unit tests with envtest |
| `make lint` | Run golangci-lint |
| `make manifests` | Generate CRD and RBAC manifests |
| `make generate` | Generate deepcopy methods |
| `make chart` | Sync CRDs and RBAC into Helm chart |
| `make docker-build` | Build Docker image |
| `make docker-push` | Push Docker image |
| `make install` | Install CRDs into current cluster |
| `make deploy` | Deploy controller to current cluster |
| `make helm-lint` | Lint Helm charts |
| `make helm-package` | Package Helm charts |

## Cleanup

```bash
unset KUBECONFIG
helm uninstall chat-ui-operator -n chat-ui-system
helm uninstall chat-ui-sync-agent -n api-syncagent
make uninstall

kind delete cluster --name chat-ui-dev
```

## Troubleshooting

### ChatUIInstance stays in Provisioning

The readiness check requires DNS resolution of the Ingress hostname, which won't work without an Ingress controller. The Open WebUI pod is still running — use `kubectl port-forward` to access it.

### ImagePullBackOff for operator image

See the Prerequisites section for setting up `ghcr-creds`.

### KUBECONFIG confusion

The Quick Start switches between two kubeconfigs. See the [Private LLM troubleshooting](https://github.com/apeirora/showroom-msp-private-llm/blob/main/docs/installation-local.md#kubeconfig-confusion) section for details.

### Sync agent can't connect to KCP

Check the kubeconfig secret:

```bash
kubectl -n api-syncagent get secret pm-kcp-kubeconfig -o jsonpath='{.data.kubeconfig}' | base64 -d | head -5
```

Since both KCP and the sync agent run in the same Kind cluster, the KCP admin kubeconfig should work. Make sure `/etc/hosts` has the entry for `kcp.api.portal.dev.local`.

### kubectl ws: command not found

Install the [kubectl-kcp plugin](https://github.com/kcp-dev/kcp/releases). See the [Private LLM prerequisites](https://github.com/apeirora/showroom-msp-private-llm/blob/main/docs/installation-local.md#install-kubectl-kcp).
