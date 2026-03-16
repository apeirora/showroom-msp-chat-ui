# Installation: Remote Cluster

Deploy the Chat UI Operator to a remote Kubernetes cluster, either standalone or as part of the ApeiroRA Platform Mesh GitOps flow.

---

## Option A: Direct Helm Install

The simplest path for standalone deployment.

### Prerequisites

- A Kubernetes cluster (1.28+) with a Traefik ingress controller
- Helm 3.12+
- A wildcard DNS record pointing to your Traefik ingress (e.g., `*.chat-ui.example.com`)
- A TLS certificate (optional, for HTTPS)

### Install

```bash
helm upgrade --install chat-ui-operator \
  oci://ghcr.io/apeirora/charts/chat-ui-operator \
  --namespace chat-ui --create-namespace \
  --version 0.8.0 \
  --set image.repository=ghcr.io/apeirora/chat-ui-controller \
  --set image.tag=0.8.0 \
  --set env.PUBLIC_HOST="chat-ui.example.com" \
  --set env.PUBLIC_SCHEME=https \
  --set env.TLS_SECRET_NAME="chat-ui-wildcard-tls"
```

### Verify

```bash
kubectl -n chat-ui get pods
kubectl -n chat-ui logs deploy/chat-ui-operator-controller-manager
```

---

## Option B: GitOps with Flux (Platform Mesh)

In the ApeiroRA Platform Mesh, Chat UI is deployed via Flux on MSP clusters. The infrastructure repository (`showroom-msp-cluster-infra`) contains all the manifests.

### Directory Structure

```
showroom-msp-cluster-infra/apps/chat-ui/
├── base/
│   ├── kustomization.yaml
│   ├── namespace.yaml
│   ├── pm-kubeconfig-external-secret.yaml
│   ├── ghcr-showroom-external-secret.yaml
│   ├── operator-helm.yaml          # HelmRelease for the operator
│   ├── sync-agent-helm.yaml        # HelmRelease for the sync agent
│   ├── pm-integration-helm.yaml    # HelmRelease for KCP integration
│   ├── pm-integration-kustomization.yaml
│   └── ui-helm.yaml                # HelmRelease for portal content server
└── overlays/
    ├── dev/
    │   ├── kustomization.yaml
    │   ├── operator-values.yaml
    │   ├── sync-agent-values.yaml
    │   ├── pm-integration-values.yaml
    │   ├── ui-values.yaml
    │   └── wildcard-certificate.yaml
    └── prod/
        └── ...
```

### Key Flux Resources

**Operator HelmRelease** -- Deploys the controller manager:

```yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: chat-ui-operator
  namespace: chat-ui
spec:
  chart:
    spec:
      chart: chat-ui-operator
      version: ">=0.8.0 <1.0.0"
      sourceRef:
        kind: HelmRepository
        name: helm-showroom-repository
        namespace: flux-system
  valuesFrom:
    - kind: ConfigMap
      name: chat-ui-operator-values
```

**Sync Agent HelmRelease** -- Deploys the KCP sync agent (depends on pm-integration):

```yaml
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: sync-agent
  namespace: chat-ui
spec:
  dependsOn:
    - name: chat-ui-pm-integration
      namespace: chat-ui
  chart:
    spec:
      chart: chat-ui-sync-agent
      version: ">=0.8.0 <1.0.0"
```

### Environment-Specific Values

**Development** (`overlays/dev/operator-values.yaml`):

```yaml
image:
  repository: ghcr.io/apeirora/chat-ui-controller
  pullPolicy: Always
  imagePullSecrets:
    - name: ghcr-showroom-secret
env:
  PUBLIC_HOST: chat-ui.msp01.dev.showroom.apeirora.eu
  PUBLIC_SCHEME: https
  TLS_SECRET_NAME: chat-ui-tls
  INGRESS_EXTRA_ANNOTATIONS: |
    {"dns.gardener.cloud/class":"garden","dns.gardener.cloud/dnsnames":"*.chat-ui.msp01.dev.showroom.apeirora.eu"}
```

### Deployment Flow

```
1. Push changes to showroom-msp-cluster-infra
2. Flux on MCP cluster detects change
3. Flux Kustomization reconciles overlays/dev/
4. HelmRelease objects are applied to MSP cluster
5. Helm installs/upgrades charts in the chat-ui namespace
```

---

## DNS and TLS Configuration

### Gardener DNS

On Gardener-managed clusters, DNS is handled via annotations:

```yaml
env:
  INGRESS_EXTRA_ANNOTATIONS: |
    {
      "dns.gardener.cloud/class": "garden",
      "dns.gardener.cloud/dnsnames": "*.chat-ui.msp01.dev.showroom.apeirora.eu"
    }
```

The operator also sets `dns.gardener.cloud/dnsnames` on each Ingress to the specific instance hostname.

### TLS

Provide a wildcard TLS certificate as a Kubernetes Secret and reference it:

```yaml
env:
  TLS_SECRET_NAME: "chat-ui-wildcard-tls"
  PUBLIC_SCHEME: "https"
```

The operator will add a `spec.tls` entry to each Ingress referencing this Secret.

---

## Private Registry Access

If your cluster needs credentials to pull from `ghcr.io/apeirora`:

```yaml
image:
  imagePullSecrets:
    - name: ghcr-showroom-secret
```

Create the Secret:

```bash
kubectl -n chat-ui create secret docker-registry ghcr-showroom-secret \
  --docker-server=ghcr.io \
  --docker-username=<username> \
  --docker-password=<token>
```

---

## OpenTelemetry

To export traces to an OTLP collector, set the standard environment variables on the operator pod:

```yaml
env:
  OTEL_EXPORTER_OTLP_ENDPOINT: "http://otel-collector.monitoring:4318"
  OTEL_SERVICE_NAME: "chat-ui-operator"
```

Without these variables, traces are printed to stdout.
