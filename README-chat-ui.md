# Chat UI Operator

Provides a HuggingFace‑style Chat UI (based on Open WebUI) as a Service for OpenAI‑compatible **Private LLM** backends.

## What it is / scope
- **Per‑tenant Chat UI**: Deploys one Open WebUI instance per `ChatUIInstance` CR.
- **Private LLM backend**: Reads connection info from a Secret containing `OPENAI_API_URL` and `OPENAI_API_KEY` produced by the Private LLM operator’s *“Include URL in Secret”* feature.
- **Ingress + URL wiring**: Exposes the UI via Ingress, using `<slug>.<PUBLIC_HOST>` and writes the public URL to `status.url`.
- **Demo‑first defaults**: UI auth is disabled (`WEBUI_AUTH=false`) and many advanced features are off; intended for demos and non‑sensitive playgrounds.

---

## `ChatUIInstance` CRD & examples

Minimal example:

```yaml
apiVersion: ui.privatellms.msp/v1alpha1
kind: ChatUIInstance
metadata:
  name: chatui-sample
  namespace: default
spec:
  credentialsSecretRef:
    name: llm-backend-sample
  replicas: 1
```

With optional image override:

```yaml
apiVersion: ui.privatellms.msp/v1alpha1
kind: ChatUIInstance
metadata:
  name: chatui-custom
  namespace: default
spec:
  credentialsSecretRef:
    name: llm-backend-sample
  replicas: 2
  image: ghcr.io/open-webui/open-webui:latest
```

Key fields:
- **`spec.credentialsSecretRef.name`**: Name of a Secret in the same namespace with the OpenAI‑compatible endpoint.
- **`spec.replicas`**: Optional; defaults to 1. Set to 0 to temporarily stop the UI without deleting the CR.
- **`spec.image`**: Optional override for the Open WebUI image; if omitted, the controller uses `ghcr.io/open-webui/open-webui:latest`.

The referenced Secret **must** have this shape:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: llm-backend-sample
  namespace: default
  labels:
    apeirora.eu/llm-api-compatibility: "openai"
type: Opaque
stringData:
  OPENAI_API_URL: "https://your-openai-compatible-endpoint/v1"
  OPENAI_API_KEY: "replace-me"
```

The operator mounts:
- `OPENAI_API_KEY` → `OPENAI_API_KEY`
- `OPENAI_API_URL` → `OPENAI_API_BASE_URL`

---

## Secret discovery, labels & selection behavior

- **Source of Secrets**: The Private LLM operator can emit Secrets for each LLM endpoint when *“Include URL in Secret”* is enabled (story [ST] #86).
- **Required label**: Such Secrets must carry  
  **`apeirora.eu/llm-api-compatibility=openai`**  
  so that the Showroom UI can discover them.
- **Expected schema**: The Secret must be `type: Opaque` with at least:
  - `stringData.OPENAI_API_URL` – full OpenAI‑compatible base URL (typically ending with `/v1`).
  - `stringData.OPENAI_API_KEY` – API key/token for the backend.
- **Selection behavior in Showroom**:
  - The `chat-ui-ui` chart ships a `pm-content.json` that defines a *Create* form for `ChatUIInstance`.
  - The *Credentials Secret* field is populated from a GraphQL query that lists Secrets in the current namespace filtered by `apeirora.eu/llm-api-compatibility=openai`.
  - When a user selects a Secret, only the **name** is written into `spec.credentialsSecretRef.name`; the label is used solely for discovery.

You can also create Secrets manually with the same label and keys if you are not using the Private LLM operator.

---

## Deploying via Helm (direct)

Install the operator into a cluster:

```sh
helm upgrade --install chat-ui-operator oci://ghcr.io/apeirora/charts/chat-ui-operator \
  --namespace chat-ui-system --create-namespace \
  --version <version> \
  --set image.repository=ghcr.io/apeirora/chat-ui-controller \
  --set image.tag=<version> \
  --set env.PUBLIC_HOST="chat-ui.example.internal" \
  --set env.PUBLIC_SCHEME=https \
  --set env.TLS_SECRET_NAME="chat-ui-tls" \
  --set env.INGRESS_EXTRA_ANNOTATIONS='{}'
```

Then create a `ChatUIInstance`:

```sh
kubectl apply -f config/samples/ui_v1alpha1_chatuiinstance.yaml
```

The operator will:
- Deploy an Open WebUI `Deployment` and `Service` per instance.
- Create a Traefik `Ingress` per instance with host `<slug>.<PUBLIC_HOST>`.
- Populate `status.url` once the URL is known.

---

## Deploying via OCM + Helm

This repository also defines an OCM component for Chat UI:

```yaml
name: ui.privatellms.msp/chat-ui
version: ${VERSION}
resources:
  - name: oci-helm-chart-chat-ui-operator
    type: helmChart
    version: "${VERSION}"
    access:
      type: ociArtifact
      imageReference: ghcr.io/${GITHUB_REPOSITORY_OWNER}/charts/chat-ui-operator:${CHART_TAG}
  - name: chat-ui-image
    type: ociImage
    version: "${VERSION}"
    access:
      type: ociArtifact
      imageReference: ghcr.io/${GITHUB_REPOSITORY_OWNER}/chat-ui-controller:${IMAGE_TAG}
```

In Platform Mesh / Showroom:
- The OCM component is published to `oci://ghcr.io/apeirora/ocm`.
- Cluster‑infra repos (for example `showroom-msp-cluster-infra/pm-cc-d2/msp01`) reference the `chat-ui-operator`, `chat-ui-pm-integration`, `chat-ui-sync-agent` and `chat-ui-ui` Helm charts from `oci://ghcr.io/apeirora/charts`.
- Flux `Kustomization`s then install:
  - The **operator** into `chat-ui-system`.
  - The **sync agent** into `api-syncagent`.
  - The **provider metadata** (APIExport, ContentConfiguration) into the KCP provider workspace.
  - The **portal content server** that serves `pm-content.json`.

If you are consuming this via OCM, use your standard Platform Mesh OCM flows to pick up `ui.privatellms.msp/chat-ui` at the desired version, which in turn pins both the controller image and the operator chart.

---

## Obtaining the UI URL & Showroom integration

- **From Kubernetes**:
  - `status.url` is computed as  
    `$(PUBLIC_SCHEME)://<slug>.$(PUBLIC_HOST)`  
    where `<slug>` is a stable, random identifier stored as the `ui.privatellms.msp/slug` annotation.
  - You can inspect it via:

    ```sh
    kubectl get chatuiinstances.ui.privatellms.msp \
      -A -o wide
    ```

    The CRD defines a `URL` print column backed by `.status.url`.

- **In the Showroom portal**:
  - The `chat-ui-ui` chart publishes a `pm-content.json` fragment that:
    - Registers a *Chat UI Instances* list view backed by the `ChatUIInstance` resource.
    - Shows `status.url` as a **URL** column in the list.
    - Provides a *Create* form that lets the user choose a labeled Secret and sets `spec.credentialsSecretRef.name`.
  - Users can click the URL in the list to open the Chat UI for a given instance.

- **Ingress host**:
  - The controller sets the Ingress host to `<slug>.<PUBLIC_HOST>`.
  - On Gardener MSP clusters, `PUBLIC_HOST` is typically something like  
    `chat-ui.msp01.pm-cc-d2.shoot.gardener.cc-one.showroom.apeirora.eu`,  
    so a full instance URL might look like:  
    `https://abcd1234.chat-ui.msp01.pm-cc-d2.shoot.gardener.cc-one.showroom.apeirora.eu`.

---

## Troubleshooting

- **Missing Secret or name not set**
  - Symptom: `status.phase=Error`, `Ready` condition `Reason=MissingSecret`.
  - Fix:
    - Ensure `spec.credentialsSecretRef.name` is non‑empty.
    - Ensure the Secret exists in the same namespace.
    - Check that it has both `OPENAI_API_URL` and `OPENAI_API_KEY` keys.

- **Missing label for discovery**
  - Symptom: Secret does not appear in the Showroom *Credentials Secret* dropdown.
  - Fix: Add `apeirora.eu/llm-api-compatibility=openai` to the Secret metadata.

- **Bad URL / API key**
  - Symptom: UI loads but chat requests fail (errors returned in the Open WebUI UI, or 4xx/5xx in logs).
  - Fix:
    - Verify `OPENAI_API_URL` matches the backend’s documented base URL (including `/v1` if required).
    - Verify `OPENAI_API_KEY` is valid and has access to the requested model.
    - Check the Open WebUI pod logs for errors when calling the backend.

- **Ingress / DNS / TLS issues**
  - Symptom: `status.url` is set, but the browser cannot reach the host or shows TLS errors.
  - Fix:
    - Confirm `env.PUBLIC_HOST` and `env.PUBLIC_SCHEME` are set correctly on the operator Deployment.
    - Check the generated `Ingress` for the instance:

      ```sh
      kubectl get ingress -n <ns> <name>-chatui -o yaml
      ```

    - Ensure DNS is configured for `<slug>.<PUBLIC_HOST>` and points to the Traefik ingress.
    - If using TLS, configure `env.TLS_SECRET_NAME` so the Ingress references a valid certificate.

- **CORS / mixed content**
  - API calls to the LLM backend are made server‑side by Open WebUI, so browser CORS is rarely the issue.
  - If embedding the UI in an iframe or mixing HTTP/HTTPS, ensure:
    - The Chat UI URL matches the scheme expected by the embedding page.
    - Any corporate reverse proxies or WAFs allow WebSocket/HTTP2 as needed.

---

## Security considerations

- **Auth disabled by default**
  - The controller sets `WEBUI_AUTH=false` and disables many Open WebUI features (user accounts, sharing, community features, etc.).
  - Anyone who can reach the Ingress URL can send prompts to the configured LLM backend.

- **Recommended scope**
  - Treat this as a **demo / showroom** component, not a production multi‑tenant UI.
  - Do not expose it to the public internet for sensitive or customer traffic without adding additional controls.

- **Restricting access**
  - Prefer to expose Chat UI only on internal networks (e.g. private DNS zones, VPN‑only access).
  - Use Kubernetes NetworkPolicies, ingress‑controller middlewares, or external auth (OIDC, SSO) in front of the Traefik entrypoint if you must expose it more broadly.
  - As a last resort, set `spec.replicas: 0` on all `ChatUIInstance` objects to shut down UIs while keeping CRs for later reuse.

---

## Releases & artifact locations

- **Container image**
  - Operator controller: `ghcr.io/apeirora/chat-ui-controller:<version>` (plus `:latest` for convenience).

- **Helm charts (OCI)**
  - Registry: `oci://ghcr.io/apeirora/charts`
  - Charts:
    - `chat-ui-operator`
    - `chat-ui-sync-agent`
    - `chat-ui-pm-integration`
    - `chat-ui-ui`

- **OCM component**
  - Repository: `oci://ghcr.io/apeirora/ocm`
  - Component name: `ui.privatellms.msp/chat-ui`
  - Resources: Helm chart (`oci-helm-chart-chat-ui-operator`) and controller image (`chat-ui-image`), both versioned with the same semver.

- **Versioning policy**
  - Semantic versions are managed by `release-please` and recorded in `CHANGELOG.md`.
  - The Go module, controller image, and all Helm charts share the same version number.
  - Tags of the form `vX.Y.Z` trigger:
    - Building and pushing `ghcr.io/apeirora/chat-ui-controller:X.Y.Z` (and `:latest`).
    - Packaging and pushing all four charts to `oci://ghcr.io/apeirora/charts` with `version: X.Y.Z`.
    - (Optionally) updating/publishing the OCM component when that pipeline is enabled.

