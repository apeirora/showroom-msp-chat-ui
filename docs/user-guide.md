# User Guide

This guide covers day-to-day usage of Chat UI -- from creating instances via the Showroom portal to troubleshooting common issues.

---

## Creating a Chat UI Instance

### Via the Showroom Portal

1. Navigate to your organization workspace in the Showroom portal
2. Find **Chat UI Instances** in the sidebar under the **Chat UI** category
3. Click **Create**
4. Fill in:
   - **Name** -- A unique name for this instance
   - **Replicas** -- Number of pods (1, 3, 6, or 12)
   - **Credentials Secret** -- Select from the dropdown (shows Secrets labeled `apeirora.eu/llm-api-compatibility=openai`)
5. Click **Create**
6. The instance will appear with phase `Provisioning`, then transition to `Ready` with a clickable URL

### Via kubectl

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-llm-creds
  labels:
    apeirora.eu/llm-api-compatibility: "openai"
type: Opaque
stringData:
  OPENAI_API_URL: "https://my-llm-backend/v1"
  OPENAI_API_KEY: "sk-..."
---
apiVersion: ui.privatellms.msp/v1alpha1
kind: ChatUIInstance
metadata:
  name: my-chat
spec:
  credentialsSecretRef:
    name: my-llm-creds
  replicas: 1
```

```bash
kubectl apply -f chatui.yaml
kubectl get chatuiinstances -o wide
```

## The Credentials Secret

The Chat UI Operator expects a Secret with two keys:

| Key | Description | Example |
|-----|-------------|---------|
| `OPENAI_API_URL` | OpenAI-compatible base URL | `https://api.openai.com/v1` |
| `OPENAI_API_KEY` | Authentication token | `sk-abc123...` |

**Where do these Secrets come from?**

- The **Private LLM operator** creates them automatically when "Include URL in Secret" is enabled
- You can also create them **manually** for any OpenAI-compatible backend (OpenAI, Azure OpenAI, vLLM, Ollama with OpenAI compat, etc.)

> **Tip**: Always add the label `apeirora.eu/llm-api-compatibility: "openai"` so the Secret appears in the portal dropdown.

## Instance Lifecycle

```
Created ──► Provisioning ──► Ready
                │
                ▼
              Error (if Secret is missing/invalid)
```

| Phase | Meaning |
|-------|---------|
| `Provisioning` | Deployment, Service, or Ingress is being set up. Readiness checks have not all passed yet. |
| `Ready` | All health checks passed. The URL is accessible. |
| `Error` | The referenced Secret is missing or invalid. |

## Accessing the Chat UI

Once an instance reaches `Ready`, the URL is available in:

- `status.url` field on the CR
- The **URL** column in the Showroom portal list view (clickable link)
- The Kubernetes print column: `kubectl get chatuiinstances -o wide`

The URL follows the pattern:

```
<PUBLIC_SCHEME>://<slug>.<PUBLIC_HOST>
```

For example: `https://abc123def456.chat-ui.msp01.dev.showroom.apeirora.eu`

The `<slug>` is a stable 12-character random string assigned on first reconciliation.

## Scaling

Change the replica count:

```bash
kubectl patch chatuiinstance my-chat --type merge \
  -p '{"spec":{"replicas":3}}'
```

Set to `0` to pause without deleting:

```bash
kubectl patch chatuiinstance my-chat --type merge \
  -p '{"spec":{"replicas":0}}'
```

## Changing the Backend

Update the Secret reference to point to a different LLM backend:

```bash
kubectl patch chatuiinstance my-chat --type merge \
  -p '{"spec":{"credentialsSecretRef":{"name":"new-backend-creds"}}}'
```

The operator will update the Deployment's environment variables and trigger a rolling restart.

If the Secret **contents** change (e.g., rotated API key), the operator detects the checksum change and automatically rolls the pods -- no manual intervention needed.

## Custom Image

Override the Open WebUI image:

```yaml
spec:
  image: ghcr.io/open-webui/open-webui:v0.5.0
```

If omitted, the default is `ghcr.io/open-webui/open-webui:latest`.

## Open WebUI Configuration

The operator deploys Open WebUI with **demo-first defaults**:

- **Auth disabled** (`WEBUI_AUTH=false`) -- anyone with the URL can chat
- **OpenAI API only** -- Ollama API disabled
- **Minimal features** -- No web search, code execution, image generation, community sharing, or user management
- **Default model** -- `x-ai/grok-code-fast-1` (configurable via the Open WebUI UI)

> **Warning**: These defaults are designed for showroom/demo environments. Do not expose instances to the public internet without adding authentication (e.g., OIDC via Traefik middleware, VPN, or NetworkPolicies).

## Troubleshooting

### Instance Stuck in Provisioning

Check the `Ready` condition for details:

```bash
kubectl get chatuiinstance my-chat -o jsonpath='{.status.conditions[?(@.type=="Ready")]}' | jq
```

Common reasons:

| Reason | Meaning | Fix |
|--------|---------|-----|
| `DeploymentProgressing` | Pods are still rolling out | Wait, or check pod events |
| `DeploymentNotReady` | Pods are not passing readiness probes | Check pod logs |
| `ServiceEndpointsMissing` | No ready endpoints on port 8080 | Check pod health |
| `HealthCheckFailed` | In-cluster HTTP probe returned non-2xx | Check Open WebUI logs |
| `DNSNotReady` | Hostname does not resolve yet | Wait for DNS propagation |

### Instance Shows Phase=Error

```bash
kubectl get chatuiinstance my-chat -o jsonpath='{.status.conditions}' | jq
```

| Reason | Fix |
|--------|-----|
| `MissingSecret` | Set `spec.credentialsSecretRef.name` to a valid Secret name |
| `SecretNotFound` | Create the Secret in the same namespace as the ChatUIInstance |

### Open WebUI Loads But Chat Fails

The LLM backend connection issue. Check:

1. The `OPENAI_API_URL` in the Secret ends with `/v1`
2. The `OPENAI_API_KEY` is valid
3. The LLM backend is reachable from the cluster

```bash
# Check the Open WebUI pod logs
kubectl logs deploy/my-chat-chatui -n <namespace> --tail=50
```

### Secret Not in Portal Dropdown

The Secret must have the label:

```yaml
labels:
  apeirora.eu/llm-api-compatibility: "openai"
```

The portal UI uses a GraphQL query filtered by this label.

### Ingress / DNS Issues

Check the generated Ingress:

```bash
kubectl get ingress <name>-chatui -n <namespace> -o yaml
```

Verify:
- `spec.ingressClassName` is `traefik`
- `spec.rules[0].host` matches the expected hostname
- `annotations` include the correct DNS annotations for your environment
- TLS is configured if using HTTPS

### Operator Logs

```bash
kubectl -n chat-ui logs deploy/chat-ui-operator-controller-manager --tail=100
```

Look for:
- `"created Chat UI Deployment"` -- instance is being provisioned
- `"reconciled ChatUIInstance"` -- successful reconciliation
- `"credentials secret not found"` -- missing Secret
- `"secret checksum changed, triggering rollout"` -- Secret rotation detected

## Releases and Versioning

Releases follow [semantic versioning](https://semver.org/) managed by [release-please](https://github.com/googleapis/release-please).

| Artifact | Location |
|----------|----------|
| Controller image | `ghcr.io/apeirora/chat-ui-controller:<version>` |
| Helm charts | `oci://ghcr.io/apeirora/charts/<chart-name>:<version>` |
| OCM component | `oci://ghcr.io/apeirora/ocm` (component `ui.privatellms.msp/chat-ui`) |
| Changelog | [CHANGELOG.md](../CHANGELOG.md) |

All artifacts (image + 4 charts) share the same version number.
