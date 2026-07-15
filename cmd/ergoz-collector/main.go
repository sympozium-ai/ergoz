// ergoz-collector is the fleet aggregation pod (project decision D2): it
// discovers ergoz-agent endpoints through their headless Service, scrapes
// each agent's metrics, and serves the merged fleet view — Prometheus text
// at /metrics and JSON at /api/v1/fleet — so consumers (Sympozium, dashboards)
// talk to one endpoint instead of every node.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/sympozium-ai/ergoz/internal/fleet"
)

func main() {
	service := envOr("ERGOZ_AGENT_SERVICE", "ergoz-agent")
	port := portOr("ERGOZ_AGENT_PORT", 9743)
	listen := envOr("ERGOZ_LISTEN", ":9744")
	interval := durationOr("ERGOZ_SCRAPE_INTERVAL", 5*time.Second)

	c := fleet.New(fleet.DNSLister(service, port), interval)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go c.Run(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", c.ServeMetrics)
	mux.HandleFunc("GET /api/v1/fleet", c.ServeFleet)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	log.Printf("ergoz-collector on %s (agents=%s:%d every %s)", listen, service, port, interval)
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

func portOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			return p
		}
		log.Printf("ignoring invalid %s=%q, using %d", key, os.Getenv(key), def)
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
