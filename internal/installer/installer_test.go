package installer

import (
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

func TestLoadChart_EmbeddedChartIsValid(t *testing.T) {
	ch, err := LoadChart()
	if err != nil {
		t.Fatalf("LoadChart: %v", err)
	}
	if ch.Name() != "ergoz" {
		t.Fatalf("chart name = %q, want ergoz", ch.Name())
	}
	if ch.Metadata.Version == "" || ch.Metadata.AppVersion == "" {
		t.Fatalf("chart missing version/appVersion: %+v", ch.Metadata)
	}
	if len(ch.Templates) == 0 {
		t.Fatal("chart has no templates (embed prefix regression?)")
	}
}

func TestLoadChart_TemplatesRender(t *testing.T) {
	ch, err := LoadChart()
	if err != nil {
		t.Fatal(err)
	}
	// The install action validates templates render; a dry-run install
	// against an in-memory store exercises that without a cluster.
	cfg := &action.Configuration{Releases: storage.Init(driver.NewMemory())}
	inst := action.NewInstall(cfg)
	inst.ReleaseName = ReleaseName
	inst.Namespace = Namespace
	inst.DryRun = true
	inst.ClientOnly = true
	if _, err := inst.Run(ch, map[string]interface{}{"image": map[string]interface{}{"tag": "test"}}); err != nil {
		t.Fatalf("dry-run install (template render) failed: %v", err)
	}
}
