// Package nvml reads NVIDIA GPU power through libnvidia-ml.so.1, loaded at
// RUNTIME via dlopen (purego) — no cgo, so the static CGO_ENABLED=0 build is
// preserved and nodes without the NVIDIA driver simply report the library as
// unavailable. libnvidia-ml ships with every driver install, and the power
// queries used here are not datacenter-gated.
//
// The binding is deliberately tiny: init/shutdown, handle-by-PCI-address,
// and instantaneous power (mW) — Ergoz reports current watts, nothing else.
package nvml

import (
	"errors"
	"fmt"
	"os"
)

// Return codes (subset of nvmlReturn_t).
const (
	retSuccess      = 0
	retNotSupported = 3
)

// ErrUnavailable means libnvidia-ml could not be loaded on this node.
var ErrUnavailable = errors.New("libnvidia-ml unavailable")

// ErrNotSupported means the device/driver does not support the query.
var ErrNotSupported = errors.New("not supported by device/driver")

// Device is one NVIDIA GPU under NVML.
type Device interface {
	// PowerMilliwatts is the current board power draw (±5% per NVML docs).
	PowerMilliwatts() (uint32, error)
}

// Library is the loaded NVML runtime.
type Library interface {
	// DeviceByPCI resolves a device by PCI address ("0000:01:00.0").
	DeviceByPCI(addr string) (Device, error)
	// Shutdown releases the library.
	Shutdown()
}

// realLib backs Library with dlopen'd libnvidia-ml.
type realLib struct {
	shutdown    func() int32
	handleByPCI func(string, *uintptr) int32
	powerUsage  func(uintptr, *uint32) int32
}

type realDevice struct {
	lib    *realLib
	handle uintptr
}

// Load dlopens libnvidia-ml and initializes NVML. Returns ErrUnavailable
// (wrapped) when the library is absent — the caller logs and moves on.
// ERGOZ_NVML_PATH overrides the library path (also how the synthetic test
// stub is injected).
func Load() (Library, error) {
	names := []string{"libnvidia-ml.so.1", "libnvidia-ml.so"}
	if p := os.Getenv("ERGOZ_NVML_PATH"); p != "" {
		names = []string{p}
	}

	var handle uintptr
	var err error
	for _, name := range names {
		if handle, err = dlopen(name); err == nil {
			break
		}
	}
	if handle == 0 {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	// register (purego RegisterLibFunc) panics if a symbol is missing — a
	// driver with an unexpected libnvidia-ml must degrade to "unavailable",
	// never crash-loop the agent and take AMD/sysfs sampling down with it.
	l := &realLib{}
	if err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%w: resolving NVML symbols: %v", ErrUnavailable, r)
			}
		}()
		var initFn func() int32
		register(&initFn, handle, "nvmlInit_v2")
		register(&l.shutdown, handle, "nvmlShutdown")
		register(&l.handleByPCI, handle, "nvmlDeviceGetHandleByPciBusId_v2")
		register(&l.powerUsage, handle, "nvmlDeviceGetPowerUsage")
		if rc := initFn(); rc != retSuccess {
			return fmt.Errorf("nvmlInit_v2 failed: code %d", rc)
		}
		return nil
	}(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *realLib) DeviceByPCI(addr string) (Device, error) {
	var h uintptr
	if rc := l.handleByPCI(addr, &h); rc != retSuccess {
		return nil, fmt.Errorf("nvmlDeviceGetHandleByPciBusId_v2(%s): code %d", addr, rc)
	}
	return &realDevice{lib: l, handle: h}, nil
}

func (l *realLib) Shutdown() {
	if l.shutdown != nil {
		l.shutdown()
	}
}

func (d *realDevice) PowerMilliwatts() (uint32, error) {
	var mw uint32
	switch rc := d.lib.powerUsage(d.handle, &mw); rc {
	case retSuccess:
		return mw, nil
	case retNotSupported:
		return 0, ErrNotSupported
	default:
		return 0, fmt.Errorf("nvmlDeviceGetPowerUsage: code %d", rc)
	}
}
