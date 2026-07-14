# Changelog

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
