# Changelog

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
