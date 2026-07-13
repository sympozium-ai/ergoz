package sampler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/sympozium-ai/ergoz/internal/nvml"
	"github.com/sympozium-ai/llmfit-dra/pkg/probe"
)

func nvidiaDevice() probe.Device {
	return probe.Device{
		Kind: probe.KindGPU, PCIVendor: "10de", PCIDevice: "2684",
		PCIAddr: "0000:01:00.0", Driver: "nvidia",
	}
}

func labelsFor(node string, d probe.Device) prometheus.Labels {
	return prometheus.Labels{
		"node": node, "kind": string(d.Kind), "vendor_id": d.PCIVendor,
		"device_id": d.PCIDevice, "pci": d.PCIAddr, "driver": d.Driver,
	}
}

// TestNVIDIA_SyntheticSampling drives the full sampler path for an NVIDIA
// device against the fake NVML library: board power, software energy
// integration between samples, and the native hardware counter.
func TestNVIDIA_SyntheticSampling(t *testing.T) {
	fakeDev := &nvml.FakeDevice{MW: 123456, MJ: 5_000_000}
	fake := &nvml.Fake{Devices: map[string]*nvml.FakeDevice{"0000:01:00.0": fakeDev}}

	reg := prometheus.NewRegistry()
	// sysRoot is an empty temp dir: no hwmon, no runtime_status → active.
	s := New(reg, "test-node", time.Second, t.TempDir(), []probe.Device{nvidiaDevice()}, fake)

	if len(s.targets) != 1 || s.targets[0].NVMLDev == nil {
		t.Fatalf("expected 1 NVML-backed target, got %+v", s.targets)
	}
	l := labelsFor("test-node", nvidiaDevice())

	t0 := time.Now()
	s.targets[0].lastSample = t0.Add(-2 * time.Second) // simulate a prior sample 2s ago
	for _, tgt := range s.targets {
		s.sampleOne(tgt, t0)
	}

	if got := testutil.ToFloat64(s.power.With(l)); got != 123.456 {
		t.Fatalf("power = %v W, want 123.456", got)
	}
	// 123.456 W for 2 s = 246.912 J software-integrated.
	if got := testutil.ToFloat64(s.energy.With(l)); got < 246.9 || got > 246.92 {
		t.Fatalf("software energy = %v J, want ~246.912", got)
	}
	if got := testutil.ToFloat64(s.hwEnergy.With(l)); got != 5000 {
		t.Fatalf("hw energy = %v J, want 5000 (5e6 mJ)", got)
	}

	// Counter advances → gauge follows the hardware value.
	fakeDev.MJ = 6_234_000
	for _, tgt := range s.targets {
		s.sampleOne(tgt, t0.Add(time.Second))
	}
	if got := testutil.ToFloat64(s.hwEnergy.With(l)); got != 6234 {
		t.Fatalf("hw energy after advance = %v J, want 6234", got)
	}
}

// TestNVIDIA_EnergyCounterUnsupported: consumer SKUs without the Volta+
// counter must produce NO hw-energy series — absence, never zero.
func TestNVIDIA_EnergyCounterUnsupported(t *testing.T) {
	fake := &nvml.Fake{Devices: map[string]*nvml.FakeDevice{
		"0000:01:00.0": {MW: 50_000, EnergyErr: nvml.ErrNotSupported},
	}}
	reg := prometheus.NewRegistry()
	s := New(reg, "test-node", time.Second, t.TempDir(), []probe.Device{nvidiaDevice()}, fake)

	for _, tgt := range s.targets {
		s.sampleOne(tgt, time.Now())
	}

	if got := testutil.CollectAndCount(s.hwEnergy); got != 0 {
		t.Fatalf("hw energy series count = %d, want 0 for unsupported counter", got)
	}
	if got := testutil.ToFloat64(s.power.With(labelsFor("test-node", nvidiaDevice()))); got != 50 {
		t.Fatalf("power = %v W, want 50", got)
	}
}

// TestNVIDIA_SuspendGateBlocksNVML: a runtime-suspended GPU must never be
// queried through NVML — the query itself would wake an RTD3-suspended
// device, adding the idle watts this tool exists to observe. The panicking
// device is the tripwire.
func TestNVIDIA_SuspendGateBlocksNVML(t *testing.T) {
	sysRoot := t.TempDir()
	writeRuntimeStatus(t, sysRoot, "0000:01:00.0", "suspended")

	reg := prometheus.NewRegistry()
	s := New(reg, "test-node", time.Second, sysRoot, []probe.Device{nvidiaDevice()}, panicLib{})

	for _, tgt := range s.targets {
		s.sampleOne(tgt, time.Now()) // must not touch NVML
	}
	l := labelsFor("test-node", nvidiaDevice())
	if got := testutil.ToFloat64(s.suspended.With(l)); got != 1 {
		t.Fatalf("suspended = %v, want 1", got)
	}
	if got := testutil.ToFloat64(s.power.With(l)); got != 0 {
		t.Fatalf("power = %v, want synthetic 0 for suspended device", got)
	}
}

func writeRuntimeStatus(t *testing.T, sysRoot, pci, status string) {
	t.Helper()
	dir := filepath.Join(sysRoot, "bus", "pci", "devices", pci, "power")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime_status"), []byte(status+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// panicLib hands out devices that panic on any query.
type panicLib struct{}

func (panicLib) DeviceByPCI(string) (nvml.Device, error) { return panicDevice{}, nil }
func (panicLib) Shutdown()                               {}

type panicDevice struct{}

func (panicDevice) PowerMilliwatts() (uint32, error) {
	panic("NVML queried for a runtime-suspended device — the suspend gate failed")
}
func (panicDevice) TotalEnergyMillijoules() (uint64, error) {
	panic("NVML queried for a runtime-suspended device — the suspend gate failed")
}
