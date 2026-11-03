// Package discover turns the shared llmfit-dra probe walk into the set of
// accelerators the agent measures. It adds two Ergoz-side corrections on
// top of probe's DRM/accel class walk:
//
//  1. PCI supplement. probe finds GPUs through /sys/class/drm, but headless
//     datacenter NVIDIA GPUs (T4, A10G, L4, ... on stock cloud GPU images)
//     have no DRM card unless the optional nvidia_drm module is loaded. Those
//     devices are found here by walking /sys/bus/pci/devices for
//     display-class functions bound to an accelerator driver, deduplicated
//     against the DRM walk by PCI address.
//  2. Firmware framebuffers. Generic boot framebuffers (simple-framebuffer,
//     simpledrm, efifb, ...) register a DRM card whose sysfs parent is the
//     PCI VGA function they borrowed, so probe reports them as a "gpu" with
//     that PCI address but no vendor/device id. They are not accelerators
//     and have no power source, so they are dropped.
package discover

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sympozium-ai/llmfit-dra/pkg/probe"
)

// firmwareFramebufferDrivers are generic, firmware-provided display drivers.
// A DRM card bound to one of these is a boot console, never an accelerator.
var firmwareFramebufferDrivers = map[string]bool{
	"simple-framebuffer": true,
	"simpledrm":          true,
	"efifb":              true,
	"vesafb":             true,
	"vesadrm":            true,
	"efidrm":             true,
	"ofdrm":              true,
	"offb":               true,
}

// pciAcceleratorDrivers are drivers whose display-class PCI functions are
// measurable accelerators even without a DRM card. Only NVIDIA's proprietary
// driver is known to omit the DRM card in practice (nvidia_drm is a separate,
// optional module); the others are listed so a DRM-less configuration still
// surfaces the device as present.
var pciAcceleratorDrivers = map[string]bool{
	"nvidia": true,
	"amdgpu": true,
	"xe":     true,
	"i915":   true,
}

// Devices walks sysRoot/procRoot and returns the devices to measure.
func Devices(sysRoot, procRoot string) ([]probe.Device, error) {
	devices, err := probe.New(sysRoot, procRoot).Walk()
	if err != nil {
		return nil, err
	}
	return Refine(sysRoot, devices), nil
}

// Refine applies the framebuffer filter and the PCI supplement to a probe
// walk result. Non-GPU devices pass through unchanged.
func Refine(sysRoot string, devices []probe.Device) []probe.Device {
	out := make([]probe.Device, 0, len(devices))
	seen := map[string]bool{}
	gpuIdx := 0
	for _, d := range devices {
		if d.Kind == probe.KindGPU {
			if isFirmwareFramebuffer(d) {
				log.Printf("discover: skipping firmware framebuffer %q at %s (not an accelerator)", d.Driver, d.PCIAddr)
				continue
			}
			if d.PCIAddr != "" && seen[d.PCIAddr] {
				continue // second DRM card on the same function
			}
			if d.Index >= gpuIdx {
				gpuIdx = d.Index + 1
			}
		}
		if d.PCIAddr != "" && (d.Kind == probe.KindGPU || d.Kind == probe.KindNPU || d.Kind == probe.KindNIC) {
			seen[d.PCIAddr] = true
		}
		out = append(out, d)
	}

	for _, d := range pciAccelerators(sysRoot) {
		if seen[d.PCIAddr] {
			continue
		}
		d.Index = gpuIdx
		gpuIdx++
		seen[d.PCIAddr] = true
		log.Printf("discover: %s (vendor %s device %s driver %s) has no DRM card; found via PCI bus", d.PCIAddr, d.PCIVendor, d.PCIDevice, d.Driver)
		out = append(out, d)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Index < out[j].Index
	})
	return out
}

func isFirmwareFramebuffer(d probe.Device) bool {
	if firmwareFramebufferDrivers[d.Driver] {
		return true
	}
	// A DRM card whose device dir has no PCI vendor id is not itself a PCI
	// function (platform device parented under one). Real accelerators always
	// carry a vendor id.
	return d.PCIVendor == "" && d.PCIDevice == ""
}

// pciAccelerators lists display-class (0x03xxxx) PCI functions bound to an
// accelerator driver, sorted by address.
func pciAccelerators(sysRoot string) []probe.Device {
	dir := filepath.Join(sysRoot, "bus", "pci", "devices")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []probe.Device
	for _, e := range entries {
		addr := e.Name()
		devDir := filepath.Join(dir, addr)
		if !strings.HasPrefix(readHex(filepath.Join(devDir, "class")), "03") {
			continue
		}
		driver := linkBase(filepath.Join(devDir, "driver"))
		if !pciAcceleratorDrivers[driver] {
			continue
		}
		d := probe.Device{
			Kind:      probe.KindGPU,
			PCIVendor: readHex(filepath.Join(devDir, "vendor")),
			PCIDevice: readHex(filepath.Join(devDir, "device")),
			PCIAddr:   addr,
			PCIeRoot:  pcieRoot(devDir),
			Driver:    driver,
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PCIAddr < out[j].PCIAddr })
	return out
}

func readHex(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(b)), "0x")
}

func linkBase(path string) string {
	t, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return filepath.Base(t)
}

func pcieRoot(devDir string) string {
	resolved, err := filepath.EvalSymlinks(devDir)
	if err != nil {
		return ""
	}
	root := ""
	for _, part := range strings.Split(resolved, string(filepath.Separator)) {
		if strings.HasPrefix(part, "pci") && strings.Contains(part, ":") {
			root = part
		}
	}
	return root
}
