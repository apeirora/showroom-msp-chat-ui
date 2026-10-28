# Local Installation

Install the Chat UI provider on a local [Platform Mesh](https://github.com/platform-mesh/helm-charts) setup with two `helm install` commands.

## Prerequisites

- [Platform Mesh local-setup](https://github.com/platform-mesh/helm-charts/tree/main/local-setup) running (`task local-setup`)
- [kubectl-kcp plugin](https://github.com/kcp-dev/kcp/releases) — provides `kubectl kcp workspace`
- [Helm](https://helm.sh/) 3.14+
- An OpenAI-compatible endpoint to point Chat UI at. The Platform Mesh tutorial installs [Private LLM](https://github.com/apeirora/showroom-msp-private-llm/blob/main/docs/installation-local.md) first; alternatively, supply your own endpoint via a Secret (see [Create a ChatUIInstance](#create-a-chatuiinstance-consumer-side)).
- Verified HTTPS access from the application pods to KCP and the organization
  issuer, an ingress hostname with trusted TLS, and an OIDC client whose audience
  KCP accepts. Local HTTP port-forwarding does not satisfy the access policy.

Export the admin kubeconfig path (only needed once per shell):

```sh
export HELM_CHARTS_DIR="$(pwd)"       # assumes CWD is the platform-mesh/helm-charts repo
export KCP="$HELM_CHARTS_DIR/.secret/kcp/admin.kubeconfig"
```

## Install

### 1. Create the provider workspace on KCP

```sh
kubectl kcp workspace create providers --type=root:providers --ignore-existing
kubectl kcp workspace use root:providers
kubectl kcp workspace create chat-ui --type=root:provider --ignore-existing
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

Create `chat-ui-policy-values.yaml` with both policies nested under the operator:

```yaml
chat-ui-operator:
  hostnamePolicy:
    cutover: '' # Replace with a fixed API-server timestamp before creating instances.
    entries: []
  accessPolicy:
    kcpServer: https://kcp.example.test # Actual verified HTTPS origin, with no path.
    kcpCA: '' # Optional PEM CA for KCP; otherwise use system trust.
    credentialsNamespace: chat-ui-credentials
    proxyImage: ghcr.io/apeirora/chat-ui-controller@sha256:<reviewed-controller-image-digest>
    entries: [] # Add exact reviewed MSP/source identities and OIDC references.
  clientSecrets: [] # Optional delivery through existing scoped ESO SecretStores.
  env:
    PUBLIC_HOST: chat-ui.example.test # Reachable owned suffix with trusted TLS.
    PUBLIC_SCHEME: https
    TLS_SECRET_NAME: chat-ui-tls
```

An empty cutoff refuses manager startup. Reuse the same cutoff on upgrades; if
instances already exist, follow the [hostname migration](hostname-policy.md)
to inventory mappings, stop the old controller and apply the CRD first.
Replace the KCP origin, host and controller image digest with the reviewed target
values. Prepare the credential namespace separately from all instance namespaces,
then have the platform owner register OIDC clients with exact owned-host callback
URIs and deliver their `client_secret` Secrets there. Existing platform delivery
or the scoped ESO `clientSecrets` integration can supply them; no secret values
belong in this file. See [access-policy.md](access-policy.md) for the mapping and
delivery schema. Empty access entries permit startup but grant no instance access.

```sh
helm install chat-ui charts/chat-ui-msp-app \
  --namespace chat-ui --create-namespace \
  -f "chat-ui-policy-values.yaml" \
  --set-file "kcpKubeconfig.adminContent=$KCP"
```

Creates the operator, sync agent, and a `pm-kcp-kubeconfig` Secret generated from the admin kubeconfig rewritten to target `root:providers:chat-ui` via KCP's in-cluster front-proxy.

### 4. Install the KCP-side umbrella (KCP workspace)

```sh
helm install chat-ui-pm charts/chat-ui-pm-app \
  --namespace chat-ui --create-namespace
```

Creates the `ui.privatellms.msp` APIExport, ProviderMetadata, and ContentConfiguration.

## Verify

```sh
kubectl -n chat-ui get pods
kubectl -n chat-ui rollout status deployment/chat-ui --timeout=3m
kubectl -n chat-ui logs deploy/chat-ui --tail=10

kubectl kcp workspace use root:providers:chat-ui
kubectl get apiexport ui.privatellms.msp \
  -o jsonpath='{range .spec.resources[*]}{.name}{"\n"}{end}'
```

The APIExport resources must include `chatuiinstances`.

## Create a ChatUIInstance (consumer side)

Chat UI reads the endpoint and API key from a Secret in its KCP namespace.
Use a Private LLM API token Secret. Do not derive runtime DNS from the KCP namespace.

```sh
kubectl kcp workspace use root:orgs:demo   # or create it: see private-llm local install

# Create the token request in the same KCP namespace as the LLMInstance.
kubectl apply -f - <<'TOKEN'
apiVersion: llm.privatellms.msp/v1alpha1
kind: APITokenRequest
metadata:
  name: demo-chat-token
  namespace: default
spec:
  instanceName: demo-llm
TOKEN
kubectl wait -n default --for=condition=Ready apitokenrequest/demo-chat-token --timeout=5m
TOKEN_SECRET=$(kubectl get apitokenrequest demo-chat-token -n default -o jsonpath='{.status.secretName}')

kubectl apply -f - <<EOF
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
apiVersion: ui.privatellms.msp/v1alpha1
kind: ChatUIInstance
metadata:
  name: demo-chat
  namespace: default
spec:
  credentialsSecretRef:
    name: "$TOKEN_SECRET"
  replicas: 1
EOF

kubectl get chatuiinstances -w
```

> The token Secret carries the endpoint published by the LLM operator.
> The sync agent creates a separate credential replica in the Chat UI runtime namespace.

## Access the Chat UI

After the source and MSP objects exist, read both identities through trusted access
and add their exact UIDs, namespaces and names to the reviewed access policy. Do not
infer authorization mappings from tenant annotations. Apply the policy update,
register the selected `https://<owned-host>/oauth2/callback` URI and verify the client
Secret is delivered. Confirm the Ingress hostname resolves and presents trusted TLS.

Open the instance's published HTTPS URL once `Ready=True`, then sign in with an
organization identity allowed to `get` that exact source ChatUIInstance. The
guarded service does not provide an unauthenticated HTTP port-forward fallback.

## Cleanup

```sh
kubectl kcp workspace use root:providers:chat-ui
helm uninstall chat-ui -n chat-ui
helm uninstall chat-ui-pm -n chat-ui
```

## Troubleshooting

### ChatUIInstance stays in `Provisioning`

Check the instance conditions, reviewed hostname/access mappings, delivered client
Secret and backend Secret first. Missing access configuration withdraws the owned
Ingress and stops the workload. Ready also requires a working Ingress hostname and
trusted TLS; configure these locally before browser acceptance.

### Sync-agent can't reach KCP

See the equivalent troubleshooting section in the [Private LLM local install guide](https://github.com/apeirora/showroom-msp-private-llm/blob/main/docs/installation-local.md#sync-agent-cant-reach-kcp).

## Development Mode

For iterating on controller code, see [development.md](./development.md).

Prepare separate JSON files containing the `hostnamePolicy` and `accessPolicy`
objects themselves (without their YAML wrapper keys), then run:

```sh
make run \
  HOSTNAME_POLICY_FILE="/path/to/reviewed/hostname-policy.json" \
  ACCESS_POLICY_FILE="/path/to/reviewed/access-policy.json"
```

Both paths are required. The same policy validation and credential-delivery
requirements apply when the controller runs on the host.
