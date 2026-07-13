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
// device against the fake NVML library: current board power via NVML.
func TestNVIDIA_SyntheticSampling(t *testing.T) {
	fakeDev := &nvml.FakeDevice{MW: 123456}
	fake := &nvml.Fake{Devices: map[string]*nvml.FakeDevice{"0000:01:00.0": fakeDev}}

	reg := prometheus.NewRegistry()
	// sysRoot is an empty temp dir: no hwmon, no runtime_status → active.
	s := New(reg, "test-node", time.Second, t.TempDir(), []probe.Device{nvidiaDevice()}, fake)

	if len(s.targets) != 1 || s.targets[0].NVMLDev == nil {
		t.Fatalf("expected 1 NVML-backed target, got %+v", s.targets)
	}
	l := labelsFor("test-node", nvidiaDevice())

	for _, tgt := range s.targets {
		s.sampleOne(tgt, time.Now())
	}
	if got := testutil.ToFloat64(s.power.With(l)); got != 123.456 {
		t.Fatalf("power = %v W, want 123.456", got)
	}

	// Reading tracks the current value on the next sample.
	fakeDev.MW = 90_000
	for _, tgt := range s.targets {
		s.sampleOne(tgt, time.Now())
	}
	if got := testutil.ToFloat64(s.power.With(l)); got != 90 {
		t.Fatalf("power after change = %v W, want 90", got)
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

// TestSuspend_ClearsComponentSeries: when a device goes suspended, any
// previously-exported component series must be deleted, not frozen at their
// last active value ("absent, never stale").
func TestSuspend_ClearsComponentSeries(t *testing.T) {
	sysRoot := t.TempDir()
	reg := prometheus.NewRegistry()
	s := New(reg, "test-node", time.Second, sysRoot, []probe.Device{nvidiaDevice()}, nil)
	l := labelsFor("test-node", nvidiaDevice())

	// Seed an active component reading directly, as a prior active pass would.
	cl := prometheus.Labels{"component": "gfx"}
	for k, v := range l {
		cl[k] = v
	}
	s.component.With(cl).Set(22.4)
	if testutil.CollectAndCount(s.component) != 1 {
		t.Fatal("setup: expected one component series")
	}

	// Now mark suspended and sample: the component series must be gone.
	writeRuntimeStatus(t, sysRoot, "0000:01:00.0", "suspended")
	for _, tgt := range s.targets {
		s.sampleOne(tgt, time.Now())
	}
	if got := testutil.CollectAndCount(s.component); got != 0 {
		t.Fatalf("component series count = %d after suspend, want 0 (stale gfx must be dropped)", got)
	}
	if got := testutil.ToFloat64(s.suspended.With(l)); got != 1 {
		t.Fatalf("suspended = %v, want 1", got)
	}
}

type panicDevice struct{}

func (panicDevice) PowerMilliwatts() (uint32, error) {
	panic("NVML queried for a runtime-suspended device — the suspend gate failed")
}
