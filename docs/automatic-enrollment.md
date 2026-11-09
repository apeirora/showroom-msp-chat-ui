# Automatic instance enrollment

## WHAT

Automatic enrollment is optional and disabled by default. It registers exact
Chat UI callback URLs on existing Crossplane Keycloak Clients and publishes
source-to-runtime bindings for explicitly approved KCP workspaces. It does not
create organizations, clients, users, client secrets or API credentials. An
existing static access mapping takes precedence and does not depend on enrollment.

The `chat-ui-enrollment` chart runs a separate process on the platform cluster
that owns the Crossplane Clients. The Chat UI operator remains on the MSP cluster.
Installing the default chart creates no resources or permissions. Enabling it is
a separate deployment decision, after the prerequisites and development checks below.

## WHY

New portal-created instances need a verified KCP source UID, MSP runtime UID and
registered callback before their authenticated endpoint can start. Tenant-provided
labels, slugs, issuer fields and reported status cannot provide that binding.
The enroller reads both APIs, uses the KCP-assigned logical-cluster identity and
the pinned Showroom syncagent namespace formula, and applies the same hostname
policy as the operator. The formula is `chat-<cluster-namespace prefix, max 36
characters>-<first 20 hex characters of SHA3-256(cluster/namespace)>`.

A first binding requires a nonzero source creation time and an MSP creation time
that is not earlier. It also requires no prior `status.accessSecretUIDs` and no
existing `<instance-name>-chatui` Deployment or Ingress in the MSP namespace.
Nonempty or malformed prior access status denies automatic enrollment; an API
failure is not evidence that a workload or route is absent. These checks are
repeated before publication. Existing exact pins and reviewed static bindings
are exempt; an existing runtime requiring recovery still uses the reviewed
manual procedure.

These guards prevent automatic adoption of prior runtime/session state even
when source and MSP timestamps have the same second. They do not prove a
syncagent source-UID back-reference: a same-second empty, unused MSP object can
still bind. Clocks and the trusted sync path must be verified during acceptance.
Thereafter the durable source pin survives source
deletion while the same MSP UID exists. Recreating a source under the same name
cannot inherit that runtime. A reviewed client/issuer/credential-reference change
for the same source and runtime identities can update the binding.

Callback registration is additive. Existing callbacks must be exact HTTPS
`/oauth2/callback` URLs under the configured public host, and existing origins must
match those callbacks. Foreign URLs, wildcards and malformed values are rejected.
Retired callbacks remain registered, with a hard limit of 1,000 callbacks per
Client. Reaching that limit stops new registration before any writes. Pruning
requires a separate reviewed action. Retaining a callback does not grant instance use: the access proxy still
requires the user's permission to read the exact live KCP source UID.

The enroller publishes a new active binding only after an unauthenticated
`prompt=none` request returns the exact callback with `login_required` and the
matching state. This proves callback registration, not successful user login or
model inference. At most eight pending callbacks are probed per pass, with a
rotating offset; one pending callback does not block other verified entries.
Pins are retained for pending candidates. Complete API inventories and a second
identity check are required before publishing; API read failures are never
interpreted as an empty inventory.

## ACTION

### Prepare ownership and credentials

Keep the current working static mappings, client secrets and delivery path until
the new development workflow passes acceptance. Do not activate this with an
administrator's kubeconfig or point it at an unverified cluster.

| Identity | Required access | Not required |
|---|---|---|
| KCP reader | List ChatUIInstances directly under each approved `/clusters/<logical-cluster-ID>` | Source writes, Secret reads |
| MSP enrollment identity | Get ChatUIInstances, `deployments.apps` and `ingresses.networking.k8s.io` in approved runtime namespaces; get `kube-system` namespace; get/patch one named enrollment ConfigMap | Secret reads, creating ConfigMaps, workload changes |
| Platform Pod service account | Get/patch only the named Crossplane Clients; get `kube-system` namespace | Keycloak admin credentials, Secret reads |
| Existing MSP operator | Get the named enrollment ConfigMap in its credential namespace | Enrollment ConfigMap writes |

The chart supplies the platform service-account permissions and the operator
chart supplies its named ConfigMap reader Role. The KCP/MSP identities and their
credentials must be provisioned separately through the environment's established
credential lifecycle. Direct KCP workspace access must be tested: an APIExport
virtual-workspace kubeconfig is not assumed to work for this route. API origins
and both Kubernetes `kube-system` UIDs are pinned in configuration. Mounted
kubeconfigs are reloaded on each pass; TLS verification is mandatory, redirects
are refused and credential helpers are not executed.
The Keycloak callback probe currently uses system CA roots; a private-CA issuer
needs a separately reviewed trust configuration before activation.
Restrict Deployment and Ingress GET permissions to the expected
`<instance-name>-chatui` names where those names are known at provisioning time.

Each existing Crossplane Client must have its exact UID, provider configuration,
realm and client ID recorded. It must be confidential, enabled, use code flow and
PKCE S256, and have the unsupported alternate flows disabled. Hand off ownership
of `spec.forProvider.validRedirectUris` and `webOrigins` before enabling enrollment:

1. Record the existing exact callbacks and origins for review and rollback.
2. Prepare the change in the owning GitOps repository so it no longer reconciles
   those two fields. Keep its other Client security settings managed there.
3. Verify live `metadata.managedFields`: no manager other than
   `chat-ui-enrollment` may own either callback field. This includes Helm, Flux,
   Argo, kubectl and the provider; a guessed controller name is not exempt.
4. Add the explicit annotation
   `chatui.msp.apeirora.eu/enrollment-owned-callbacks: "true"` through the owner.
   The annotation alone does not bypass the field-ownership check.

Kubernetes RBAC can restrict patching to named Clients, but cannot restrict patches
to individual fields. The enroller code supplies that field restriction and tests
both UID and resourceVersion before each update. Review the narrow platform
service account accordingly.

### Configure the two processes

Use the same reviewed `accessPolicy`, `hostnamePolicy` and public host for both
processes. Keep the existing hostname cutover and legacy exceptions unchanged.
Add this opt-in block to the operator's existing access policy:

```yaml
accessPolicy:
  # Preserve the existing kcpServer, kcpCA, credentialsNamespace, proxyImage and entries.
  enrollment:
    configMapName: chat-ui-enrollment
    workspaces:
      - cluster: <approved-logical-cluster-ID>
        issuer: https://portal.example.test/keycloak/realms/example
        clientID: chat-ui
        clientSecretName: chat-ui-oidc-example
```

Only new instances in these approved workspaces enroll automatically. Adding a
new organization or workspace remains a platform configuration review. The
operator validates each selected entry against its current workspace, issuer,
client and credential allowlist; a stale policy in the producer cannot grant
access outside that list. Historical pins remain structurally valid when policy
changes, so unrelated approved workspaces continue to work. Coordinate changes
in both policy copies; differences can keep affected instances unavailable.

Before starting either consumer, create the platform-owned ConfigMap through a
reviewed bootstrap in `accessPolicy.credentialsNamespace` on the MSP cluster:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: chat-ui-enrollment
  namespace: <operator-credential-namespace>
  labels:
    app.kubernetes.io/managed-by: chat-ui-enrollment
data:
  enrollment.json: '{"version":1,"entries":[],"pins":[]}'
```

The enroller updates an existing ConfigMap; it never creates or adopts one from a
tenant-selected reference. Preserve the pins and protect this object from tenant
writes. Its mutable `enrollment.json` must have one writer. Do not keep a Helm or
Flux declaration that continually restores the empty document. The exact bootstrap
and ownership handoff belong in the environment deployment review.

Example enrollment chart values, with environment-specific placeholders:

```yaml
enabled: false # Change only for the reviewed activation.
image: registry.example.test/chat-ui-controller@sha256:<released-digest>
kcpKubeconfigSecret:
  name: chat-enrollment-kcp-reader
mspKubeconfigSecret:
  name: chat-enrollment-msp-writer
configuration:
  accessPolicy: {} # Exact reviewed operator accessPolicy, including enrollment.
  hostnamePolicy: {} # Exact reviewed operator hostnamePolicy.
  publicHost: chat.example.test
  mspServer: https://msp-api.example.test
  mspClusterUID: <verified-kube-system-UID>
  platformServer: https://<in-cluster-API-service-host>:<port>
  platformClusterUID: <verified-kube-system-UID>
  clients:
    - name: chat-ui-example # Cluster-scoped Crossplane resource name.
      uid: <verified-Crossplane-Client-UID>
      providerConfigName: environment-keycloak
      realm: example
      issuer: https://portal.example.test/keycloak/realms/example
      clientID: chat-ui
      baseRedirectURIs:
        - https://existing-chat.chat.example.test/oauth2/callback
```

The in-cluster API origin must match the Pod's in-cluster configuration. Values
contain references and public identities, never token or client-secret values.
The KCP/MSP Secrets each contain a `kubeconfig` key unless configured otherwise.
The enrollment chart requires an explicit digest-pinned released image; release
packaging does not silently select a digest for activation.

For local read-only planning, use the built binary without `--apply`:

```sh
chat-ui-enrollment --config=/protected/enrollment-config.json \
  --kcp-kubeconfig=/protected/kcp-reader.yaml \
  --msp-kubeconfig=/protected/msp-enrollment.yaml \
  --platform-kubeconfig=/protected/platform-enrollment.yaml
```

The default is one inventory/validation pass with no writes or callback probes.
`--apply --once` performs one mutating pass; `--apply` runs continuously. The chart
uses continuous mode only when explicitly enabled. Keep credentials outside Git
and do not paste them into reports. Output is bounded counts and status, not API
payloads or credential values.

### Development acceptance, then rollout

1. Run the local suites and chart renders. Build and release the artifact through
   the normal repository workflow. This does not deploy the feature.
2. Verify the scoped credentials, cluster identities, named ConfigMap and Client
   field ownership on the intended development platform. Run the read-only plan.
3. Activate a single enroller and the matching operator policy. Create a fresh
   instance through the portal, without a manual access mapping or callback edit.
   Confirm the exact source/runtime pin, registered callback, active mapping and
   guarded running workload.
4. Use ordinary authorized and unauthorized identities for actual login, model
   reply, peer-account denial and permission revocation. Verify source recreation
   cannot attach to surviving old state, including same-second timestamps before
   the first pin exists. Check that either an existing Deployment, an existing
   Ingress, nonempty/malformed access status or a failed absence check prevents
   first enrollment. Verify a pending callback does not prevent an unrelated
   instance from enrolling.
5. Exercise operator/enroller restart, delivered-client-secret rotation and mounted
   API-credential rotation. Confirm expired/revoked API credentials fail closed
   and recover only after valid replacement. Check policy removal and unavailable
   or malformed enrollment ConfigMaps as described below.
6. Promote to another environment only after recording these measured results and
   its separately reviewed ownership/credential configuration. Local fixtures,
   callback probes and healthy Pods are not end-to-end acceptance.

### Capacity and registration maintenance

The document is bounded to 1 MiB and 1,000 entries/pins; each workspace inventory
is bounded to 1,000 sources. These are rejection limits, not a tested throughput
claim. A failed or over-limit complete inventory prevents publication for that
pass; existing published mappings remain. Configure source quotas and measure
reconciliation time on the development pilot before expanding its workspace set.
At 5 API requests/second, a publishing pass can exhaust its deadline well below
the rejection limit (roughly 150 existing instances before other reads and probe
latency). This is an estimate, not measured capacity; keep the initial pilot small.
The CLI bounds each pass to 90 seconds and reports a fixed failure stage without
printing upstream response bodies. Retained pins and additive callbacks need
reviewed cleanup as instances are retired; never discard a pin while its runtime
UID survives. Preserve read access to retained pin namespaces until their runtimes
are retired, including after workspace approval is removed.

### Failure and rollback behavior

The operator uses an uncached named ConfigMap read. A transient read failure
withdraws its owned route and retries after 30 seconds, while preserving the
existing workload to avoid unnecessary restarts.
Route withdrawal prevents new ingress connections; it does not guarantee that
already-established streams terminate. They remain subject to the proxy's KCP
permission checks and ID-token expiry. A confirmed missing, deleted or malformed
map, or an absent/unapproved active
entry, withdraws the owned route and stops the owned workload. Static reviewed
mappings remain independent of enrollment availability. Per-request KCP source
UID authorization continues inside running access proxies.

Disabling the enroller stops future registration and mapping writes; it does not
revoke already published entries. For controlled rollback, stop its writer, retain
the protected document and pins, then restore reviewed static mappings or withdraw
selected active entries through the platform owner. Verify the operator's route
withdrawal and workload state. Keep source pins until their MSP UID is gone.
Do not remove pins just to force a recreated source to reuse old application state.
Client callback pruning is a separate reviewed cleanup after access withdrawal.
Never roll back to an unauthenticated endpoint.

Local chart checks: `HELM=<helm-binary> python3 hack/test-chat-ui-enrollment-chart.py`
(requires PyYAML), plus `helm lint charts/chat-ui-enrollment`.
