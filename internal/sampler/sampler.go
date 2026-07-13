// Package sampler runs the per-node measurement loop: for each identified
// accelerator, read power at the configured cadence, software-integrate
// energy (Σ W·Δt), and publish Prometheus metrics.
//
// Metric names are accelerator-neutral (project decision D8): kinds are
// labels, not name segments, so NPUs/DPUs slot in without renames.
package sampler

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/sympozium-ai/ergoz/internal/gpumetrics"
	"github.com/sympozium-ai/ergoz/internal/hwmon"
	"github.com/sympozium-ai/ergoz/internal/nvml"
	"github.com/sympozium-ai/llmfit-dra/pkg/probe"
)

// pciVendorNVIDIA is the PCI vendor id for NVIDIA (no 0x prefix, per probe).
const pciVendorNVIDIA = "10de"

// Target is one accelerator under measurement.
type Target struct {
	Device probe.Device
	Sensor hwmon.Sensor
	// NVMLDev is non-nil for NVIDIA devices when libnvidia-ml resolved.
	// The proprietary driver exposes no sysfs power, so NVML is the only
	// source; the runtime-PM suspend gate still comes from sysfs and is
	// checked BEFORE any NVML call — NVML queries wake an RTD3-suspended
	// GPU, which would add the idle watts this tool exists to observe.
	NVMLDev nvml.Device

	energyJoules  float64
	lastSample    time.Time
	warnedNoPower bool
	warnedNoHWEng bool
}

// Sampler owns the loop and the metric vectors.
type Sampler struct {
	node     string
	interval time.Duration
	targets  []*Target

	power     *prometheus.GaugeVec
	component *prometheus.GaugeVec
	energy    *prometheus.GaugeVec
	hwEnergy  *prometheus.GaugeVec
	suspended *prometheus.GaugeVec
	scrapeDur prometheus.Gauge
}

// New builds a sampler over the given devices. Devices without any sensor
// still get a suspended/presence series so absence is visible, not silent.
func New(reg prometheus.Registerer, node string, interval time.Duration, sysRoot string, devices []probe.Device, nvmlLib nvml.Library) *Sampler {
	labels := []string{"node", "kind", "vendor_id", "device_id", "pci", "driver"}
	s := &Sampler{
		node:     node,
		interval: interval,
		power: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ergoz_accel_power_watts",
			Help: "Instantaneous accelerator power draw (board/socket scope) in watts, from vanilla-kernel sysfs.",
		}, labels),
		component: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ergoz_accel_component_power_watts",
			Help: "Decomposed component power in watts where the ASIC exposes it (e.g. APU gfx vs socket). Fields failing per-ASIC sanity validation are absent, never zero.",
		}, append(labels, "component")),
		energy: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ergoz_accel_energy_joules_total",
			Help: "Software-integrated energy (sum of watts x seconds) since agent start. Counter semantics; resets on agent restart.",
		}, labels),
		hwEnergy: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ergoz_accel_hw_energy_joules_total",
			Help: "Native hardware cumulative energy counter (joules) since driver load, where the device supports one (NVML Volta+). Absent when unsupported — never zero.",
		}, labels),
		suspended: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ergoz_accel_runtime_suspended",
			Help: "1 when the device is runtime-PM suspended (power reported as synthetic 0 W rather than waking the device), else 0.",
		}, labels),
		scrapeDur: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ergoz_agent_sample_duration_seconds",
			Help: "Wall time of the last full sampling pass.",
		}),
	}
	reg.MustRegister(s.power, s.component, s.energy, s.hwEnergy, s.suspended, s.scrapeDur)

	for _, d := range devices {
		if d.Kind != probe.KindGPU && d.Kind != probe.KindNPU {
			continue
		}
		t := &Target{
			Device: d,
			Sensor: hwmon.Discover(sysRoot, d.PCIAddr),
		}
		if d.PCIVendor == pciVendorNVIDIA && d.Driver == "nvidia" && nvmlLib != nil {
			dev, err := nvmlLib.DeviceByPCI(d.PCIAddr)
			if err != nil {
				log.Printf("nvml: could not resolve %s: %v — device stays present without a power series", d.PCIAddr, err)
			} else {
				t.NVMLDev = dev
			}
		}
		s.targets = append(s.targets, t)
	}
	return s
}

// Targets returns the devices under measurement (for startup logging).
func (s *Sampler) Targets() []*Target { return s.targets }

// Run samples until ctx is done.
func (s *Sampler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.sampleAll() // immediate first pass so /metrics is warm before the first tick
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleAll()
		}
	}
}

func (s *Sampler) sampleAll() {
	start := time.Now()
	for _, t := range s.targets {
		s.sampleOne(t, start)
	}
	s.scrapeDur.Set(time.Since(start).Seconds())
}

func (s *Sampler) sampleOne(t *Target, now time.Time) {
	l := prometheus.Labels{
		"node":      s.node,
		"kind":      string(t.Device.Kind),
		"vendor_id": t.Device.PCIVendor,
		"device_id": t.Device.PCIDevice,
		"pci":       t.Device.PCIAddr,
		"driver":    t.Device.Driver,
	}

	// D9: never wake a suspended device to measure it.
	if t.Sensor.RuntimeSuspended() {
		s.suspended.With(l).Set(1)
		s.power.With(l).Set(0)
		s.energy.With(l).Set(t.energyJoules) // energy holds, does not accrue
		t.lastSample = now
		return
	}
	s.suspended.With(l).Set(0)

	if t.NVMLDev != nil {
		s.sampleNVML(t, l, now)
		return
	}

	watts, err := t.Sensor.PowerWatts()
	if err == nil {
		s.power.With(l).Set(watts)
		if !t.lastSample.IsZero() {
			t.energyJoules += watts * now.Sub(t.lastSample).Seconds()
		}
		s.energy.With(l).Set(t.energyJoules)
	} else if !t.warnedNoPower {
		log.Printf("sample %s: no hwmon power (%v) — device stays present without a power series", t.Device.PCIAddr, err)
		t.warnedNoPower = true
	}
	t.lastSample = now

	blob, err := t.Sensor.GPUMetrics()
	if err != nil || !gpumetrics.Supported(blob) {
		return
	}
	m, err := gpumetrics.Decode(blob)
	if err != nil {
		log.Printf("sample %s: gpu_metrics decode failed: %v", t.Device.PCIAddr, err)
		return
	}
	setComponent := func(component string, v *float64) {
		if v == nil {
			// Absent, never zero: delete any stale series so a field that
			// stops validating disappears instead of freezing.
			cl := prometheus.Labels{"component": component}
			for k, val := range l {
				cl[k] = val
			}
			s.component.Delete(cl)
			return
		}
		cl := prometheus.Labels{"component": component}
		for k, val := range l {
			cl[k] = val
		}
		s.component.With(cl).Set(*v)
	}
	setComponent("socket", m.SocketPowerW)
	setComponent("gfx", m.GfxPowerW)
	setComponent("npu", m.NPUPowerW)
	setComponent("cpu_cores", m.CoreSumPowerW)
}

// sampleNVML reads an NVIDIA device. Board power feeds the same
// software-integrated energy as sysfs devices; the native cumulative
// counter (Volta+) is exported additionally when supported.
func (s *Sampler) sampleNVML(t *Target, l prometheus.Labels, now time.Time) {
	mw, err := t.NVMLDev.PowerMilliwatts()
	if err != nil {
		if !t.warnedNoPower {
			log.Printf("sample %s: nvml power failed (%v)", t.Device.PCIAddr, err)
			t.warnedNoPower = true
		}
		t.lastSample = now
		return
	}
	watts := float64(mw) / 1000.0
	s.power.With(l).Set(watts)
	if !t.lastSample.IsZero() {
		t.energyJoules += watts * now.Sub(t.lastSample).Seconds()
	}
	s.energy.With(l).Set(t.energyJoules)
	t.lastSample = now

	mj, err := t.NVMLDev.TotalEnergyMillijoules()
	switch {
	case err == nil:
		s.hwEnergy.With(l).Set(float64(mj) / 1000.0)
	case errors.Is(err, nvml.ErrNotSupported):
		// Absence, never zero: some consumer SKUs lack the counter.
		s.hwEnergy.Delete(l)
		if !t.warnedNoHWEng {
			log.Printf("sample %s: native energy counter unsupported — software integration only", t.Device.PCIAddr)
			t.warnedNoHWEng = true
		}
	default:
		if !t.warnedNoHWEng {
			log.Printf("sample %s: nvml energy counter failed (%v)", t.Device.PCIAddr, err)
			t.warnedNoHWEng = true
		}
	}
}
