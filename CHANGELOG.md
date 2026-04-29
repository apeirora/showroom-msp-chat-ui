# Changelog

## [0.10.7](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.10.6...v0.10.7) (2026-04-29)


### Bug Fixes

* **go:** rename module path to github.com/apeirora/showroom-msp-chat-ui ([d9fef39](https://github.com/apeirora/showroom-msp-chat-ui/commit/d9fef39402c2135379b7cabcb51a29d217ba3d29))

## [0.10.6](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.10.5...v0.10.6) (2026-04-24)


### Bug Fixes

* **release:** make OCM publish non-blocking ([d78e5e1](https://github.com/apeirora/showroom-msp-chat-ui/commit/d78e5e12de5ebb9881581ec5c80f719722986cd3))

## [0.10.5](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.10.4...v0.10.5) (2026-04-24)


### Bug Fixes

* **release:** keep OCM resources by reference ([9f603e9](https://github.com/apeirora/showroom-msp-chat-ui/commit/9f603e9ca2fc7f3b6b11bc0ef0183b8dcb7d3cf4))

## [0.10.4](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.10.3...v0.10.4) (2026-04-24)


### Bug Fixes

* **controller:** reconcile Open WebUI env drift ([841328f](https://github.com/apeirora/showroom-msp-chat-ui/commit/841328f00e74aa46eb853e5507a08431b330797c))

## [0.10.3](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.10.2...v0.10.3) (2026-04-24)


### Bug Fixes

* **controller:** Tolerate slow Open WebUI startup ([9eb3a55](https://github.com/apeirora/showroom-msp-chat-ui/commit/9eb3a55d2031aa297a317106363ff9b3cdecd18b))

## [0.10.2](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.10.1...v0.10.2) (2026-04-24)


### Bug Fixes

* **chart:** name chat-ui-operator-ocm chart after its directory ([#73](https://github.com/apeirora/showroom-msp-chat-ui/issues/73)) ([57a9fe0](https://github.com/apeirora/showroom-msp-chat-ui/commit/57a9fe0b466ad0555ae4ad7dd2ff3a19b7130cd2))
* **chart:** rewrite workspace-scoped KCP kubeconfigs ([8c2e360](https://github.com/apeirora/showroom-msp-chat-ui/commit/8c2e360173ee2b59aea6c804f53995ffd0afdaaa))

## [0.10.1](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.10.0...v0.10.1) (2026-04-23)


### Bug Fixes

* **controller:** drop grok default, add stable WEBUI_SECRET_KEY ([#71](https://github.com/apeirora/showroom-msp-chat-ui/issues/71)) ([a67063a](https://github.com/apeirora/showroom-msp-chat-ui/commit/a67063a3082f65f0d1826e0f878cfcbe7f2f5202))

## [0.10.0](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.9.7...v0.10.0) (2026-04-22)


### Features

* **ci:** build multi-arch (amd64 + arm64) operator image ([#68](https://github.com/apeirora/showroom-msp-chat-ui/issues/68)) ([404828e](https://github.com/apeirora/showroom-msp-chat-ui/commit/404828ec38a69fc1062cee9e5f85c91502fdedb3))

## [0.9.7](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.9.6...v0.9.7) (2026-04-21)


### Bug Fixes

* **chart:** drop permissionClaims from chart, let sync-agent own them ([#66](https://github.com/apeirora/showroom-msp-chat-ui/issues/66)) ([70c6ad2](https://github.com/apeirora/showroom-msp-chat-ui/commit/70c6ad2ad232765ce326259af4e439ddf3a85d0f))

## [0.9.6](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.9.5...v0.9.6) (2026-04-21)


### Reverts

* **chart:** APIExport claim shape to verbs-only (no all:true) ([#64](https://github.com/apeirora/showroom-msp-chat-ui/issues/64)) ([67b80c6](https://github.com/apeirora/showroom-msp-chat-ui/commit/67b80c6cb0912f2e459bb0ebf73b21a7014b5278))

## [0.9.5](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.9.4...v0.9.5) (2026-04-21)


### Bug Fixes

* **chart:** APIExport claims need both verbs and all:true ([#62](https://github.com/apeirora/showroom-msp-chat-ui/issues/62)) ([ec08d3d](https://github.com/apeirora/showroom-msp-chat-ui/commit/ec08d3d66269a201f00cebd1af0f139bd90c4f6c))

## [0.9.4](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.9.3...v0.9.4) (2026-04-21)


### Bug Fixes

* **chart:** emit APIExport permissionClaims with all:true ([#60](https://github.com/apeirora/showroom-msp-chat-ui/issues/60)) ([500bd33](https://github.com/apeirora/showroom-msp-chat-ui/commit/500bd33cf9c216a5aeb847725baa4e311e0a7de0))

## [0.9.3](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.9.2...v0.9.3) (2026-04-21)


### Bug Fixes

* **ci:** recursively pull deps before packaging umbrella charts ([#58](https://github.com/apeirora/showroom-msp-chat-ui/issues/58)) ([47c26df](https://github.com/apeirora/showroom-msp-chat-ui/commit/47c26df367d243b1b9a9697a15517a4387000694))

## [0.9.2](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.9.1...v0.9.2) (2026-04-21)


### Bug Fixes

* **chart:** widen umbrella sub-chart version constraints ([#56](https://github.com/apeirora/showroom-msp-chat-ui/issues/56)) ([1eb50ce](https://github.com/apeirora/showroom-msp-chat-ui/commit/1eb50ceffbfbecaadc55eeb832533255f2f1b1ba))

## [0.9.1](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.9.0...v0.9.1) (2026-04-21)


### Bug Fixes

* **build:** bump builder image to golang:1.24 to match go.mod ([#54](https://github.com/apeirora/showroom-msp-chat-ui/issues/54)) ([e25af96](https://github.com/apeirora/showroom-msp-chat-ui/commit/e25af96275a4ba2697ace434d44fd02fdbeff77b))

## [0.9.0](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.8.0...v0.9.0) (2026-04-21)


### Features

* **charts:** add chat-ui-msp-app and chat-ui-pm-app umbrellas ([#53](https://github.com/apeirora/showroom-msp-chat-ui/issues/53)) ([6deea0d](https://github.com/apeirora/showroom-msp-chat-ui/commit/6deea0dc9642fa352411cfb3a74189d1c17cec8c))
* enable OCM component publishing ([973d59c](https://github.com/apeirora/showroom-msp-chat-ui/commit/973d59cdfba69a5b3756c0f70ff7a31b3acd6f0d))
* enable OCM component publishing ([d36b086](https://github.com/apeirora/showroom-msp-chat-ui/commit/d36b086e6e000594b1ca94fec7e5c032c5f94d34))


### Bug Fixes

* add chat-ui-ui to HELM_CHARTS and fix docs imageRepository default ([a7fc91b](https://github.com/apeirora/showroom-msp-chat-ui/commit/a7fc91bdf735ce45a10a7ed81e00bafb7479769f))
* build-local.sh chart output path and bootstrap imagePullSecrets ([dd70d2d](https://github.com/apeirora/showroom-msp-chat-ui/commit/dd70d2d8ef18ed00abee73a0714c09e77c189406))
* correct default VERSION to 0.8.0 ([abde29e](https://github.com/apeirora/showroom-msp-chat-ui/commit/abde29ea8e9ae6839e609c632c105d47dd65af3a))
* correct imagePullSecrets path for chat-ui chart ([2da5e3c](https://github.com/apeirora/showroom-msp-chat-ui/commit/2da5e3cdcbcaf57730cc6d62a36eb9de1235a0d6))
* correct imagePullSecrets path in bootstrap.yaml ([616b607](https://github.com/apeirora/showroom-msp-chat-ui/commit/616b607e9667819528f5299d36e8b1f3ff910dd1))
* **metadata:** remove tracked contact email ([#42](https://github.com/apeirora/showroom-msp-chat-ui/issues/42)) ([aa63854](https://github.com/apeirora/showroom-msp-chat-ui/commit/aa63854da28e0400a6b3fbe48468fa32ea873419))
* OCM chart consistency improvements ([94af2f2](https://github.com/apeirora/showroom-msp-chat-ui/commit/94af2f241be8287418f7513eda50b385aaa50e09))
* replace KRO with plain OCM resources (tested on cluster) ([d27aa62](https://github.com/apeirora/showroom-msp-chat-ui/commit/d27aa628e3a005c96271830e0e9c9cfec2851765))
* resolve merge conflict in bootstrap.yaml (take main) ([598d776](https://github.com/apeirora/showroom-msp-chat-ui/commit/598d776f52d3ba251cad49d3ae7c6c8084768798))
* resolve merge conflict in OCM installation docs ([2abee27](https://github.com/apeirora/showroom-msp-chat-ui/commit/2abee27f878ece4ff6b18f2e778f930023018cca))
* **sync-agent:** bump api-syncagent to 0.5.1 for hostAliases support ([#48](https://github.com/apeirora/showroom-msp-chat-ui/issues/48)) ([d4235a2](https://github.com/apeirora/showroom-msp-chat-ui/commit/d4235a293af3bc593511a385b9226d5d315d59fc))
* **sync-agent:** fix hostAliases format and add apiExportEndpointSliceName ([#49](https://github.com/apeirora/showroom-msp-chat-ui/issues/49)) ([4c7dbe1](https://github.com/apeirora/showroom-msp-chat-ui/commit/4c7dbe1e1568255d8706f8e97e23966746b1753b))
* update OCM chart for controller v0.29.0 API compatibility ([2aca207](https://github.com/apeirora/showroom-msp-chat-ui/commit/2aca207ef605fd4726e1c08e712d37f666a87c6c))

## [0.8.0](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.7.0...v0.8.0) (2026-03-03)


### Features

* **chart:** make replicas field required ([#31](https://github.com/apeirora/showroom-msp-chat-ui/issues/31)) ([b43b3a8](https://github.com/apeirora/showroom-msp-chat-ui/commit/b43b3a8023a5ff3fe4711073ecbed6b90be8b7f1))

## [0.7.0](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.6.1...v0.7.0) (2026-03-03)


### Features

* **ui:** replace replicas text input with dropdown selector ([#29](https://github.com/apeirora/showroom-msp-chat-ui/issues/29)) ([24d7c6d](https://github.com/apeirora/showroom-msp-chat-ui/commit/24d7c6d9f2b4125bebe335c36ae81f1b65ea7d6d))

## [0.6.1](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.6.0...v0.6.1) (2026-03-02)


### Bug Fixes

* **controller:** update secret references when credentialsSecretRef changes ([#27](https://github.com/apeirora/showroom-msp-chat-ui/issues/27)) ([f80e3bf](https://github.com/apeirora/showroom-msp-chat-ui/commit/f80e3bfa0bb97d9e383e20f6da5735aa62de5763))

## [0.6.0](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.5.5...v0.6.0) (2026-02-26)


### Features

* **controller:** add DNS resolution gate to readiness checks ([#25](https://github.com/apeirora/showroom-msp-chat-ui/issues/25)) ([e874510](https://github.com/apeirora/showroom-msp-chat-ui/commit/e874510f462591e0ac5d0fb9730ebebaeb524c00))

## [0.5.5](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.5.4...v0.5.5) (2026-02-26)


### Bug Fixes

* **controller:** Gate chat UI readiness on deployment and service health ([#23](https://github.com/apeirora/showroom-msp-chat-ui/issues/23)) ([03397fe](https://github.com/apeirora/showroom-msp-chat-ui/commit/03397fe1441e4ac251573cc4b3c63555c23148a5))

## [0.5.4](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.5.3...v0.5.4) (2026-02-18)


### Bug Fixes

* add required verbs field to v1alpha2 permissionClaims ([#21](https://github.com/apeirora/showroom-msp-chat-ui/issues/21)) ([7ea3c44](https://github.com/apeirora/showroom-msp-chat-ui/commit/7ea3c4423258ab9795316d1ab304ea6b67128cea))

## [0.5.3](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.5.2...v0.5.3) (2026-02-18)


### Bug Fixes

* remove v1alpha1 'all' field from permissionClaims ([#19](https://github.com/apeirora/showroom-msp-chat-ui/issues/19)) ([ec0d337](https://github.com/apeirora/showroom-msp-chat-ui/commit/ec0d3373b9d29e24c48caa1cf9f91ffa81e4df8d))

## [0.5.2](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.5.1...v0.5.2) (2026-02-18)


### Bug Fixes

* update APIExport apiVersion to v1alpha2 ([#17](https://github.com/apeirora/showroom-msp-chat-ui/issues/17)) ([02bccc9](https://github.com/apeirora/showroom-msp-chat-ui/commit/02bccc90e0e7f4c7d177a38649c2175cf223be75))

## [0.5.1](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.5.0...v0.5.1) (2026-02-17)


### Bug Fixes

* **chart:** sync chat-ui credential secret from kcp ([7425611](https://github.com/apeirora/showroom-msp-chat-ui/commit/74256113c489d806b277bc685ad40742ccb13b49))
* **core:** Restore ChatUI Credentials Secret dropdown ([49b49fb](https://github.com/apeirora/showroom-msp-chat-ui/commit/49b49fb795fe2305a41a6a94fda791b412a05fc6))
* **sync-agent:** sync chat-ui credential secret from kcp ([1406d17](https://github.com/apeirora/showroom-msp-chat-ui/commit/1406d175db8c9f6187555673dbc74fd6e6feabc5))
* **ui:** add versioned Chat UI pm-content schema metadata ([623d9a2](https://github.com/apeirora/showroom-msp-chat-ui/commit/623d9a21443ab88e4a6deddd45cb66a438e10d34))
* **ui:** restore Credentials Secret dropdown for ChatUI create form ([ecc7dfb](https://github.com/apeirora/showroom-msp-chat-ui/commit/ecc7dfbed45d68c16ba06d836a6711c0d678309b))

## [0.5.0](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.4.3...v0.5.0) (2026-02-15)


### Features

* **ui:** display Chat UI URL as clickable link ([#12](https://github.com/apeirora/showroom-msp-chat-ui/issues/12)) ([bc66cd8](https://github.com/apeirora/showroom-msp-chat-ui/commit/bc66cd8b1613fa11a84de9786c2dcc59939bb629))

## [0.4.3](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.4.2...v0.4.3) (2026-02-15)


### Bug Fixes

* trigger pod rollout when credentials secret changes ([#10](https://github.com/apeirora/showroom-msp-chat-ui/issues/10)) ([4a9eddc](https://github.com/apeirora/showroom-msp-chat-ui/commit/4a9eddc95c54fdff49d5c9f07ab2c1e186f8337b))

## [0.4.2](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.4.1...v0.4.2) (2026-01-29)


### Bug Fixes

* **pm-integration:** add RBAC for sync-agent virtual workspace access ([0d6dc25](https://github.com/apeirora/showroom-msp-chat-ui/commit/0d6dc25520eaab2b2b94bc0949c0d9018b313d50))

## [0.4.1](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.4.0...v0.4.1) (2025-12-02)


### Bug Fixes

* valid json ([316719d](https://github.com/apeirora/showroom-msp-chat-ui/commit/316719d34a60eb6ebf3984ab047f4613ada78b27))

## [0.4.0](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.3.0...v0.4.0) (2025-12-02)


### Features

* add SVG chat icon and update metadata to use it ([#7](https://github.com/apeirora/showroom-msp-chat-ui/issues/7)) ([b25fb86](https://github.com/apeirora/showroom-msp-chat-ui/commit/b25fb867c3d66b3b5e00f79bf6ffdbba1ef5d74c))
* dns tenants ([#5](https://github.com/apeirora/showroom-msp-chat-ui/issues/5)) ([0608d2c](https://github.com/apeirora/showroom-msp-chat-ui/commit/0608d2c908093c038aecfa80517c6d6cd84aecb7))

## [0.3.0](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.2.0...v0.3.0) (2025-12-01)


### Features

* update build-push and ci workflows to include chat-ui-ui component and comment out coverage steps ([#3](https://github.com/apeirora/showroom-msp-chat-ui/issues/3)) ([0c00373](https://github.com/apeirora/showroom-msp-chat-ui/commit/0c0037317a83ccdb645b65f6d52e03fd3c22235b))

## [0.2.0](https://github.com/apeirora/showroom-msp-chat-ui/compare/v0.1.0...v0.2.0) (2025-12-01)


### Features

* add code, ci/cd and Helm charts ([#1](https://github.com/apeirora/showroom-msp-chat-ui/issues/1)) ([1e89a3b](https://github.com/apeirora/showroom-msp-chat-ui/commit/1e89a3b16206c358d4edd9357336e603512a81bf))
