# Development prerelease

The current Release Please configuration targets `0.14.4-rc.1` as a GitHub
prerelease. Use the existing Release Please App workflow and review its generated
release PR before merging. Do not create a manual tag or merge an older stable
release proposal. The bot owns the manifest, changelog, chart-version and default
image/component-version updates; this preparation does not edit them by hand.

The tag workflow publishes the versioned multi-architecture controller image,
which contains both `/manager` and `/access-proxy`, and all seven Helm charts.
Prerelease tags never update the image's `latest` tag or GitHub's latest release.
The packaged standalone/umbrella operator defaults and OCM installer image
overrides are set to the published repository and version. The installer's
component pin is updated by Release Please. The portal UI and OCM installer
charts retain their existing independent source versions; the workflow sets the
version/appVersion of every published chart package.

Before activation, verify the exact release commit/tag, prerelease flag, image
digest and platforms, all chart checksums and rendered operator defaults. Also
verify that the prior stable release and `latest` image digest are unchanged.
The workflow currently treats OCM transfer failure as a warning; workflow success
alone is therefore not proof that the OCM component is available. Verify the
component separately when using the OCM installation path.

Publishing artifacts does not complete the security migration. Follow
[image-policy-upgrades.md](image-policy-upgrades.md),
[hostname-policy.md](hostname-policy.md) and [access-policy.md](access-policy.md):
inventory and stop the old controller, review every existing route/hostname,
update stored unsupported image/replica values before CRD/schema changes, and
provide exact source-instance mappings, registered OIDC clients, accepted KCP
audiences and delivered client credentials. Supply the verified released digest
for `accessPolicy.proxyImage`; do not derive that digest solely from a running
pod. Valid TLS material must exist in each runtime namespace. The empty policy
defaults intentionally prevent startup.

Do not promote this development release or deploy it to production implicitly.
A later stable release needs its own reviewed configuration change: remove the
fixed `release-as` override and set `prerelease` to `false`, then inspect the bot's
chosen stable version. Advancing another development RC likewise requires an
explicit version change and artifact/migration verification.
