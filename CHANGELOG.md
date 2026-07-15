# Changelog

## [0.2.3](https://github.com/sympozium-ai/ergoz/compare/v0.2.2...v0.2.3) (2026-07-15)


### Bug Fixes

* **collector:** age readings when agent discovery fails ([#13](https://github.com/sympozium-ai/ergoz/issues/13)) ([9711317](https://github.com/sympozium-ai/ergoz/commit/971131737caf6877c0f1cbe02440d385addda458))

## [0.2.2](https://github.com/sympozium-ai/ergoz/compare/v0.2.1...v0.2.2) (2026-07-14)


### Features

* **chart:** opt-in nvidia.devAccess for NVML device access ([#10](https://github.com/sympozium-ai/ergoz/issues/10)) ([aaf05f3](https://github.com/sympozium-ai/ergoz/commit/aaf05f3d8694c8952940847538f5bb458f8cbbd7)), closes [#7](https://github.com/sympozium-ai/ergoz/issues/7)
* **cli:** ergoz status -o json|yaml for machine-readable output ([721a0cb](https://github.com/sympozium-ai/ergoz/commit/721a0cb854c93929992e882bf9f8e039c4e83740))


### Bug Fixes

* **release:** never cancel main-branch CI runs — releases gate on them ([#9](https://github.com/sympozium-ai/ergoz/issues/9)) ([e4faa57](https://github.com/sympozium-ai/ergoz/commit/e4faa5754346b24b4e4d5fce93443a1a4a95cfa8)), closes [#8](https://github.com/sympozium-ai/ergoz/issues/8)

## [0.2.1](https://github.com/sympozium-ai/ergoz/compare/v0.2.0...v0.2.1) (2026-07-13)


### Bug Fixes

* **install:** fail fast with cause on image-pull/crashloop; amd64-only image ([066173d](https://github.com/sympozium-ai/ergoz/commit/066173d04eb723c1bc138c87c53833ec341086d0))

## [0.2.0](https://github.com/sympozium-ai/ergoz/compare/v0.1.3...v0.2.0) (2026-07-13)


### ⚠ BREAKING CHANGES

* /api/v1/fleet drops the per-device energyJoules field and the two *_energy_joules_total metrics are gone. Consumers wanting watt-hours integrate powerWatts × time downstream.

### Features

* point-in-time watts only — drop energy accumulation, add stale-marking ([b1fb83d](https://github.com/sympozium-ai/ergoz/commit/b1fb83db4edfcf83dea29738134319bb8ebf151e))

## [0.1.3](https://github.com/sympozium-ai/ergoz/compare/v0.1.2...v0.1.3) (2026-07-13)


### Bug Fixes

* agent image now runs; enforce security claims; vuln gate ([4deaa28](https://github.com/sympozium-ai/ergoz/commit/4deaa28d5ad4565c7c6e1c823ffe53f068271bce))

## [0.1.2](https://github.com/sympozium-ai/ergoz/compare/v0.1.1...v0.1.2) (2026-07-13)


### Features

* **brand:** sibling-stack diagram (sympozium / llmfit-dra / ergoz) ([3ca9377](https://github.com/sympozium-ai/ergoz/commit/3ca9377609a74aeadafacbd530c96591ac1f5a49))
* Phase 1 — NVIDIA GeForce power via NVML (runtime dlopen, no cgo) ([7565ba9](https://github.com/sympozium-ai/ergoz/commit/7565ba95a87fda785a83462175e20cebf3a47bf5))

## [0.1.1](https://github.com/sympozium-ai/ergoz/compare/v0.1.0...v0.1.1) (2026-07-13)


### Features

* **brand:** Meter mark + blocky ERGOZ lockup at the top of the README ([9520399](https://github.com/sympozium-ai/ergoz/commit/9520399a9f223a3d8f2bf4e5301942800e396351))
