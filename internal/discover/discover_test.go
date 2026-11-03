package discover

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sympozium-ai/llmfit-dra/pkg/probe"
)

// fakeSys builds a minimal sysfs + procfs tree shaped like the real kernel's:
// devices live under devices/pci0000:00/<addr>, bus/pci/devices/<addr> and
// class/drm/cardN/device are symlinks into it, and driver is a symlink to
// bus/pci/drivers/<name>.
type fakeSys struct {
	t    *testing.T
	root string
}

func newFakeSys(t *testing.T) *fakeSys {
	t.Helper()
	root := t.TempDir()
	f := &fakeSys{t: t, root: root}
	f.write("proc/meminfo", "MemTotal:       16384000 kB\n")
	f.write("proc/cpuinfo", "model name\t: Test CPU\n")
	f.mkdir("sys/bus/pci/devices")
	return f
}

func (f *fakeSys) sys() string  { return filepath.Join(f.root, "sys") }
func (f *fakeSys) proc() string { return filepath.Join(f.root, "proc") }

func (f *fakeSys) mkdir(rel string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.root, rel), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSys) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeSys) symlink(target, rel string) {
	f.t.Helper()
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		f.t.Fatal(err)
	}
}

// pciFn adds a PCI function and its bus/pci/devices link.
func (f *fakeSys) pciFn(addr, vendor, device, class, driver string) string {
	dev := "sys/devices/pci0000:00/" + addr
	f.write(dev+"/vendor", "0x"+vendor+"\n")
	f.write(dev+"/device", "0x"+device+"\n")
	f.write(dev+"/class", "0x"+class+"\n")
	if driver != "" {
		f.mkdir("sys/bus/pci/drivers/" + driver)
		f.symlink(filepath.Join(f.sys(), "bus/pci/drivers", driver), dev+"/driver")
	}
	f.symlink(filepath.Join(f.root, dev), "sys/bus/pci/devices/"+addr)
	return dev
}

// drmCard adds class/drm/<card> whose device link points at devRel.
func (f *fakeSys) drmCard(card, devRel string) {
	f.mkdir("sys/class/drm/" + card)
	f.symlink(filepath.Join(f.root, devRel), "sys/class/drm/"+card+"/device")
}

func gpus(ds []probe.Device) []probe.Device {
	var out []probe.Device
	for _, d := range ds {
		if d.Kind == probe.KindGPU {
			out = append(out, d)
		}
	}
	return out
}

// AWS g6 shape: L4 bound to nvidia with no nvidia_drm (no DRM card), plus the
// Amazon VGA function carrying a simple-framebuffer DRM card.
func TestNVIDIAWithoutDRM(t *testing.T) {
	f := newFakeSys(t)
	vga := f.pciFn("0000:00:03.0", "1d0f", "1111", "030000", "")
	f.write(vga+"/simple-framebuffer.0/.keep", "")
	f.mkdir("sys/bus/platform/drivers/simple-framebuffer")
	f.symlink(filepath.Join(f.sys(), "bus/platform/drivers/simple-framebuffer"), vga+"/simple-framebuffer.0/driver")
	f.drmCard("card0", vga+"/simple-framebuffer.0")
	f.pciFn("0000:31:00.0", "10de", "27b8", "030200", "nvidia")

	ds, err := Devices(f.sys(), f.proc())
	if err != nil {
		t.Fatal(err)
	}
	g := gpus(ds)
	if len(g) != 1 {
		t.Fatalf("want exactly the L4, got %+v", g)
	}
	d := g[0]
	if d.PCIAddr != "0000:31:00.0" || d.PCIVendor != "10de" || d.PCIDevice != "27b8" || d.Driver != "nvidia" {
		t.Fatalf("unexpected device %+v", d)
	}
	if d.PCIeRoot != "pci0000:00" {
		t.Fatalf("PCIeRoot = %q", d.PCIeRoot)
	}
}

// With nvidia_drm loaded the GPU has a DRM card too: report it once.
func TestNVIDIAWithDRMNoDuplicate(t *testing.T) {
	f := newFakeSys(t)
	dev := f.pciFn("0000:00:1e.0", "10de", "1eb8", "030200", "nvidia")
	f.drmCard("card1", dev)

	ds, err := Devices(f.sys(), f.proc())
	if err != nil {
		t.Fatal(err)
	}
	g := gpus(ds)
	if len(g) != 1 || g[0].PCIAddr != "0000:00:1e.0" || g[0].DevNode != "/dev/dri/card1" {
		t.Fatalf("want one DRM-discovered T4, got %+v", g)
	}
}

// Framework Strix Halo shape: amdgpu APU with a DRM card, XDNA NPU on the
// accel class. Must be identical to the plain probe walk.
func TestAMDGPUAPUUnchanged(t *testing.T) {
	f := newFakeSys(t)
	gpu := f.pciFn("0000:c5:00.0", "1002", "1586", "030000", "amdgpu")
	f.drmCard("card1", gpu)
	npu := f.pciFn("0000:c6:00.1", "1022", "17f0", "118000", "amdxdna")
	f.mkdir("sys/class/accel/accel0")
	f.symlink(filepath.Join(f.root, npu), "sys/class/accel/accel0/device")

	raw, err := probe.New(f.sys(), f.proc()).Walk()
	if err != nil {
		t.Fatal(err)
	}
	ds, err := Devices(f.sys(), f.proc())
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != len(raw) {
		t.Fatalf("refine changed device count: raw %d, refined %d (%+v)", len(raw), len(ds), ds)
	}
	for i := range raw {
		if raw[i] != ds[i] {
			t.Fatalf("device %d changed:\n raw %+v\n got %+v", i, raw[i], ds[i])
		}
	}
	g := gpus(ds)
	if len(g) != 1 || g[0].Driver != "amdgpu" || g[0].PCIVendor != "1002" {
		t.Fatalf("amdgpu APU missing: %+v", g)
	}
}

// A simpledrm card parented under a real GPU's PCI function (before the
// native driver takes over) must not shadow or duplicate the GPU.
func TestFramebufferOnRealGPUFunction(t *testing.T) {
	f := newFakeSys(t)
	gpu := f.pciFn("0000:c5:00.0", "1002", "1586", "030000", "amdgpu")
	f.write(gpu+"/simple-framebuffer.0/.keep", "")
	f.mkdir("sys/bus/platform/drivers/simple-framebuffer")
	f.symlink(filepath.Join(f.sys(), "bus/platform/drivers/simple-framebuffer"), gpu+"/simple-framebuffer.0/driver")
	f.drmCard("card0", gpu+"/simple-framebuffer.0")
	f.drmCard("card1", gpu)

	ds, err := Devices(f.sys(), f.proc())
	if err != nil {
		t.Fatal(err)
	}
	g := gpus(ds)
	if len(g) != 1 || g[0].Driver != "amdgpu" || g[0].DevNode != "/dev/dri/card1" {
		t.Fatalf("want only the amdgpu card, got %+v", g)
	}
}

// Display functions with no accelerator driver (e.g. the bare Amazon VGA
// device, or an unbound GPU) are not added from the PCI walk.
func TestPCIWalkIgnoresNonAcceleratorDrivers(t *testing.T) {
	f := newFakeSys(t)
	f.pciFn("0000:00:03.0", "1d0f", "1111", "030000", "")
	f.pciFn("0000:00:04.0", "10de", "2237", "030200", "vfio-pci")
	f.pciFn("0000:00:05.0", "8086", "1234", "020000", "e1000e") // NIC class

	ds, err := Devices(f.sys(), f.proc())
	if err != nil {
		t.Fatal(err)
	}
	if g := gpus(ds); len(g) != 0 {
		t.Fatalf("want no GPUs, got %+v", g)
	}
}

// Multiple DRM-less NVIDIA GPUs get distinct, stable indices.
func TestMultipleNVIDIAIndices(t *testing.T) {
	f := newFakeSys(t)
	f.pciFn("0000:00:1f.0", "10de", "1eb8", "030200", "nvidia")
	f.pciFn("0000:00:1e.0", "10de", "1eb8", "030200", "nvidia")

	ds, err := Devices(f.sys(), f.proc())
	if err != nil {
		t.Fatal(err)
	}
	g := gpus(ds)
	if len(g) != 2 || g[0].PCIAddr != "0000:00:1e.0" || g[0].Index != 0 || g[1].PCIAddr != "0000:00:1f.0" || g[1].Index != 1 {
		t.Fatalf("unexpected ordering/indices: %+v", g)
	}
}
