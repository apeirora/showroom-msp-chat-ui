# Hostname ownership and migration

The operator selects public names from immutable service-cluster identity and an
operator-owned policy file. It does not use tenant annotations, labels or status
to choose the hostname. Existing slug annotations are left untouched and ignored,
so tenant metadata synchronization does not become an ownership mechanism.

New names are `ui-` plus the first 32 hexadecimal characters of
SHA-256(`ui.privatellms.msp/slug/v1:` + MSP object UID). This is a 128-bit suffix,
not a mathematical guarantee against collisions. Session signing keys are generated
independently from cryptographic randomness. The guarded WebUI requires organization
OAuth login and the user's KCP `get` permission on the exact source ChatUIInstance;
there is no separate chat role. Each instance needs an operator-reviewed source
mapping, OIDC client credentials and valid ingress TLS as described in
[access-policy.md](access-policy.md). Controlled-account login, denial, revocation
and browser checks on the target environment remain rollout acceptance gates.

## Required operator configuration

The manager requires both `--hostname-policy-file` and `--access-policy-file`.
It validates both files before starting any controller. The chart mounts a
ConfigMap generated from `hostnamePolicy` values; only platform operators may
manage this configuration.
There is no tenant-facing field for opting out or requesting a legacy name.

```yaml
hostnamePolicy:
  # Example only. Replace with the fixed timestamp of the stopped-controller inventory.
  cutover: '2026-10-27T09:00:00Z'
  entries:
    - uid: '<actual-MSP-instance-UID>'
      namespace: '<actual-MSP-namespace>'
      name: 'existing-ui'
      mode: legacy
      slug: 'verified-existing-name'
    # Only for an inventoried old object verified never provisioned:
    - uid: '<another-actual-MSP-UID>'
      namespace: '<actual-MSP-namespace>'
      name: 'unused-ui'
      mode: derive
```

Use actual MSP identities, not the source KCP object's UID. Never template the
cutover from the current time at each installation. Empty/malformed configuration,
unknown fields, duplicate identities/slugs and invalid legacy names stop startup.
Startup also checks the complete hostname length for every legacy name.
Legacy entries cannot use the reserved `ui-<32 lowercase hex>` format. Mappings
bind UID, namespace and name together. A changed UID never inherits a mapping.

Unmapped objects created at or before the cutoff report `LegacyHostnameUnmapped`,
with `Ready=False` and no published URL. They are not silently renamed. This holds
even if their tenant annotation or previous status names a valid address. The
ordinary Chat UI deletion path remains available without a mapping.

With the instance access gate, this denial withdraws an owned Ingress and stops
the owned workload. Foreign routes require operator recovery. The migration inventory must resolve every existing route before
rollout; an unowned or ambiguous route requires a separately authorized repair.
The independent image policy can still stop an unsupported Deployment before
hostname validation. Do not describe unresolved legacy routing as secured.

## Route checks

Before credential access and again before creating/updating an Ingress, uncached
API reads check the named Ingress controller identity and existing Ingress rule hosts
across namespaces. A foreign or unowned same-named Ingress is not adopted. Existing
exact-host and matching single-label wildcard rules conflict, regardless of class.
Errors report `HostnameOwnershipError` or `HostnameConflict` without exposing the
other namespace/name. API read errors stop reconciliation without copying raw
errors to tenant status. Ready instances are checked again after 30 seconds. Each
pass repeats HTTP/DNS checks and two cluster-wide Ingress lists; budget for this
load and for transient health failures moving an instance back to Provisioning.

The owned Ingress's rules converge to one host/path/backend reference; stale rules
and default backends are removed. The access integration also verifies ownership and converges the backend Service. Additional TLS entries and unmanaged annotations are
preserved by the existing reconciler and require inventory. The API's
resource-version checks protect updates against concurrent edits to that object.

The selected `status.slug` can appear before validation finishes and can remain on
a conflict. Consumers must also require `Ready=True` for the current generation;
the slug alone is not proof of route ownership or current serving behavior.

A pre-write list is not an atomic reservation across different routing objects.
Other controllers can race it, and alternate routing kinds such as IngressRoute
are not inspected. Neither are host-less rules, default backends in other routes,
TLS hosts or DNS annotations. The managed hostname scope must be exclusive to this operator,
or a shared admission/claim mechanism must be established before claiming protection
against those writers. Kubernetes tenants using the published ChatUIInstance API
cannot choose an address through its annotations; direct MSP routing writers remain
part of the platform trust boundary. Inventory DNS and all routing kinds separately.

## Coordinated upgrade

1. Stop the old operator and verify its pods have stopped reconciling. Do not use
   simultaneous old/new versions. Inventory all MSP instances and their routes,
   including not-ready and terminating instances and any legacy namespace scheme.
   Verify controller owner references on every legacy Ingress. Include wildcard
   rules under PUBLIC_HOST in every class: a matching wildcard blocks all managed
   names. Inventory host-less/catch-all routes, default backends, TLS hosts and DNS
   annotations as well as alternate routing kinds and external DNS records.
2. Resolve ownership from trusted account/resource evidence. Do not mechanically
   bless the tenant annotation or existing route: either may already be disputed.
   Set the fixed cutoff at or after the verified stop of the old controller, using
   the API server's clock (which assigns creation timestamps). An earlier cutoff
   could silently rename an object provisioned between that cutoff and the stop.
   Set explicit mappings for all existing provisioned objects.
3. Apply the CRD status fields through the owning deployment system and verify the
   KCP APIResourceSchema receives `status.slug` and `status.slugSource`. An old
   schema can prune them. The manager verifies persistence using an uncached read
   after a hostname status change; pruning stops reconciliation before credentials
   or routes, leaves a stable non-ready status, and returns a retryable log error.
   The access integration withdraws owned routes and stops owned workloads on this failure. Helm's CRD directory alone does not upgrade existing CRDs.
   Coordinate this with the separate image-policy migration in
   [image-policy-upgrades.md](image-policy-upgrades.md); all prior gates still apply.
   Finish the required image-value cleanup before applying the combined schema.
   Inventory and correct existing `spec.replicas > 1` values to `1` before tightening
   the CRD and served KCP schema. This version supports one application replica;
   `0` uses the default of one and is not a shutdown command. Do not assume stored
   out-of-range values or their status updates remain valid after the schema change.
4. Deploy one new manager with the reviewed map. The chart requires one replica,
   uses `Recreate`, and restarts on ConfigMap content changes. A different deployment
   of the old controller must also be stopped; the chart cannot enforce that.
5. Verify preserved addresses, new instance creation, ignored tenant changes,
   conflict rejection/recovery, deletion and actual ingress/DNS behavior with
   controlled accounts. Local/envtest checks are not KCP/Traefik acceptance.

Keep each reviewed mapping for the lifetime of its object. The current cutoff
cannot distinguish a post-cutoff restore or sync-agent recreation with a new UID
from a genuinely new object. Stop the controller, inventory old routes and review
the remap before such recovery. Changing the public suffix or legacy mapping also
changes routing and needs a reviewed migration. Reverting to the previous operator
restores trust in tenant annotations and is not a security resolution.
It also gives Derived instances random annotated names, changing their URLs;
rollback therefore needs its own reviewed route mapping.

This commit covers the Chat UI operator only. Private LLM's auth, token and BYOC
resource/cleanup consumers still require their own matching migration; their
annotation readers are not changed by this patch.
