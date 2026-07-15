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

// DevicePower is one accelerator's current power reading.
type DevicePower struct {
	Node       string             `json:"node"`
	Kind       string             `json:"kind"`
	PCI        string             `json:"pci"`
	VendorID   string             `json:"vendorId"`
	DeviceID   string             `json:"deviceId"`
	Driver     string             `json:"driver"`
	PowerWatts float64            `json:"powerWatts"`
	Components map[string]float64 `json:"components,omitempty"`
	Suspended  bool               `json:"suspended"`
	// Stale is true when this device's agent has not scraped successfully
	// recently — the value is last-known, not current. Stale devices are
	// excluded from TotalWatts.
	Stale bool `json:"stale"`
}

// Snapshot is the fleet view served at /api/v1/fleet. It is a point-in-time
// view of current power draw — no accumulated energy.
type Snapshot struct {
	ScrapedAt time.Time `json:"scrapedAt"`
	// AgentsTotal is the number of agents currently discovered.
	AgentsTotal int `json:"agentsTotal"`
	// AgentsUp is the number that scraped successfully on the last pass.
	AgentsUp int `json:"agentsUp"`
	// TotalWatts sums current (non-stale) device power across the fleet.
	TotalWatts float64 `json:"totalWatts"`
	// StaleDevices counts devices whose reading is last-known, not current.
	StaleDevices int           `json:"staleDevices"`
	Devices      []DevicePower `json:"devices"`
}

// agentEntry is one agent's last-known families and when it last scraped OK.
type agentEntry struct {
	families map[string]*dto.MetricFamily
	lastOK   time.Time
}

// Collector scrapes agents and serves the merged view.
type Collector struct {
	List     AgentLister
	Interval time.Duration
	Client   *http.Client

	mu       sync.RWMutex
	snapshot Snapshot
	agents   map[string]*agentEntry // addr → last-known data
}

func New(list AgentLister, interval time.Duration) *Collector {
	return &Collector{
		List:     list,
		Interval: interval,
		Client:   &http.Client{Timeout: 5 * time.Second},
		agents:   map[string]*agentEntry{},
	}
}

// staleAfter is how long since an agent's last good scrape before its
// readings are flagged stale (and, once well past it, dropped).
func (c *Collector) staleAfter() time.Duration {
	d := 3 * c.Interval
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	return d
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
		c.ageWithoutDiscovery()
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

	up := 0
	now := time.Now()
	c.mu.Lock()
	// Update last-known data for agents that scraped OK; agents that failed
	// keep their prior entry (and age toward stale) rather than vanishing.
	for range agents {
		r := <-results
		if r.fams != nil {
			c.agents[r.addr] = &agentEntry{families: r.fams, lastOK: now}
			up++
		}
	}
	// Prune agents that are neither in discovery nor seen recently — a
	// genuinely-gone node, not a transient blip.
	discovered := make(map[string]bool, len(agents))
	for _, a := range agents {
		discovered[a] = true
	}
	for addr, e := range c.agents {
		if !discovered[addr] && now.Sub(e.lastOK) > c.staleAfter() {
			delete(c.agents, addr)
		}
	}
	snap := buildSnapshot(c.agents, now, c.staleAfter())
	snap.AgentsTotal = len(agents)
	snap.AgentsUp = up
	c.snapshot = snap
	c.mu.Unlock()
}

// ageWithoutDiscovery rebuilds the snapshot when discovery itself failed, so
// readings age toward stale instead of the last good view being served as
// current indefinitely. Entries are kept, not pruned: discovery failing says
// nothing about which agents are gone, so there is no basis to drop any.
func (c *Collector) ageWithoutDiscovery() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := buildSnapshot(c.agents, now, c.staleAfter())
	// Nothing was discovered and nothing scraped on this pass; report that
	// rather than carrying the previous pass's counts forward.
	snap.AgentsTotal = 0
	snap.AgentsUp = 0
	c.snapshot = snap
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

func buildSnapshot(agents map[string]*agentEntry, now time.Time, staleAfter time.Duration) Snapshot {
	snap := Snapshot{ScrapedAt: now}
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
	dev := func(labels map[string]string, stale bool) *DevicePower {
		key := labels["node"] + "/" + labels["pci"]
		d, ok := devices[key]
		if !ok {
			d = &DevicePower{
				Node: labels["node"], Kind: labels["kind"], PCI: labels["pci"],
				VendorID: labels["vendor_id"], DeviceID: labels["device_id"], Driver: labels["driver"],
				Stale: stale,
			}
			devices[key] = d
		}
		return d
	}

	for _, e := range agents {
		stale := now.Sub(e.lastOK) > staleAfter
		fams := e.families
		get(fams, "ergoz_accel_power_watts", func(l map[string]string, v float64) {
			dev(l, stale).PowerWatts = v
		})
		get(fams, "ergoz_accel_runtime_suspended", func(l map[string]string, v float64) {
			dev(l, stale).Suspended = v == 1
		})
		get(fams, "ergoz_accel_component_power_watts", func(l map[string]string, v float64) {
			d := dev(l, stale)
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
		d := devices[k]
		snap.Devices = append(snap.Devices, *d)
		if d.Stale {
			snap.StaleDevices++
		} else {
			// Only current readings contribute to the live fleet total.
			snap.TotalWatts += d.PowerWatts
		}
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
	for _, e := range c.agents {
		for name, fam := range e.families {
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
