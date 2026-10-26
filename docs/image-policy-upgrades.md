# Image policy upgrades

The operator release controls the Open WebUI image. `spec.image` may be absent,
empty, or exactly the supported digest in `api/v1alpha1.SupportedImage`. Both CRD
copies enforce the same enum; reconciliation independently enforces the policy.
This is an exact image approval, not permission to run every version in a registry.

The initial pin is the GHCR multi-platform index reported by both running cc-d2
Chat UI pods on 26 October 2026. Its manifest bytes were checked against its digest,
and it contains Linux amd64 and arm64 images. This preserves that observed build;
it is not a vulnerability scan or a claim that other environments run the same build.
Registry metadata identifies upstream `main` revision
`33c1978d5bad52a31df75ea55143025dfad18d16`, built 25 October 2026; both `main`
and `latest` resolved to the pinned index at verification time. This is not a
versioned stable release. Maintainers must approve retaining this build or choose
and validate another pin before deployment; upstream digest retention is not
guaranteed, so mirror it if deployment availability requires retained artifacts.

## Existing installations

1. Inventory `spec.image` in **both** the tenant KCP objects and their MSP copies.
   Record the running pod image digests and deployment ownership. Do not include
   Secret values. Explicit tags/custom images become unsupported.
2. Before installing the stricter schema, clear explicit image values in KCP and
   allow the sync agent to propagate the change. This includes old explicit pins
   when a future release changes the supported digest. Verify the MSP copies.
   A stricter schema can reject ordinary updates and finalizer removal on stored
   invalid objects; do not depend on validation ratcheting in Kubernetes or KCP.
3. Deploy the reviewed controller in the intended development environment. It
   enforces the policy even with the old schema. Unsupported requests receive
   `ImageNotAllowed`; their owned Deployment is scaled to zero. Deletion still
   follows the ordinary finalizer path under the old schema.
4. For a supported request with an older Deployment image, the controller first
   scales that Deployment to zero and waits for its controller to observe the
   scale-down. It then sets the pinned image and restores requested replicas.
   Terminating old pods can remain during their shutdown grace period.
   This runs before Secret access, so a missing Secret cannot leave an unapproved
   old image running indefinitely. `Ready=False`/`ImageMigration` describes the
   transition. Expect downtime and loss of all container-local state, including chat
   history, uploads, settings and sessions; these Deployments have no persistent
   volumes. Check actual pod termination and image IDs before accepting the rollout.
5. Once no invalid stored values remain, apply the reviewed CRD upgrade through its
   owning deployment system. Helm's `crds/` directory alone does not upgrade an
   existing CRD. Check the Flux HelmRelease CRD policy and the actual served schema;
   do not assume a chart version bump applied the enum. Then check the sync agent's
   KCP APIResourceSchema and APIExport propagation. The sync agent can propagate the
   stricter schema automatically, so finish KCP cleanup before applying the MSP CRD.
   Regeneration also adds standard validation of status conditions (status enum,
   reason syntax and length limits); verify stored conditions before applying it.
   MSP admission alone does not prove tenant-facing KCP admission.
6. With controlled test accounts, verify a supported instance, rejection of a
   foreign image/tag/different digest on create and update, status propagation,
   and recovery after clearing `spec.image`. Check credential **references**, not
   values. Repeat acceptance separately for each approved environment rollout.

No rollout should claim all old pods stopped merely because a scale update was
accepted. A blocked Pod termination or failing Deployment controller needs operator
attention. An unowned Deployment is not modified; reconciliation reports
`ImagePolicyError` and returns an error.

## Changing the supported image

Review the upstream build and its supported platforms, update `SupportedImage` and
the image enum marker together, and regenerate manifests and DeepCopy code.
`make manifests` also synchronizes the operator chart CRD. Keep the credential-name
validation and `cui` short name intact. Run tests, lint and chart checks. Repeat the
stored-image migration above before tightening the enum to the new pin.

Rolling back to a controller without image enforcement restores arbitrary-image
execution. That is not a security resolution. Prefer restoring the last reviewed
pin in a controller release that retains enforcement.

Image-rewriting admission policies and mirrors that change the Deployment image
reference are unsupported by this exact-reference contract. Such a rewrite would
cause repeated scale-downs. Inventory these policies before rollout; approve a
release pin in the destination repository when mirroring is required.

The JSON API shape is unchanged. Go callers constructing these API types must use
`CredentialsSecretReference` instead of `corev1.LocalObjectReference`; the local
type preserves the existing required, non-empty Secret name during generation.
