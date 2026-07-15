package fleet

import (
	"context"
	"errors"
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
# HELP ergoz_accel_runtime_suspended Suspend marker.
# TYPE ergoz_accel_runtime_suspended gauge
ergoz_accel_runtime_suspended{node="n1",kind="npu",vendor_id="1022",device_id="17f0",pci="0000:c4:00.1",driver="amdxdna"} 1
`

// TestStaleMarking: when an agent stops responding, its device stays
// visible but flagged stale and drops out of TotalWatts — it does not vanish.
func TestStaleMarking(t *testing.T) {
	up := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !up {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(agentPayload))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	// 1ms interval → staleAfter clamps to 5s; force staleness by rewinding
	// the recorded lastOK instead of sleeping.
	c := New(func(context.Context) ([]string, error) { return []string{addr}, nil }, time.Millisecond)
	c.scrapeAll(context.Background())
	if c.snapshot.TotalWatts != 20.072 || c.snapshot.StaleDevices != 0 {
		t.Fatalf("fresh: total=%v stale=%d", c.snapshot.TotalWatts, c.snapshot.StaleDevices)
	}

	// Agent goes down; age its last-good time past the stale threshold.
	up = false
	c.mu.Lock()
	c.agents[addr].lastOK = time.Now().Add(-time.Hour)
	c.mu.Unlock()
	c.scrapeAll(context.Background())

	if len(c.snapshot.Devices) != 2 {
		t.Fatalf("stale devices vanished: got %d, want 2 still visible", len(c.snapshot.Devices))
	}
	if c.snapshot.StaleDevices != 2 {
		t.Fatalf("staleDevices = %d, want 2", c.snapshot.StaleDevices)
	}
	if c.snapshot.TotalWatts != 0 {
		t.Fatalf("stale watts must be excluded from total, got %v", c.snapshot.TotalWatts)
	}
	if c.snapshot.AgentsUp != 0 || c.snapshot.AgentsTotal != 1 {
		t.Fatalf("agents up/total = %d/%d, want 0/1", c.snapshot.AgentsUp, c.snapshot.AgentsTotal)
	}
	for _, d := range c.snapshot.Devices {
		if !d.Stale {
			t.Fatalf("device %s not marked stale", d.PCI)
		}
	}
}

// TestDiscoveryFailureAgesReadings: when discovery itself fails (a headless
// Service with no ready endpoints resolves to NXDOMAIN), the last good
// snapshot must not keep being served as current — readings age to stale.
func TestDiscoveryFailureAgesReadings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(agentPayload))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	discoveryOK := true
	c := New(func(context.Context) ([]string, error) {
		if !discoveryOK {
			return nil, errors.New("lookup ergoz-agent: no such host")
		}
		return []string{addr}, nil
	}, time.Millisecond)

	c.scrapeAll(context.Background())
	if c.snapshot.TotalWatts != 20.072 {
		t.Fatalf("fresh total = %v, want 20.072", c.snapshot.TotalWatts)
	}

	// Discovery breaks and the reading ages past the stale threshold.
	discoveryOK = false
	c.mu.Lock()
	c.agents[addr].lastOK = time.Now().Add(-time.Hour)
	c.mu.Unlock()
	c.scrapeAll(context.Background())

	if c.snapshot.StaleDevices != 2 || c.snapshot.TotalWatts != 0 {
		t.Fatalf("discovery outage served as current: stale=%d total=%v, want 2/0",
			c.snapshot.StaleDevices, c.snapshot.TotalWatts)
	}
	if len(c.snapshot.Devices) != 2 {
		t.Fatalf("devices vanished: got %d, want 2 still visible", len(c.snapshot.Devices))
	}
	if c.snapshot.AgentsUp != 0 || c.snapshot.AgentsTotal != 0 {
		t.Fatalf("agents up/total = %d/%d, want 0/0", c.snapshot.AgentsUp, c.snapshot.AgentsTotal)
	}

	// Discovery recovers: the device is current again, not stuck stale.
	discoveryOK = true
	c.scrapeAll(context.Background())
	if c.snapshot.TotalWatts != 20.072 || c.snapshot.StaleDevices != 0 {
		t.Fatalf("after recovery: total=%v stale=%d, want 20.072/0",
			c.snapshot.TotalWatts, c.snapshot.StaleDevices)
	}
}

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
	if gpu == nil || gpu.Components["socket"] != 20.724 {
		t.Fatalf("gpu = %+v", gpu)
	}
	if gpu.Stale {
		t.Fatal("fresh scrape must not be stale")
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
