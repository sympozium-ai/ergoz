package nvml

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// stubC is a synthetic libnvidia-ml implementing exactly the surface the
// binding uses, with recognizable values: 123456 mW power and an energy
// counter that advances 123456 mJ per read. Compiling and dlopen'ing it
// exercises the REAL purego path — symbol resolution, calling convention,
// string marshaling for the PCI address — everything except NVIDIA silicon.
const stubC = `
#include <string.h>
typedef int nvmlReturn_t;
static unsigned long long energy_mj = 5000000ULL;

int nvmlInit_v2(void) { return 0; }
int nvmlShutdown(void) { return 0; }

int nvmlDeviceGetHandleByPciBusId_v2(const char *id, void **handle) {
    if (strcmp(id, "0000:01:00.0") != 0) return 2; /* NVML_ERROR_INVALID_ARGUMENT */
    *handle = (void *)0xE60;
    return 0;
}

int nvmlDeviceGetPowerUsage(void *handle, unsigned int *mw) {
    if (handle != (void *)0xE60) return 2;
    *mw = 123456; /* 123.456 W */
    return 0;
}

int nvmlDeviceGetTotalEnergyConsumption(void *handle, unsigned long long *mj) {
    if (handle != (void *)0xE60) return 2;
    energy_mj += 123456ULL;
    *mj = energy_mj;
    return 0;
}
`

func buildStub(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not available; skipping synthetic NVML test")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "stub.c")
	lib := filepath.Join(dir, "libnvidia-ml.so.1")
	if err := os.WriteFile(src, []byte(stubC), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("gcc", "-shared", "-fPIC", "-o", lib, src).CombinedOutput()
	if err != nil {
		t.Fatalf("compiling stub: %v\n%s", err, out)
	}
	return lib
}

func TestLoad_SyntheticStub(t *testing.T) {
	t.Setenv("ERGOZ_NVML_PATH", buildStub(t))

	lib, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer lib.Shutdown()

	dev, err := lib.DeviceByPCI("0000:01:00.0")
	if err != nil {
		t.Fatalf("DeviceByPCI: %v", err)
	}

	mw, err := dev.PowerMilliwatts()
	if err != nil || mw != 123456 {
		t.Fatalf("power = %d mW, err %v; want 123456", mw, err)
	}

	e1, err := dev.TotalEnergyMillijoules()
	if err != nil {
		t.Fatalf("energy: %v", err)
	}
	e2, _ := dev.TotalEnergyMillijoules()
	if e2-e1 != 123456 {
		t.Fatalf("energy counter delta = %d, want 123456 (monotonic)", e2-e1)
	}

	// Wrong PCI address must fail through the real string-marshaling path.
	if _, err := lib.DeviceByPCI("0000:ff:00.0"); err == nil {
		t.Fatal("expected error for unknown PCI address")
	}
}

func TestLoad_UnavailableIsGraceful(t *testing.T) {
	t.Setenv("ERGOZ_NVML_PATH", "/nonexistent/libnvidia-ml.so.1")
	if _, err := Load(); err == nil {
		t.Fatal("expected ErrUnavailable for missing library")
	}
}
