// Package hwmon reads accelerator power sensors from sysfs for a device
// identified by PCI address. Everything here works on a vanilla kernel with
// world-readable files — no vendor userspace, no root (the DaemonSet still
// runs privileged-capable per project decision D3, but this package never
// requires it).
package hwmon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Sensor is the sysfs power view of one PCI accelerator.
type Sensor struct {
	// PCIDir is <sysRoot>/bus/pci/devices/<addr>.
	PCIDir string
	// PowerFile is the hwmon instantaneous/averaged power file (µW), empty
	// when the driver exposes none.
	PowerFile string
	// GPUMetricsFile is the amdgpu binary metrics blob, empty when absent.
	GPUMetricsFile string
	// EnergyFile is a cumulative energy counter (µJ; xe/i915), empty when
	// absent.
	EnergyFile string
}

// Discover locates the sensor files for a PCI device. A Sensor with no
// files is returned (not an error) when the device exposes nothing — the
// caller reports the device as present-but-unmeasurable.
func Discover(sysRoot, pciAddr string) Sensor {
	dir := filepath.Join(sysRoot, "bus", "pci", "devices", pciAddr)
	s := Sensor{PCIDir: dir}

	entries, _ := filepath.Glob(filepath.Join(dir, "hwmon", "hwmon*"))
	for _, h := range entries {
		// power1_input is the instantaneous reading; power1_average the
		// SMU-filtered one. Presence varies by ASIC — take input first.
		for _, f := range []string{"power1_input", "power1_average"} {
			p := filepath.Join(h, f)
			if s.PowerFile == "" && readable(p) {
				s.PowerFile = p
			}
		}
		p := filepath.Join(h, "energy1_input")
		if s.EnergyFile == "" && readable(p) {
			s.EnergyFile = p
		}
	}

	if p := filepath.Join(dir, "gpu_metrics"); readable(p) {
		s.GPUMetricsFile = p
	}
	return s
}

// RuntimeSuspended reports whether the device is runtime-PM suspended.
// Per project decision D9, suspended devices are reported as synthetic 0 W
// with a marker — sampling them would wake the device and add the idle
// watts this tool exists to observe.
func (s Sensor) RuntimeSuspended() bool {
	b, err := os.ReadFile(filepath.Join(s.PCIDir, "power", "runtime_status"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "suspended"
}

// PowerWatts reads the hwmon power file (µW → W).
func (s Sensor) PowerWatts() (float64, error) {
	if s.PowerFile == "" {
		return 0, fmt.Errorf("no hwmon power file")
	}
	v, err := readInt(s.PowerFile)
	if err != nil {
		return 0, err
	}
	return float64(v) / 1e6, nil
}

// GPUMetrics reads the raw gpu_metrics blob.
func (s Sensor) GPUMetrics() ([]byte, error) {
	if s.GPUMetricsFile == "" {
		return nil, fmt.Errorf("no gpu_metrics file")
	}
	return os.ReadFile(s.GPUMetricsFile)
}

func readable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func readInt(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(string(bytes.TrimSpace(b)), 10, 64)
}
