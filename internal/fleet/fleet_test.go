package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"
	"time"
)

const agentPayload = `# HELP ergoz_accel_power_watts Instantaneous accelerator power draw.
# TYPE ergoz_accel_power_watts gauge
ergoz_accel_power_watts{node="n1",kind="gpu",vendor_id="1002",device_id="1586",pci="0000:c3:00.0",driver="amdgpu"} 20.072
# HELP ergoz_accel_component_power_watts Decomposed component power.
# TYPE ergoz_accel_component_power_watts gauge
ergoz_accel_component_power_watts{node="n1",kind="gpu",vendor_id="1002",device_id="1586",pci="0000:c3:00.0",driver="amdgpu",component="socket"} 20.724
# HELP ergoz_accel_energy_joules_total Software-integrated energy.
# TYPE ergoz_accel_energy_joules_total gauge
ergoz_accel_energy_joules_total{node="n1",kind="gpu",vendor_id="1002",device_id="1586",pci="0000:c3:00.0",driver="amdgpu"} 51.19
# HELP ergoz_accel_runtime_suspended Suspend marker.
# TYPE ergoz_accel_runtime_suspended gauge
ergoz_accel_runtime_suspended{node="n1",kind="npu",vendor_id="1022",device_id="17f0",pci="0000:c4:00.1",driver="amdxdna"} 1
`

// TestScrapeEndToEnd is the regression for the expfmt zero-value parser
// panic: it exercises the full scrape → parse → snapshot → re-encode path
// against a real HTTP server serving agent-format metrics.
func TestScrapeEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(agentPayload))
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	c := New(func(context.Context) ([]string, error) { return []string{addr}, nil }, time.Minute)
	c.scrapeAll(context.Background())

	c.mu.RLock()
	snap := c.snapshot
	c.mu.RUnlock()

	if snap.AgentsUp != 1 || snap.AgentsTotal != 1 {
		t.Fatalf("agents up/total = %d/%d, want 1/1", snap.AgentsUp, snap.AgentsTotal)
	}
	if len(snap.Devices) != 2 {
		t.Fatalf("devices = %d, want 2 (gpu + npu)", len(snap.Devices))
	}
	if snap.TotalWatts != 20.072 {
		t.Fatalf("totalWatts = %v, want 20.072", snap.TotalWatts)
	}
	var gpu, npu *DevicePower
	for i := range snap.Devices {
		switch snap.Devices[i].Kind {
		case "gpu":
			gpu = &snap.Devices[i]
		case "npu":
			npu = &snap.Devices[i]
		}
	}
	if gpu == nil || gpu.Components["socket"] != 20.724 || gpu.EnergyJ != 51.19 {
		t.Fatalf("gpu = %+v", gpu)
	}
	if npu == nil || !npu.Suspended {
		t.Fatalf("npu = %+v, want suspended", npu)
	}

	// Merged re-encode must round-trip the families.
	rec := httptest.NewRecorder()
	c.ServeMetrics(rec, nil)
	body := rec.Body.String()
	for _, want := range []string{"ergoz_accel_power_watts", `pci="0000:c3:00.0"`, "20.072"} {
		if !strings.Contains(body, want) {
			t.Fatalf("merged metrics missing %q:\n%s", want, body)
		}
	}

	// JSON API round-trip.
	rec = httptest.NewRecorder()
	c.ServeFleet(rec, nil)
	dump, _ := httputil.DumpResponse(rec.Result(), true)
	if !strings.Contains(string(dump), `"totalWatts":20.072`) {
		t.Fatalf("fleet JSON missing total: %s", dump)
	}
}
