// Package agent is Neo Agent: it runs on each Iran tunnel server, keeps the
// tunnels the panel wants running, and enforces every sold port in the kernel
// (nftables): traffic counters, quota, expiry, speed and IP caps.
//
// Limits are stored locally, so a service is cut exactly at its quota or
// expiry even while the panel is unreachable; usage is buffered and reported
// as absolute totals when the connection returns.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"neosell/internal/agentproto"
)

const (
	tickEvery = 10 * time.Second
)

// persisted is the agent's durable state (state.json).
type persisted struct {
	Desired agentproto.State `json:"desired"`
	// Totals are absolute bytes per service since it appeared on this agent.
	Totals map[int64]*total `json:"totals"`
	// Periods remembers the quota period last applied per service.
	Periods map[int64]int64 `json:"periods"`
	// QuotaUsed is the last known quota consumption per service.
	QuotaUsed map[int64]int64 `json:"quota_used"`
	// Hit records services that hit quota/expiry (reported until acknowledged by a new state).
	Hit map[int64]string `json:"hit"`
	// Tunnels holds driver bookkeeping (e.g. NeoTunnel forwards we created).
	Tunnels map[string]json.RawMessage `json:"tunnels"`
}

type total struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type Agent struct {
	dir     string
	nft     NFT
	drivers map[string]Driver
	now     func() time.Time
	ips     func() []string

	mu      sync.Mutex
	st      persisted
	lastErr []string
	status  map[string]agentproto.TunnelStatus

	// outbound messages to the panel (buffered; dropped when full, recomputed later)
	out chan []byte
}

func New(dir string, nft NFT) (*Agent, error) {
	a := &Agent{
		dir: dir, nft: nft, now: time.Now, ips: localIPs,
		status: map[string]agentproto.TunnelStatus{},
		out:    make(chan []byte, 64),
	}
	a.drivers = map[string]Driver{
		"template":  &TemplateDriver{Dir: dir},
		"neotunnel": &NeoTunnelDriver{},
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if err := a.load(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Agent) statePath() string { return filepath.Join(a.dir, "state.json") }

func (a *Agent) load() error {
	a.st = persisted{Totals: map[int64]*total{}, Periods: map[int64]int64{}, QuotaUsed: map[int64]int64{}, Hit: map[int64]string{}, Tunnels: map[string]json.RawMessage{}}
	raw, err := os.ReadFile(a.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &a.st); err != nil {
		return fmt.Errorf("state.json: %w", err)
	}
	if a.st.Totals == nil {
		a.st.Totals = map[int64]*total{}
	}
	if a.st.Periods == nil {
		a.st.Periods = map[int64]int64{}
	}
	if a.st.QuotaUsed == nil {
		a.st.QuotaUsed = map[int64]int64{}
	}
	if a.st.Hit == nil {
		a.st.Hit = map[int64]string{}
	}
	if a.st.Tunnels == nil {
		a.st.Tunnels = map[string]json.RawMessage{}
	}
	return nil
}

func (a *Agent) saveLocked() error {
	raw, _ := json.Marshal(a.st)
	tmp := a.statePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.statePath())
}

// SetState stores and applies a new desired state from the panel.
func (a *Agent) SetState(ctx context.Context, s agentproto.State) agentproto.Applied {
	a.mu.Lock()
	// Fold current counters first so nothing counted under the old rules is lost.
	a.foldLocked()
	prevPeriods := a.st.Periods
	a.st.Desired = s
	a.st.Periods = map[int64]int64{}
	keep := map[int64]bool{}
	for _, svc := range s.Services {
		keep[svc.ID] = true
		if prev, ok := prevPeriods[svc.ID]; !ok || prev != svc.Period {
			// New service or a new period (renewal): the panel's number wins.
			a.st.QuotaUsed[svc.ID] = svc.UsedBytes
			delete(a.st.Hit, svc.ID)
		} else if svc.UsedBytes > a.st.QuotaUsed[svc.ID] {
			a.st.QuotaUsed[svc.ID] = svc.UsedBytes
		}
		a.st.Periods[svc.ID] = svc.Period
		if svc.Blocked {
			delete(a.st.Hit, svc.ID) // the panel already knows
		}
	}
	for id := range a.st.QuotaUsed {
		if !keep[id] {
			delete(a.st.QuotaUsed, id)
			delete(a.st.Hit, id)
		}
	}
	// Totals of removed services are kept until reported once more, then dropped.
	a.saveLocked()
	a.mu.Unlock()

	errs := a.apply(ctx)
	a.mu.Lock()
	a.lastErr = errs
	a.mu.Unlock()
	return agentproto.Applied{Rev: s.Rev, Errors: errs}
}

// apply converges tunnels and the nftables table to the desired state.
func (a *Agent) apply(ctx context.Context) []string {
	a.mu.Lock()
	desired := a.st.Desired
	a.mu.Unlock()

	var errs []string
	// Tunnels, grouped per driver so each can diff its own set.
	byDriver := map[string][]agentproto.Tunnel{}
	for _, t := range desired.Tunnels {
		byDriver[t.Driver] = append(byDriver[t.Driver], t)
	}
	portsByTunnel := map[string][]agentproto.Port{}
	for _, s := range desired.Services {
		portsByTunnel[s.Tunnel] = append(portsByTunnel[s.Tunnel], s.Ports...)
	}
	for name, d := range a.drivers {
		book := a.bookkeeping(name)
		statuses, newBook, err := d.Sync(ctx, SyncInput{Tunnels: byDriver[name], Ports: portsByTunnel, NeoTunnel: desired.NeoTunnel, Book: book})
		a.setBookkeeping(name, newBook)
		if err != nil {
			errs = append(errs, name+": "+err.Error())
		}
		a.mu.Lock()
		for _, st := range statuses {
			a.status[st.Name] = st
		}
		a.mu.Unlock()
	}
	if err := a.applyNFT(); err != nil {
		errs = append(errs, "nftables: "+err.Error())
	}
	return errs
}

func (a *Agent) bookkeeping(driver string) json.RawMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.st.Tunnels[driver]
}

func (a *Agent) setBookkeeping(driver string, b json.RawMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if b == nil {
		delete(a.st.Tunnels, driver)
	} else {
		a.st.Tunnels[driver] = b
	}
	a.saveLocked()
}

func (a *Agent) runtimeLocked() []svcRuntime {
	now := a.now().Unix()
	out := make([]svcRuntime, 0, len(a.st.Desired.Services))
	for _, s := range a.st.Desired.Services {
		r := svcRuntime{Service: s, QuotaUsed: a.st.QuotaUsed[s.ID]}
		r.Block = s.Blocked || (s.ExpiresAt > 0 && now >= s.ExpiresAt) ||
			(s.QuotaBytes > 0 && r.QuotaUsed >= s.QuotaBytes)
		out = append(out, r)
	}
	return out
}

func (a *Agent) applyNFT() error {
	a.mu.Lock()
	rt := a.runtimeLocked()
	a.mu.Unlock()
	if !a.nft.Available() {
		if len(rt) == 0 {
			return nil
		}
		return errors.New("nft is not installed (apt install nftables)")
	}
	a.mu.Lock()
	a.foldLocked() // counters are recreated at zero by the new table
	a.mu.Unlock()
	return a.nft.Apply(BuildRuleset(rt, a.ips()))
}

// foldLocked moves kernel counters into the persisted totals and refreshes
// quota consumption.
func (a *Agent) foldLocked() {
	if !a.nft.Available() {
		return
	}
	counters, err := a.nft.ResetCounters()
	if err == nil {
		for name, bytes := range counters {
			id, suffix, ok := parseObjName(name)
			if !ok || bytes == 0 {
				continue
			}
			t := a.st.Totals[id]
			if t == nil {
				t = &total{}
				a.st.Totals[id] = t
			}
			switch suffix {
			case "up":
				t.Up += bytes
			case "down":
				t.Down += bytes
			}
		}
	}
	if quotas, err := a.nft.Quotas(); err == nil {
		for name, used := range quotas {
			if id, suffix, ok := parseObjName(name); ok && suffix == "q" && used > a.st.QuotaUsed[id] {
				a.st.QuotaUsed[id] = used
			}
		}
	}
	a.saveLocked()
}

// Tick folds counters, detects quota/expiry hits (re-applying the table so the
// cut is immediate even if a quota object was not the one that dropped), and
// returns the usage report and new events.
func (a *Agent) Tick() (agentproto.Usage, []agentproto.Event, bool) {
	a.mu.Lock()
	a.foldLocked()
	now := a.now().Unix()
	var events []agentproto.Event
	changed := false
	for _, s := range a.st.Desired.Services {
		if s.Blocked {
			continue
		}
		kind := ""
		if s.QuotaBytes > 0 && a.st.QuotaUsed[s.ID] >= s.QuotaBytes {
			kind = "quota"
		} else if s.ExpiresAt > 0 && now >= s.ExpiresAt {
			kind = "expired"
		}
		if kind != "" && a.st.Hit[s.ID] != kind {
			a.st.Hit[s.ID] = kind
			events = append(events, agentproto.Event{ServiceID: s.ID, Kind: kind, At: now})
			changed = true
		}
	}
	u := agentproto.Usage{Counters: []agentproto.Counter{}}
	ids := make([]int64, 0, len(a.st.Totals))
	for id := range a.st.Totals {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		t := a.st.Totals[id]
		u.Counters = append(u.Counters, agentproto.Counter{ServiceID: id, Up: t.Up, Down: t.Down})
	}
	a.saveLocked()
	a.mu.Unlock()
	if changed {
		a.applyNFT()
	}
	return u, events, changed
}

// PendingEvents re-sends hits not yet reflected by the panel (after reconnect).
func (a *Agent) PendingEvents() []agentproto.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []agentproto.Event
	for id, kind := range a.st.Hit {
		out = append(out, agentproto.Event{ServiceID: id, Kind: kind, At: a.now().Unix()})
	}
	return out
}

// DropTotals forgets totals of services no longer desired (after reporting them).
func (a *Agent) DropTotals() {
	a.mu.Lock()
	defer a.mu.Unlock()
	keep := map[int64]bool{}
	for _, s := range a.st.Desired.Services {
		keep[s.ID] = true
	}
	for id := range a.st.Totals {
		if !keep[id] {
			delete(a.st.Totals, id)
		}
	}
	a.saveLocked()
}

func (a *Agent) TunnelStatuses() []agentproto.TunnelStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]agentproto.TunnelStatus, 0, len(a.status))
	for _, s := range a.status {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CheckPorts reports which of the ports are already listening on this server
// (by anything other than the agent's own sold ports).
func (a *Agent) CheckPorts(c agentproto.CheckPorts) agentproto.PortsResult {
	busy := []int{}
	listening := listeningPorts()
	a.mu.Lock()
	sold := map[int]bool{}
	for _, s := range a.st.Desired.Services {
		for _, p := range s.Ports {
			sold[p.Port] = true
		}
	}
	a.mu.Unlock()
	for _, p := range c.Ports {
		if listening[p] && !sold[p] {
			busy = append(busy, p)
		}
	}
	return agentproto.PortsResult{Busy: busy}
}

func localIPs() []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			out = append(out, n.IP.String())
		}
	}
	return out
}

func logErrs(errs []string) {
	for _, e := range errs {
		slog.Warn("agent: apply", "err", e)
	}
}
