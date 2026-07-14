package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/sympozium-ai/ergoz/internal/fleet"
)

func sampleSnapshot() fleet.Snapshot {
	return fleet.Snapshot{
		ScrapedAt:   time.Date(2026, 7, 14, 9, 30, 0, 0, time.UTC),
		AgentsTotal: 1,
		AgentsUp:    1,
		TotalWatts:  18.0,
		Devices: []fleet.DevicePower{
			{Node: "node-1", Kind: "gpu", PCI: "0000:c3:00.0", VendorID: "1002", DeviceID: "1586",
				Driver: "amdgpu", PowerWatts: 18.0, Components: map[string]float64{"socket": 18.1, "gfx": 0.0}},
			{Node: "node-1", Kind: "npu", PCI: "0000:c4:00.1", VendorID: "1022", DeviceID: "17f0",
				Driver: "amdxdna", Suspended: true},
		},
	}
}

func TestRenderStatus_JSONRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := renderStatus(&buf, sampleSnapshot(), "json"); err != nil {
		t.Fatal(err)
	}
	var got fleet.Snapshot
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json output does not parse: %v\n%s", err, buf.String())
	}
	if got.TotalWatts != 18.0 || len(got.Devices) != 2 {
		t.Fatalf("json round-trip lost data: %+v", got)
	}
}

func TestRenderStatus_YAMLRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := renderStatus(&buf, sampleSnapshot(), "yaml"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "totalWatts") {
		t.Fatalf("yaml output missing camelCase key (json tags not honored):\n%s", buf.String())
	}
	var got fleet.Snapshot
	if err := yaml.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("yaml output does not parse: %v", err)
	}
	if got.TotalWatts != 18.0 || len(got.Devices) != 2 {
		t.Fatalf("yaml round-trip lost data: %+v", got)
	}
}

func TestRenderStatus_Tree(t *testing.T) {
	var buf bytes.Buffer
	if err := renderStatus(&buf, sampleSnapshot(), "tree"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"ERGOZ FLEET", "node-1", "amdgpu", "18.0 W", "suspended (0 W)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tree output missing %q:\n%s", want, out)
		}
	}
}
