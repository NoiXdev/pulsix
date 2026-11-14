// Package snmp is the snmp probe: it polls a device over SNMP v2c or v3
// from the manager (backlog 3.13d, spec
// docs/superpowers/specs/2026-11-14-snmp-checks-design.md, "The snmp
// plugin").
//
// Interface rates need the previous poll's counters, so the probe comes in
// two forms: Poller keeps one check slot's previous poll and reports rates
// from the second poll on; Probe, the registry's stateless form, is a
// fresh Poller per call and never reports a rate.
package snmp

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/noixdev/pulsix/internal/probe"
)

// hostMetrics are the metrics on the whole-host item.
var hostMetrics = []string{"snmp.up", "snmp.response_ms", "snmp.uptime_seconds"}

// Info is what a poll tells its caller beyond the observations.
type Info struct {
	// InterfacesSkipped: the device has more than 128 interfaces; only the
	// 128 with the lowest ifIndex were reported. The runner logs it (at
	// most once per check per hour).
	InterfacesSkipped bool
}

// Poller polls one check slot's device and keeps what the next poll's
// rates need: the counters and sysUpTime of its last poll, in memory only.
// Its zero value is not usable; NewPoller makes one. Polls of one Poller
// run one at a time. Dropping a Poller drops its state; it holds no
// resources between polls.
type Poller struct {
	mu   sync.Mutex
	now  func() time.Time
	dial dialFunc

	// target and prev are the last poll's device and sample; a poll of
	// another target starts over.
	target netip.AddrPort
	prev   *sample
}

// NewPoller returns a Poller without a previous poll. now is the clock the
// rates' intervals are measured with; nil is time.Now.
func NewPoller(now func() time.Time) *Poller {
	if now == nil {
		now = time.Now
	}
	return &Poller{now: now, dial: dialDevice}
}

// Probe is the snmp probe as the registry runs it: one poll without a
// previous one, so it reports no rates.
func Probe(ctx context.Context, g *probe.Guard, params map[string]any, address string, secret map[string]string) []probe.Observation {
	obs, _ := NewPoller(nil).Poll(ctx, g, params, address, secret)
	return obs
}

// Poll polls the device at address with params (validated by
// internal/checks) and the fields of the check's secret. It never returns
// a Go error: every outcome is in the observations, as probe.Func
// describes. A device that does not answer the first request is
// snmp.up = 0 alone; params or a secret that cannot be used, a target the
// guard refuses or a v3 device refusing the credentials is an error on
// snmp.up, snmp.response_ms, snmp.uptime_seconds and each oids entry's
// snmp.value.
//
// Each request waits timeout_seconds and is retried once; ctx bounds the
// whole poll.
func (p *Poller) Poll(ctx context.Context, g *probe.Guard, params map[string]any, address string, secret map[string]string) ([]probe.Observation, Info) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if g == nil {
		g = probe.NewGuard(nil, nil)
	}

	s, err := parseSettings(params)
	if err != nil {
		return probe.FailAll(err.Error(), hostMetrics...), Info{}
	}
	fail := func(msg string) []probe.Observation {
		obs := probe.FailAll(msg, hostMetrics...)
		for _, e := range s.oids {
			obs = append(obs, probe.Observation{Metric: "snmp.value", Item: e.name, Err: msg})
		}
		return obs
	}
	creds, err := credentialsFor(s.secret, s.version, secret)
	if err != nil {
		return fail(err.Error()), Info{}
	}
	host, err := probe.TargetHost(address)
	if err != nil {
		return fail(err.Error()), Info{}
	}
	ip, err := probe.ResolveAllowed(ctx, g, host)
	if errors.Is(err, probe.ErrNotAllowed) {
		return fail(probe.ErrNotAllowed.Error()), Info{}
	}
	if err != nil {
		return probe.Down("snmp"), Info{}
	}
	target := netip.AddrPortFrom(ip, uint16(s.port)) //nolint:gosec // parseSettings bounds the port to 1..65535.

	c, err := p.dial(ctx, g, target, s.timeout, creds)
	if err != nil {
		return failed(classify(err), s, fail), Info{}
	}
	defer c.close()

	// The entries to ask for; one SNMP cannot encode fails on its own.
	var obs []probe.Observation
	asked := make([]oidEntry, 0, len(s.oids))
	var badObs []probe.Observation
	for _, e := range s.oids {
		if e.bad {
			badObs = append(badObs, probe.Observation{Metric: "snmp.value", Item: e.name, Err: errBadOID})
			continue
		}
		asked = append(asked, e)
	}

	// The first request: sysUpTime and the first entries.
	oids := make([]string, 0, len(asked)+1)
	oids = append(oids, oidSysUpTime)
	for _, e := range asked {
		oids = append(oids, e.oid)
	}
	first := oids[:min(len(oids), oidsPerRequest)]
	start := time.Now()
	pkt, err := c.get(first)
	if err != nil {
		return failed(classifyFirst(err, creds.version == gosnmp.Version3, c.answered()), s, fail), Info{}
	}
	rtt := c.roundTrip()
	if rtt <= 0 {
		rtt = time.Since(start)
	}
	obs = append(obs, probe.Value("snmp.up", 1), probe.Value("snmp.response_ms", probe.Millis(rtt)))

	cur := sample{}
	vars, batchErr := answers(pkt, first)
	obs = append(obs, uptimeObservation(vars, batchErr, &cur))
	obs = append(obs, entryObservations(asked[:len(first)-1], vars[min(len(vars), 1):], batchErr)...)

	// The other entries, oidsPerRequest at a time.
	for off := len(first); off < len(oids); off += oidsPerRequest {
		batch := oids[off:min(len(oids), off+oidsPerRequest)]
		entries := asked[off-1 : off-1+len(batch)]
		pkt, err := c.get(batch)
		if err != nil {
			obs = append(obs, entryObservations(entries, nil, errNoAnswer)...)
			continue
		}
		vars, batchErr := answers(pkt, batch)
		obs = append(obs, entryObservations(entries, vars, batchErr)...)
	}
	obs = append(obs, badObs...)

	var info Info
	keep := true
	if s.interfaces {
		var ifObs []probe.Observation
		ifObs, info.InterfacesSkipped, keep = p.pollInterfaces(c, target, &cur)
		obs = append(obs, ifObs...)
	}
	if keep {
		p.target, p.prev = target, &cur
	}
	return obs, info
}

// failed is the result of a dial or first request that failed. The
// previous poll stays: the next answer's rates span both intervals.
func failed(f failure, s settings, fail func(string) []probe.Observation) []probe.Observation {
	switch f {
	case failRefused:
		return fail(probe.ErrNotAllowed.Error())
	case failCredentials:
		return fail(fmt.Sprintf(errCredentialsFmt, s.secret))
	}
	return probe.Down("snmp")
}

// pollInterfaces reads the interface table into cur and returns its
// observations, with rates against the previous poll of the same target,
// and whether cur should replace that poll: not when the table could not
// be read, so the next poll's rates span both intervals.
func (p *Poller) pollInterfaces(c client, target netip.AddrPort, cur *sample) (obs []probe.Observation, skipped, keep bool) {
	rows, skipped, err := readInterfaces(c)
	cur.at = p.now()
	if err != nil {
		return []probe.Observation{{Metric: "snmp.if.status", Err: errNoInterfaceTable}}, false, false
	}
	nameInterfaces(rows)
	cur.ifs = make(map[int]ifCounters, len(rows))
	for _, r := range rows {
		cur.ifs[r.index] = r.counters
	}
	var prev *sample
	if p.prev != nil && p.target == target {
		prev = p.prev
	}
	secs, ok := elapsed(prev, *cur)
	var prevIfs map[int]ifCounters
	if prev != nil {
		prevIfs = prev.ifs
	}
	return interfaceObservations(rows, prevIfs, secs, ok), skipped, true
}

// uptimeObservation is snmp.uptime_seconds from the first request's first
// answer, which it also records in cur.
func uptimeObservation(vars []gosnmp.SnmpPDU, batchErr string, cur *sample) probe.Observation {
	obs := probe.Observation{Metric: "snmp.uptime_seconds"}
	switch {
	case batchErr != "":
		obs.Err = batchErr
	case noSuchObject(vars[0].Type):
		obs.Err = errNoSuchObject
	default:
		ticks, ok := toUint(vars[0].Value)
		if vars[0].Type != gosnmp.TimeTicks || !ok || ticks >= 1<<32 {
			obs.Err = errUnsupportedType
			break
		}
		cur.uptime, cur.hasUptime = uint32(ticks), true
		v := float64(ticks) / 100
		obs.Value = &v
	}
	return obs
}

// entryObservations are the observations of oids entries from their
// answers, or batchErr on each when the request as a whole failed.
func entryObservations(entries []oidEntry, vars []gosnmp.SnmpPDU, batchErr string) []probe.Observation {
	out := make([]probe.Observation, 0, len(entries))
	for i, e := range entries {
		if batchErr != "" {
			out = append(out, probe.Observation{Metric: "snmp.value", Item: e.name, Err: batchErr})
			continue
		}
		out = append(out, valueObservation(e.name, vars[i]))
	}
	return out
}
