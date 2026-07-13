// Package fleet is the collector's core: it scrapes every ergoz-agent,
// caches the latest per-node samples, and serves the fleet-wide view —
// merged Prometheus metrics plus a JSON API for consumers like Sympozium.
package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// AgentLister returns the current set of agent scrape endpoints
// (host:port). Backed by the agents' headless Service in-cluster; a static
// list in tests.
type AgentLister func(ctx context.Context) ([]string, error)

// DNSLister resolves a headless Service DNS name to agent endpoints.
func DNSLister(service string, port int) AgentLister {
	return func(ctx context.Context) ([]string, error) {
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, service)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(ips))
		for _, ip := range ips {
			out = append(out, net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		}
		sort.Strings(out)
		return out, nil
	}
}

// DevicePower is one accelerator's latest reading.
type DevicePower struct {
	Node       string             `json:"node"`
	Kind       string             `json:"kind"`
	PCI        string             `json:"pci"`
	VendorID   string             `json:"vendorId"`
	DeviceID   string             `json:"deviceId"`
	Driver     string             `json:"driver"`
	PowerWatts float64            `json:"powerWatts"`
	Components map[string]float64 `json:"components,omitempty"`
	EnergyJ    float64            `json:"energyJoules"`
	Suspended  bool               `json:"suspended"`
}

// Snapshot is the fleet view served at /api/v1/fleet.
type Snapshot struct {
	ScrapedAt   time.Time     `json:"scrapedAt"`
	AgentsTotal int           `json:"agentsTotal"`
	AgentsUp    int           `json:"agentsUp"`
	TotalWatts  float64       `json:"totalWatts"`
	Devices     []DevicePower `json:"devices"`
}

// Collector scrapes agents and serves the merged view.
type Collector struct {
	List     AgentLister
	Interval time.Duration
	Client   *http.Client

	mu       sync.RWMutex
	snapshot Snapshot
	families map[string]map[string]*dto.MetricFamily // agent → name → family
}

func New(list AgentLister, interval time.Duration) *Collector {
	return &Collector{
		List:     list,
		Interval: interval,
		Client:   &http.Client{Timeout: 5 * time.Second},
		families: map[string]map[string]*dto.MetricFamily{},
	}
}

// Run scrapes on the configured cadence until ctx is done.
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	c.scrapeAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.scrapeAll(ctx)
		}
	}
}

func (c *Collector) scrapeAll(ctx context.Context) {
	agents, err := c.List(ctx)
	if err != nil {
		log.Printf("agent discovery failed: %v", err)
		return
	}
	type res struct {
		addr string
		fams map[string]*dto.MetricFamily
	}
	results := make(chan res, len(agents))
	for _, a := range agents {
		go func(addr string) {
			fams, err := c.scrapeOne(ctx, addr)
			if err != nil {
				log.Printf("scrape %s: %v", addr, err)
				results <- res{addr, nil}
				return
			}
			results <- res{addr, fams}
		}(a)
	}

	fresh := map[string]map[string]*dto.MetricFamily{}
	up := 0
	for range agents {
		r := <-results
		if r.fams != nil {
			fresh[r.addr] = r.fams
			up++
		}
	}

	snap := buildSnapshot(fresh)
	snap.AgentsTotal = len(agents)
	snap.AgentsUp = up

	c.mu.Lock()
	c.families = fresh
	c.snapshot = snap
	c.mu.Unlock()
}

func (c *Collector) scrapeOne(ctx context.Context, addr string) (map[string]*dto.MetricFamily, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	// The zero-value TextParser carries an unset validation scheme and
	// panics on first parse (prometheus/common >= 0.70) — always construct.
	parser := expfmt.NewTextParser(model.UTF8Validation)
	return parser.TextToMetricFamilies(io.LimitReader(resp.Body, 4<<20))
}

func buildSnapshot(all map[string]map[string]*dto.MetricFamily) Snapshot {
	snap := Snapshot{ScrapedAt: time.Now()}
	devices := map[string]*DevicePower{} // node/pci key

	get := func(fams map[string]*dto.MetricFamily, name string, fn func(labels map[string]string, v float64)) {
		fam, ok := fams[name]
		if !ok {
			return
		}
		for _, m := range fam.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			fn(labels, m.GetGauge().GetValue())
		}
	}
	dev := func(labels map[string]string) *DevicePower {
		key := labels["node"] + "/" + labels["pci"]
		d, ok := devices[key]
		if !ok {
			d = &DevicePower{
				Node: labels["node"], Kind: labels["kind"], PCI: labels["pci"],
				VendorID: labels["vendor_id"], DeviceID: labels["device_id"], Driver: labels["driver"],
			}
			devices[key] = d
		}
		return d
	}

	for _, fams := range all {
		get(fams, "ergoz_accel_power_watts", func(l map[string]string, v float64) {
			dev(l).PowerWatts = v
		})
		get(fams, "ergoz_accel_energy_joules_total", func(l map[string]string, v float64) {
			dev(l).EnergyJ = v
		})
		get(fams, "ergoz_accel_runtime_suspended", func(l map[string]string, v float64) {
			dev(l).Suspended = v == 1
		})
		get(fams, "ergoz_accel_component_power_watts", func(l map[string]string, v float64) {
			d := dev(l)
			if d.Components == nil {
				d.Components = map[string]float64{}
			}
			d.Components[l["component"]] = v
		})
	}

	keys := make([]string, 0, len(devices))
	for k := range devices {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		snap.Devices = append(snap.Devices, *devices[k])
		snap.TotalWatts += devices[k].PowerWatts
	}
	return snap
}

// ServeFleet handles GET /api/v1/fleet.
func (c *Collector) ServeFleet(w http.ResponseWriter, _ *http.Request) {
	c.mu.RLock()
	snap := c.snapshot
	c.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

// ServeMetrics re-exposes every agent's ergoz_* families merged, in
// Prometheus text format. Agents already label by node, so series stay
// distinct after the merge.
func (c *Collector) ServeMetrics(w http.ResponseWriter, _ *http.Request) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	w.Header().Set("Content-Type", string(expfmt.NewFormat(expfmt.TypeTextPlain)))
	enc := expfmt.NewEncoder(w, expfmt.NewFormat(expfmt.TypeTextPlain))

	merged := map[string]*dto.MetricFamily{}
	for _, fams := range c.families {
		for name, fam := range fams {
			if len(name) < 6 || name[:6] != "ergoz_" {
				continue
			}
			if existing, ok := merged[name]; ok {
				existing.Metric = append(existing.Metric, fam.Metric...)
			} else {
				// Fresh family struct: MetricFamily embeds a mutex, so it
				// must not be copied by value, and the cached slice must
				// not be mutated by later appends.
				merged[name] = &dto.MetricFamily{
					Name:   fam.Name,
					Help:   fam.Help,
					Type:   fam.Type,
					Metric: append([]*dto.Metric{}, fam.Metric...),
				}
			}
		}
	}
	names := make([]string, 0, len(merged))
	for n := range merged {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		_ = enc.Encode(merged[n])
	}
}
