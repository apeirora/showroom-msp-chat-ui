# Chat UI Operator

Provides Open WebUI as a Service for OpenAI‑compatible Private LLM backends.

## What it is / scope
- Deploys Open WebUI per `ChatUIInstance` CR.
- Reads connection from a Secret containing `OPENAI_API_URL` and `OPENAI_API_KEY`.
- Exposes the UI via Ingress and writes the public URL to `.status.url`.
- No auth in UI by default (`WEBUI_AUTH=False`). Intended for demo environments.

## CRD
```yaml
apiVersion: ui.privatellms.msp/v1alpha1
kind: ChatUIInstance
metadata:
  name: chatui-sample
spec:
  credentialsSecretRef:
    name: llm-backend-sample
  replicas: 1
```

The referenced Secret must include:
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: llm-backend-sample
  labels:
    apeirora.eu/llm-api-compatibility: "openai"
type: Opaque
stringData:
  OPENAI_API_URL: "https://your-openai-compatible-endpoint/v1"
  OPENAI_API_KEY: "replace-me"
```

## Deploy via Helm
```sh
helm upgrade --install chat-ui charts/chat-ui-operator \
  --namespace chat-ui-system --create-namespace \
  --set image.repository=ghcr.io/apeirora/chat-ui-controller \
  --set image.tag=<tag> \
  --set env.PUBLIC_HOST=localhost \
  --set env.PUBLIC_SCHEME=http
```

Create a sample:
```sh
kubectl -n default apply -f config/samples/ui_v1alpha1_chatuiinstance.yaml
```

## Secret discovery and labeling (Task 1)
- Secrets compatible with OpenAI MUST be labeled: `apeirora.eu/llm-api-compatibility=openai`.
- The Showroom (Luigi) auto-discovers Secrets via this label and lets the user pick one; the operator only requires `spec.credentialsSecretRef.name`.

## Luigi config (Showroom integration)
Add a view to list labeled Secrets and create a `ChatUIInstance` for the selected one, showing the resulting `.status.url`.

```json
{
  "navigation": {
    "nodes": [
      {
        "pathSegment": "chat-ui",
        "label": "Chat UI",
        "viewUrl": "/microfrontends/chat-ui.html",
        "context": { "namespace": "default" }
      }
    ]
  }
}
```

In the microfrontend (no custom app required if you have an existing Luigi shell), implement:
- List: `GET /api/v1/namespaces/<ns>/secrets?labelSelector=apeirora.eu/llm-api-compatibility%3Dopenai`
- Create CR: `POST /apis/ui.privatellms.msp/v1alpha1/namespaces/<ns>/chatuiinstances`
- Watch CR until `.status.url` is populated; render as a link.

Example `ChatUIInstance` body:
```json
{
  "apiVersion": "ui.privatellms.msp/v1alpha1",
  "kind": "ChatUIInstance",
  "metadata": { "name": "chatui-<chosen-secret>" },
  "spec": { "credentialsSecretRef": { "name": "<chosen-secret>" } }
}
```

## Troubleshooting
- Missing Secret/label: ensure `apeirora.eu/llm-api-compatibility=openai` and required keys exist.
- Bad URL/API key: check the Secret values; Open WebUI expects OpenAI‑compatible endpoints.
- Ingress/CORS: verify `PUBLIC_HOST`, TLS secret, and Traefik class; extra annotations can be set via `env.INGRESS_EXTRA_ANNOTATIONS`.

## Security considerations
- UI auth is disabled by default (`WEBUI_AUTH=False`) for demo scope; restrict network access or enable auth in non‑demo setups.
- Use TLS via `env.TLS_SECRET_NAME` and set `env.PUBLIC_SCHEME=https` when exposing publicly.


