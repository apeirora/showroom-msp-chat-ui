# Instance access policy

## WHAT

Chat UI use requires an authenticated organization login and the user's existing
KCP `get` permission on the **exact source ChatUIInstance**. Authorized account and
organization administrators use that same permission; there is no separate chat
role. Application users receive Open WebUI role `user`, including the first user.

Each managed pod contains the pinned OAuth2 Proxy on port 8080, the operator's Go
access proxy on **127.0.0.1:8082**, and the pinned Open WebUI on
**127.0.0.1:8081**. Service and Ingress expose only 8080. Port 8083 serves only a
fixed readiness probe. The mounted Python wrapper binds native WebUI tokens and
Socket.IO sessions to the identity supplied by the access proxy.

## WHY

OIDC login alone does not grant permission to a particular Chat UI. The proxy
sends the verified ID token to KCP for each request, over verified TLS, and accepts
only HTTP 200 for the configured source UID, logical cluster, namespace and name.
It checks long-running streams again every 30 seconds (plus a 5-second KCP timeout)
and terminates connections at ID-token expiry. It does not cache allows, follow
KCP redirects or use environment HTTP proxies. When an explicit KCP CA is supplied,
that CA replaces the system trust pool for KCP calls.

The application identity is `sha256(issuer + NUL + subject)@chatui.invalid`.
Caller-supplied identity/forwarding headers and OAuth cookies are removed before
application access. The native WebUI Authorization header is preserved but its
credential must belong to the signed-in identity. Local signup, LDAP, password,
API-key and administrator credential endpoints are blocked. The original pinned
WebUI image is unchanged; the mounted guard is versioned with the access bundle.

## ACTION

The operator requires both `--hostname-policy-file` and `--access-policy-file`.
Configure `accessPolicy` directly, under `chat-ui-operator` in the MSP umbrella,
or under `operator` in the OCM chart. The ConfigMap contains references, never
client-secret values. Empty defaults intentionally fail startup.

```yaml
accessPolicy:
  kcpServer: https://kcp.example.test
  kcpCA: '' # Optional PEM CA, otherwise system trust; never insecure TLS.
  credentialsNamespace: chat-ui-operator
  proxyImage: registry.example.test/chat-ui-controller@sha256:<released-image-digest>
  entries:
    - uid: <MSP-object-UID>
      namespace: <MSP-namespace>
      name: demo-chat
      source:
        cluster: <KCP-logical-cluster-ID>
        namespace: default
        name: demo-chat
        uid: <KCP-source-object-UID>
      issuer: https://portal.example.test/keycloak/realms/<organization>
      clientID: <registered-client-ID>
      clientSecretName: chat-ui-oidc-<organization>
```

`proxyImage` is the newly built **controller/access-proxy** image, not the separately
pinned OAuth2 Proxy image. The client Secret must contain `client_secret` and live
in the operator credential namespace, separate from all instance namespaces.
Per-organization clients may serve multiple reviewed instances in the same realm.
Each client needs exact `https://<owned-host>/oauth2/callback` redirect URIs,
authorization-code/PKCE support, and an audience accepted by the corresponding
KCP workspace authenticator. Do not broaden the portal client or share its cookie.
Register and deliver credentials through the platform owner, with rotation and
revocation; never sync the registration-management token to a tenant pod.

The source UID cannot be inferred from the MSP object's UID. Read both objects
through trusted access and bind them explicitly. Tenant labels, annotations,
source spec and reported status do not select the authorization target or issuer.
New instances remain inaccessible until an operator-reviewed mapping and client
configuration exist. A source or MSP object recreated under the same name requires
a new mapping. This version does not automate client registration or mapping.

### Client-secret delivery

Optional `clientSecrets` entries create ExternalSecret v1 resources in
`accessPolicy.credentialsNamespace`. Leave the list empty when the platform already
owns delivery. Each referenced **namespaced SecretStore** must exist in that same
namespace and have verified TLS, scoped source access and a managed credential
lifecycle. No SecretStore, reader credential or client registration is created here.

```yaml
clientSecrets:
  - name: chat-ui-oidc-public # Same as accessPolicy.entries[].clientSecretName.
    secretStoreName: chat-ui-public-source
    remoteKey: chat-ui-client-public
```

For the Kubernetes provider, point the platform-owned store at the correct KCP
workspace and the source Secret's namespace. Verify the deployed ESO version,
KCP authentication and authorization-review support before enabling it. The
ExternalSecret requests only `client_secret`; it never copies
`registration_access_token` or all Secret data. Refresh is once per minute.
Source deletion removes the managed destination Secret; an unreachable source
can leave the last value until it recovers. Revoke the IdP client/KCP audience to
revoke access immediately rather than relying on secret synchronization alone.

The controller notices delivered-value changes on its periodic reconciliation
and creates a new immutable bundle with stopped-pod cutover. This resets the
single-pod development database. Changing a client Secret's name also requires
changing the reviewed access mapping. Source reference and remote permissions
remain the platform owner's responsibility. See the
[ESO Kubernetes provider](https://external-secrets.io/latest/provider/kubernetes/).

### Coordinated development migration

1. Inventory and stop the old controller as described in [hostname-policy.md](hostname-policy.md).
   Prepare the owned host mappings, exact source identities, OIDC clients, accepted
   audiences, credential delivery and valid ingress TLS before activating this version.
2. Upgrade both CRDs and the served KCP schema through their owner. Verify
   `status.slug`, `status.slugSource` and `status.accessSecretUIDs` persist. Helm's
   CRD directory alone does not upgrade an existing CRD.
3. Deploy one manager (`Recreate`) and the released digest for its access sidecar.
   On template changes, the controller withdraws its owned Ingress, scales down
   the prior Deployment and waits for observed zero replicas **and no matching
   pods** before installing the guarded template. Foreign objects are never adopted.
   Missing/invalid access mappings also withdraw owned routes and stop owned workloads.
4. Test two ordinary identities and an authorized administrator, denied peer-account
   access, permission revocation, source recreation, first login, account switching,
   refresh, logout, WebSockets, HTTP streaming and actual DNS/TLS/Ingress behavior.
   Confirm pod-IP connections to 8081 and 8082 fail. Local fixtures do not replace
   controlled-account development acceptance.

This implementation permits only one application replica and uses an `emptyDir`
for its application database. Development chat history/settings loss was accepted;
this is **not** authorization or a suitable persistence policy for production.
Replicas greater than one fail closed. Long streams can continue only until their
original ID token expires; a reconnect obtains a refreshed session. The application
JWT lasts one hour. There is a KCP GET per request plus periodic checks on active
streams; size and monitor KCP capacity before rollout.

### Secret identity and recovery

Session signing keys are independently generated from cryptographic randomness.
Session and configuration Secrets are immutable and owned by the MSP instance.
The controller records their API-server-assigned UIDs in `status.accessSecretUIDs`
and confirms persistence before creating a workload. Copied ownership metadata or
delete/recreate under the same name does not satisfy a pin. Configuration bundles
include the guard source and change name with their content or access-proxy image.

A crash between Secret creation and status persistence leaves an untrusted orphan;
reconciliation denies access instead of adopting it. Stop the controller and owned
workload, inspect the orphan/current pins and explicitly recover the intended
identity through the platform owner before restarting. Deleting a pinned Secret
alone is not a supported rotation procedure. Routine OIDC credential/config rotation
creates a new bundle and performs the same stopped-pod cutover. Reverting to an old
bundle also requires reviewed recovery because only the current bundle is pinned.

Instance namespaces, controller status, pod admission and operator configuration
remain platform-controlled. UID checks are not admission controls against arbitrary
MSP administrators changing pods or Secrets after reconciliation. The published
credentials-secret relation must not grant tenants arbitrary MSP Secret writes.
Reserved access Secret names cannot be used as the instance's backend credential.
No host security policy or NetworkPolicy is changed by this implementation.

Rollback to the earlier unauthenticated controller reopens the security issue.
Prefer withdrawing owned routes and stopping workloads while restoring a reviewed
configuration; never present a rollback as continued authenticated protection.

### Local checks

- `make test` and `make lint` (use `GOFLAGS=-buildvcs=false` in an uncommitted worktree).
- `python3 test/access/run_oauth_runtime.py` runs the real pinned OAuth binary with
  disposable TLS/OIDC/KCP fixtures: PKCE, nonce, header replacement, native JWT
  preservation, ID-token refresh, permission revocation and logout.
- `python3 test/access/run_webui_runtime.py` runs the pinned WebUI in a disposable
  container with no host ports or external network; it checks first-user role,
  HTTP/session isolation, forbidden credential routes and socket identity binding.
  Repeat with `ENABLE_WEBSOCKET_SUPPORT=false` for the upstream polling mode.

These fixtures use no live users or model backend and are not a vulnerability audit
of either upstream image. Review the pinned compatibility guard whenever either
WebUI or its Socket.IO dependencies change. OAuth2 Proxy's alpha configuration is
validated against its pinned executable, not only against documentation:
https://oauth2-proxy.github.io/oauth2-proxy/configuration/alpha-config/
