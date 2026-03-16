# Local Development

Set up a local development environment to build, test, and run the Chat UI Operator.

---

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.23+ | Build the operator |
| Docker | 20+ | Build container images |
| Kind | 0.20+ | Local Kubernetes cluster |
| kubectl | 1.28+ | Interact with the cluster |
| Helm | 3.12+ | Chart testing |
| Make | -- | Build automation |

## Clone the Repository

```bash
git clone https://github.com/apeirora/showroom-msp-chat-ui.git
cd showroom-msp-chat-ui
```

## Build

```bash
# Build the binary
make build

# Run linters
make lint

# Run unit tests
make test
```

The binary is output to `bin/manager`.

## Run Locally (Outside Cluster)

The fastest way to iterate is running the operator on your host machine, pointing at a local or remote cluster.

**1. Create a Kind cluster**

```bash
kind create cluster --name chat-ui-dev
```

**2. Install the CRD**

```bash
make install
```

**3. Run the operator**

```bash
# Defaults to localhost, HTTP
make run

# Or with custom settings
PUBLIC_HOST=chat.localhost PUBLIC_SCHEME=http go run ./cmd/main.go
```

**4. Apply sample resources**

```bash
kubectl apply -f config/samples/ui_v1alpha1_chatuiinstance.yaml
```

**5. Watch the reconciliation**

```bash
kubectl get chatuiinstances -w
kubectl get deployments,services,ingresses -l app.kubernetes.io/name=open-webui
```

> **Note**: When running locally, the readiness checks (HTTP probe, DNS resolution) will likely fail because there is no actual Ingress controller or DNS. The instance will stay in `Provisioning` phase. This is expected for local development -- the reconciliation logic still works.

## Run in Kind (Full Stack)

For a more realistic setup, build and load the image into Kind:

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
  --set env.PUBLIC_HOST=chat.localhost
```

## Generate CRDs and RBAC

After modifying API types:

```bash
# Regenerate CRDs, deepcopy, and RBAC
make manifests generate

# Sync CRDs into the Helm chart
make chart
```

## Run Tests

```bash
# Unit tests with envtest
make test

# Lint
make lint

# Helm chart linting
make helm-lint
```

## Build Docker Image

```bash
# Default (linux/current arch)
make docker-build IMG=ghcr.io/apeirora/chat-ui-controller:dev

# Multi-platform
make docker-buildx IMG=ghcr.io/apeirora/chat-ui-controller:dev
```

## Project Layout

```
cmd/main.go                      # Entrypoint, manager setup, OpenTelemetry init
api/v1alpha1/
  chatuiinstance_types.go        # CRD spec/status definitions
  groupversion_info.go           # API group registration
  zz_generated.deepcopy.go       # Generated deep copy functions
internal/controller/
  chatuiinstance_controller.go   # Reconciler (the core logic)
  chatuiinstance_controller_test.go  # Unit tests
config/
  crd/bases/                     # Generated CRD YAML
  samples/                       # Example CR + Secret
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
| `make helm-lint` | Lint Helm charts (`chat-ui-operator`, `chat-ui-pm-integration`, `chat-ui-sync-agent`) |
| `make helm-package` | Package Helm charts (`chat-ui-operator`, `chat-ui-pm-integration`, `chat-ui-sync-agent`) |

## Cleanup

```bash
# Remove from Kind
helm uninstall chat-ui-operator -n chat-ui
make uninstall  # Remove CRDs

# Delete the Kind cluster
kind delete cluster --name chat-ui-dev
```
