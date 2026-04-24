# Local Installation

Install the Chat UI provider on a local [Platform Mesh](https://github.com/platform-mesh/helm-charts) setup with two `helm install` commands.

## Prerequisites

- [Platform Mesh local-setup](https://github.com/platform-mesh/helm-charts/tree/main/local-setup) running (`task local-setup`)
- [kubectl-kcp plugin](https://github.com/kcp-dev/kcp/releases) — provides `kubectl kcp workspace`
- [Helm](https://helm.sh/) 3.14+
- An OpenAI-compatible endpoint to point Chat UI at. The Platform Mesh tutorial installs [Private LLM](https://github.com/apeirora/showroom-msp-private-llm/blob/main/docs/installation-local.md) first; alternatively, supply your own endpoint via a Secret (see [Create a ChatUIInstance](#create-a-chatuiinstance-consumer-side)).

Export the admin kubeconfig path and KCP URL (only needed once per shell):

```sh
export HELM_CHARTS_DIR="$(pwd)"       # assumes CWD is the platform-mesh/helm-charts repo
export KCP="$HELM_CHARTS_DIR/.secret/kcp/admin.kubeconfig"
export KCP_URL="https://localhost:8443"
```

## Install

### 1. Create the provider workspace on KCP

```sh
kubectl kcp workspace create providers --type=root:providers --ignore-existing --kubeconfig=$KCP
kubectl kcp workspace use root:providers --kubeconfig=$KCP
kubectl kcp workspace create chat-ui --type=root:provider --ignore-existing --kubeconfig=$KCP
```

### 2. Pull chart dependencies

When installing from a source checkout, pull the nested sync-agent dependency
before the umbrella chart. Helm does not recursively update dependencies for
`file://` subcharts.

```sh
helm dependency update charts/chat-ui-sync-agent
helm dependency update charts/chat-ui-operator
helm dependency update charts/chat-ui-msp-app
helm dependency update charts/chat-ui-pm-app
```

### 3. Install the MSP-side umbrella (Kind cluster)

```sh
helm install chat-ui charts/chat-ui-msp-app \
  --namespace chat-ui --create-namespace \
  --set-file kcpKubeconfig.adminContent=$KCP
```

Creates the operator, sync agent, and a `pm-kcp-kubeconfig` Secret generated from the admin kubeconfig rewritten to target `root:providers:chat-ui` via KCP's in-cluster front-proxy.

### 4. Install the KCP-side umbrella (KCP workspace)

```sh
helm install chat-ui-pm charts/chat-ui-pm-app \
  --kubeconfig=$KCP \
  --kube-apiserver="$KCP_URL/clusters/root:providers:chat-ui" \
  --namespace chat-ui --create-namespace
```

Creates the `ui.privatellms.msp` APIExport, ProviderMetadata, and ContentConfiguration.

## Verify

```sh
kubectl -n chat-ui get pods
kubectl -n chat-ui rollout status deployment/chat-ui --timeout=3m
kubectl -n chat-ui logs deploy/chat-ui --tail=10

kubectl get apiexport ui.privatellms.msp --kubeconfig=$KCP \
  --server="$KCP_URL/clusters/root:providers:chat-ui" \
  -o jsonpath='{range .spec.resources[*]}{.name}{"\n"}{end}'
```

The APIExport resources must include `chatuiinstances`.

## Create a ChatUIInstance (consumer side)

Chat UI needs a backend endpoint. If you installed Private LLM first, point at its in-cluster service; otherwise provide your own OpenAI-compatible URL and key.

```sh
kubectl kcp workspace use root:orgs:demo --kubeconfig=$KCP   # or create it: see private-llm local install

# If Private LLM is installed, resolve its service DNS:
LLM_NS=$(kubectl get llminstances -A -o jsonpath='{.items[0].metadata.namespace}')
LLM_SVC="demo-llm-llama.$LLM_NS.svc.cluster.local"

kubectl apply --kubeconfig=$KCP --server="$KCP_URL/clusters/root:orgs:demo" -f - <<EOF
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: chat-ui-binding
spec:
  reference:
    export:
      path: root:providers:chat-ui
      name: ui.privatellms.msp
---
apiVersion: v1
kind: Secret
metadata:
  name: demo-llm-creds
  namespace: default
  labels:
    apeirora.eu/llm-api-compatibility: "openai"
type: Opaque
stringData:
  OPENAI_API_URL: "http://$LLM_SVC:8000/v1"
  OPENAI_API_KEY: "not-needed"
---
apiVersion: ui.privatellms.msp/v1alpha1
kind: ChatUIInstance
metadata:
  name: demo-chat
  namespace: default
spec:
  credentialsSecretRef:
    name: demo-llm-creds
  replicas: 1
EOF

kubectl get chatuiinstances --kubeconfig=$KCP \
  --server="$KCP_URL/clusters/root:orgs:demo" -w
```

> The `OPENAI_API_URL` uses the in-cluster DNS name. Chat UI calls the LLM server-side, so the URL does not need to be reachable from your browser.

## Access the Chat UI

```sh
CHAT_NS=$(kubectl get chatuiinstances -A -o jsonpath='{.items[0].metadata.namespace}')
kubectl port-forward -n "$CHAT_NS" svc/demo-chat-chatui 8080:8080
```

Open <http://localhost:8080>.

## Cleanup

```sh
helm uninstall chat-ui -n chat-ui
helm uninstall chat-ui-pm -n chat-ui \
  --kubeconfig=$KCP \
  --kube-apiserver="$KCP_URL/clusters/root:providers:chat-ui"
```

## Troubleshooting

### ChatUIInstance stays in `Provisioning`

The readiness check requires DNS resolution of the Ingress hostname, which won't work locally without an Ingress controller. The Open WebUI pod is still running — use `kubectl port-forward` to access it.

### Sync-agent can't reach KCP

See the equivalent troubleshooting section in the [Private LLM local install guide](https://github.com/apeirora/showroom-msp-private-llm/blob/main/docs/installation-local.md#sync-agent-cant-reach-kcp).

## Development Mode

For iterating on controller code, see [development.md](./development.md).
