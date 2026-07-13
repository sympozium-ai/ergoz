# Ergoz

**Vendor-neutral accelerator power telemetry for Kubernetes — consumer hardware first.**

Ergoz (Greek ἔργον *ergon*, "work" — the root of *energy*) measures the current
power draw of accelerators (GPUs, NPUs, …) across a fleet and exposes it as
Kubernetes-native telemetry. It exists because the incumbents are vendor
silos: dcgm-exporter is datacenter-NVIDIA, Kepler is RAPL+NVML in practice,
AMD's exporter is Instinct-only. Nothing covers the commodity hardware people
actually run local inference on — and on AMD consumer hardware, the kernel
already exposes everything needed, world-readable, with zero vendor userspace.

First consumer: [Sympozium](https://github.com/sympozium-ai/sympozium)
(placement decisions and the electricity dimension of run cost estimation).
Device identity comes from
[`llmfit-dra/pkg/probe`](https://github.com/sympozium-ai/llmfit-dra) — one
source of truth for accelerator identification across the org.

## Architecture (Phase 0)

```
┌────────── node ──────────┐
│ ergoz-agent (DaemonSet)  │   reads /sys (hostPath, ro):
│  probe walk → devices    │   - amdgpu hwmon power1_input (µW)
│  sample loop (1s)        │   - amdgpu gpu_metrics v3.0 blob (validated decode)
│  Σ W·Δt energy integr.   │   - power/runtime_status (D9 suspend gate)
│  :9743/metrics           │
└──────────┬───────────────┘
           │ scraped every 5s via headless-Service DNS
┌──────────▼───────────────┐
│ ergoz-collector (Deploy) │   :9744/metrics      merged fleet Prometheus view
│  fleet cache             │   :9744/api/v1/fleet JSON for Sympozium etc.
└──────────────────────────┘
```

No CRDs (decision D1), no RBAC (nothing talks to the Kubernetes API), fully
non-root today (hwmon power files are world-readable on vanilla kernels).

## Metrics

| Metric | Meaning |
|---|---|
| `ergoz_accel_power_watts{node,kind,vendor_id,device_id,pci,driver}` | Instantaneous board/socket power (hwmon) |
| `ergoz_accel_component_power_watts{...,component}` | Decomposed power where the ASIC exposes it: `socket`, `gfx`, `npu`, `cpu_cores`. Fields failing per-ASIC sanity validation are **absent, never zero** |
| `ergoz_accel_energy_joules_total{...}` | Software-integrated energy since agent start |
| `ergoz_accel_runtime_suspended{...}` | 1 = device runtime-PM suspended; power reported as synthetic 0 W rather than waking it (D9) |

Names are accelerator-neutral (D8): `kind` is a label (`gpu`, `npu`, …), so new
device classes need no renames.

## Verified hardware (empirical, not aspirational)

| Hardware | Source | Status |
|---|---|---|
| AMD Strix Halo APU (`amdgpu`) | hwmon `power1_input` + `gpu_metrics` v3.0 | ✅ live: socket/gfx/per-core decomposition agrees with hwmon within 0.2 W |
| AMD XDNA NPU (`amdxdna`) | runtime_status | ✅ live: reported suspended, synthetic 0 W |
| Intel Lunar Lake iGPU (`xe`) | — | ✅ correctly reported unmeasurable (no hwmon power exists; a known kernel gap) |
| NVIDIA GeForce | NVML (`libnvidia-ml.so.1`, dlopen) | 🔜 Phase 1 |
| Intel Arc dGPU | xe/i915 `energy1_input` (µJ counter) | 🔜 Phase 1 |
| CPU package (RAPL) | `/sys/class/powercap` (root-only) | 🔜 Phase 2, opt-in privileged mode |

The `gpu_metrics` parser trusts only fields validated on real silicon: 0xFFFF
sentinels are dropped, `average_all_core_power` is recomputed from the
per-core array (observed ~2x disagreement), and gfx > socket readings are
discarded rather than exported. See `internal/gpumetrics`.

## Install the CLI

While the repo is private (uses your existing `gh` auth):

```bash
gh api repos/sympozium-ai/ergoz/contents/install.sh -H "Accept: application/vnd.github.raw" | sh
```

Once public, the sympozium-style one-liner works as-is:

```bash
curl -fsSL https://raw.githubusercontent.com/sympozium-ai/ergoz/main/install.sh | sh
```

(Append `-s -- --local` to install to `~/.local/bin` without sudo. With a Go
toolchain, `GOPRIVATE=github.com/sympozium-ai go install
github.com/sympozium-ai/ergoz/cmd/ergoz@latest` also works.)

## CLI

```
$ ergoz install                 # applies the embedded manifests (no CRDs, no RBAC)
$ ergoz status
ERGOZ FLEET  ·  1/1 agents up  ·  total 18.0 W  ·  scraped 13:52:02
└─ kind-control-plane  18.0 W
   ├─ gpu  0000:c3:00.0   amdgpu (1002:1586)  18.0 W  [socket 18.4 W · gfx 0.0 W · npu 0.0 W · cpu_cores 4.4 W]
   └─ npu  0000:c4:00.1   amdxdna (1022:17f0)  suspended (0 W)
$ ergoz uninstall
```

`ergoz status` reads the collector through the kube-apiserver service proxy —
no port-forward needed. `ergoz install --image-tag dev` targets a sideloaded
(e.g. `kind load`-ed) image. `--kubeconfig`/`--context` behave as usual.

## Run it from source

```bash
make build          # binaries in bin/ (agent, collector, CLI)
make test           # unit tests (go test -race)
make docker-build   # single image, two entrypoints
bin/ergoz install --image-tag dev && bin/ergoz status
```

Agent knobs (env): `ERGOZ_SAMPLE_INTERVAL` (default `1s` — a hwmon read costs
~5 µs, so even 100ms is cheap), `ERGOZ_SYSFS_ROOT`, `ERGOZ_LISTEN`.
Collector knobs: `ERGOZ_SCRAPE_INTERVAL` (default `5s`), `ERGOZ_AGENT_SERVICE`,
`ERGOZ_AGENT_PORT`.

## Security notes

- The agent mounts host `/sys` **read-only** and runs non-root,
  `readOnlyRootFilesystem`, all capabilities dropped, RuntimeDefault seccomp.
- Power telemetry is a side channel: fleet-wide watts can reveal that/when
  workloads run. Keep the collector Service cluster-internal and put RBAC or
  network policy in front of anything that re-exports it. The default sample
  interval (≥1 s) is a deliberate floor.

## Roadmap

- **Phase 1**: NVIDIA GeForce via NVML (dlopen, no cgo hard-dep); Intel Arc
  energy counters (ΔµJ/Δt with wrap handling); per-ASIC `gpu_metrics` v1/v2
  tables for AMD dGPUs and older APUs.
- **Phase 2**: opt-in privileged mode for RAPL CPU package power; OTLP push
  exporter; device→pod attribution via kubelet pod-resources (consumed by
  llmfit-dra/Sympozium — Ergoz itself stays observability-only, decision D4/D7).
