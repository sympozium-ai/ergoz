// ergoz-agent is the per-node DaemonSet: it identifies accelerators via
// llmfit-dra's probe walk, samples their power from vanilla-kernel sysfs,
// and serves Prometheus metrics for the Ergoz collector to aggregate.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sympozium-ai/ergoz/internal/sampler"
	"github.com/sympozium-ai/llmfit-dra/pkg/probe"
)

func main() {
	node := envOr("NODE_NAME", hostname())
	sysRoot := envOr("ERGOZ_SYSFS_ROOT", "/sys")
	procRoot := envOr("ERGOZ_PROCFS_ROOT", "/proc")
	listen := envOr("ERGOZ_LISTEN", ":9743")
	interval := durationOr("ERGOZ_SAMPLE_INTERVAL", time.Second)

	devices, err := probe.New(sysRoot, procRoot).Walk()
	if err != nil {
		log.Fatalf("device walk failed: %v", err)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	s := sampler.New(reg, node, interval, sysRoot, devices)

	for _, t := range s.Targets() {
		log.Printf("measuring %s %s (vendor %s device %s driver %s): power=%q gpu_metrics=%q",
			t.Device.Kind, t.Device.PCIAddr, t.Device.PCIVendor, t.Device.PCIDevice,
			t.Device.Driver, t.Sensor.PowerFile, t.Sensor.GPUMetricsFile)
	}
	if len(s.Targets()) == 0 {
		log.Printf("no measurable accelerators found under %s — serving empty metrics", sysRoot)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go s.Run(ctx)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	log.Printf("ergoz-agent on %s (node=%s interval=%s targets=%d)", listen, node, interval, len(s.Targets()))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func durationOr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		log.Printf("ignoring invalid %s=%q, using %s", key, os.Getenv(key), def)
	}
	return def
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
