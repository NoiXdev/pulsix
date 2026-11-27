// The local status page's view of the running agent (backlog 3.132): the
// loop records what it sends as it sends it — check and probe results
// after redaction, facts, heartbeats — and when it last heard from the
// manager, and Snapshot puts that together with the scheduler's checks,
// the probe runner's assignments, the buffers, the identity and the last
// look at agent.yaml. Only the host's own agent.yaml turns the page on
// (applyStatusPage); nothing the manager sends can.
package agent

import (
	"context"
	"net"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/probes"
	"github.com/noixdev/pulsix/internal/agent/secrets"
	"github.com/noixdev/pulsix/internal/agent/statuspage"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// lastBatch is one check's or probe slot's last results, as produced for
// the manager.
type lastBatch struct {
	at     time.Time
	values []statuspage.Value
}

type probeSlot struct {
	target uuid.UUID
	key    string
}

// statusRecorder holds what the loop sent and heard. It is safe for
// concurrent use: the sinks run on the scheduler's and the runner's
// goroutines, the session on its own, the page on the HTTP server's.
type statusRecorder struct {
	mu             sync.Mutex
	startedAt      time.Time
	connectedSince *time.Time
	lastContact    *time.Time
	heartbeatAt    *time.Time
	heartbeat      *agentpb.Heartbeat
	factsAt        *time.Time
	facts          map[string]string
	checks         map[string]lastBatch
	probes         map[probeSlot]lastBatch
	names          *agentpb.AgentNames
	identity       *Identity
	set            secrets.Set
	state          secrets.State
}

func newStatusRecorder() *statusRecorder {
	return &statusRecorder{checks: map[string]lastBatch{}, probes: map[probeSlot]lastBatch{}}
}

func tptr(t time.Time) *time.Time { return &t }

func (r *statusRecorder) started(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startedAt = at
}

func (r *statusRecorder) connected(at *time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connectedSince = at
}

// contact notes a message from the manager, and the names it carried.
func (r *statusRecorder) contact(at time.Time, names *agentpb.AgentNames) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastContact = tptr(at)
	if names != nil {
		r.names = names
	}
}

func (r *statusRecorder) sentHeartbeat(at time.Time, hb *agentpb.Heartbeat) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.heartbeatAt, r.heartbeat = tptr(at), hb
}

func (r *statusRecorder) sentFacts(at time.Time, facts map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factsAt, r.facts = tptr(at), facts
}

func (r *statusRecorder) setIdentity(id *Identity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.identity = id
}

func (r *statusRecorder) looked(set secrets.Set, state secrets.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.set, r.state = set, state
}

// checkResults records each check's batch, as it goes to the manager.
func (r *statusRecorder) checkResults(results []checks.Result) {
	byKey := map[string]lastBatch{}
	for _, x := range results {
		b := byKey[x.CheckKey]
		b.at = x.At
		b.values = append(b.values, statuspage.Value{Item: x.Item, Metric: x.Metric, Value: x.Value, Label: x.Label, Error: x.Err})
		byKey[x.CheckKey] = b
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, b := range byKey {
		r.checks[k] = b
	}
}

// probeResults records each slot's round, as it goes to the manager.
func (r *statusRecorder) probeResults(results []probes.Result) {
	bySlot := map[probeSlot]lastBatch{}
	for _, x := range results {
		k := probeSlot{x.TargetHostID, x.CheckKey}
		b := bySlot[k]
		b.at = x.At
		b.values = append(b.values, statuspage.Value{Item: x.Item, Metric: x.Metric, Value: x.Value, Label: x.Label, Error: x.Err})
		bySlot[k] = b
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, b := range bySlot {
		r.probes[k] = b
	}
}

// newStatusPage wires the page's controller to the loop.
func (l *Loop) newStatusPage() *statuspage.Controller {
	return statuspage.NewController(l, statuspage.Options{
		DataDir: l.cfg.DataDir,
		Redact:  func(s string) string { return l.redactor().Redact(s) },
		Log:     l.log,
		Now:     l.now,
		Listen:  l.statusPageListen,
	})
}

// withStatusPageListen replaces how the status page opens its listener
// (tests: one that never answers).
func withStatusPageListen(fn func(ctx context.Context, network, addr string) (net.Listener, error)) LoopOption {
	return func(l *Loop) { l.statusPageListen = fn }
}

// applyStatusPage hands the file's status_page section to the page's
// controller. An absent file has no section: the page is off. A refused
// file turns the page off too, reported as failed when the file last
// applied asked for the page and as off when it did not (R15 of the
// 3.132 plan). localMu is held.
func (l *Loop) applyStatusPage(set secrets.Set, state secrets.State) {
	if state != secrets.StateOK && state != secrets.StateAbsent {
		problem := ""
		if l.statusPage.Asked() {
			problem = "the configuration file is refused"
		}
		l.statusPage.Apply(statuspage.Config{}, problem)
		return
	}
	page, problem := set.StatusPage()
	l.statusPage.Apply(statuspage.Config{
		Listen: page.Listen, Token: page.Token, TLSCert: page.TLSCert, TLSKey: page.TLSKey, Network: page.Network,
	}, problem)
}

// statusPageMode is the controller's mode as the heartbeat reports it.
func statusPageMode(m statuspage.Mode) agentpb.StatusPageMode {
	switch m {
	case statuspage.ModeLoopback:
		return agentpb.StatusPageMode_STATUS_PAGE_MODE_LOOPBACK
	case statuspage.ModeNetwork:
		return agentpb.StatusPageMode_STATUS_PAGE_MODE_NETWORK
	case statuspage.ModeFailed:
		return agentpb.StatusPageMode_STATUS_PAGE_MODE_FAILED
	}
	return agentpb.StatusPageMode_STATUS_PAGE_MODE_OFF
}

var statusPageModeNames = map[agentpb.StatusPageMode]string{
	agentpb.StatusPageMode_STATUS_PAGE_MODE_OFF:      "off",
	agentpb.StatusPageMode_STATUS_PAGE_MODE_LOOPBACK: "loopback",
	agentpb.StatusPageMode_STATUS_PAGE_MODE_NETWORK:  "network",
	agentpb.StatusPageMode_STATUS_PAGE_MODE_FAILED:   "failed",
}

// Snapshot is the status page's view of the agent (statuspage.Source).
func (l *Loop) Snapshot() statuspage.Snapshot {
	running := l.scheduler.Checks()
	assigned := l.probes.Assigned()
	checkN, checkOldest := l.buffer.Stats()
	probeN, probeOldest := l.probeBuffer.Stats()
	l.routeMu.Lock()
	connected := l.sender != nil
	l.routeMu.Unlock()
	hostname, _ := os.Hostname()

	r := l.status
	r.mu.Lock()
	defer r.mu.Unlock()
	s := statuspage.Snapshot{
		GeneratedAt: l.now(),
		Agent:       statuspage.Agent{Version: l.cfg.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, StartedAt: r.startedAt},
		Connection: statuspage.Connection{
			Manager: l.cfg.ManagerAddr, Connected: connected, LastContact: r.lastContact, LastHeartbeat: r.heartbeatAt,
		},
		Identity: statuspage.Identity{AgentName: l.cfg.AgentName, Hostname: hostname},
		Facts:    statuspage.Facts{SentAt: r.factsAt, Values: r.facts},
		Buffer:   statuspage.Buffer{CheckResults: checkN, ProbeResults: probeN, Oldest: oldestOf(checkOldest, probeOldest)},
	}
	if connected {
		s.Connection.ConnectedSince = r.connectedSince
	}
	if id := r.identity; id != nil {
		s.Identity.HostID = id.HostID()
		if leaf := id.Leaf(); leaf != nil {
			s.Identity.OrgID = orgIDOf(leaf.URIs)
			s.Identity.CertNotAfter = tptr(leaf.NotAfter)
			s.Identity.RenewalDue = tptr(leaf.NotAfter.Add(-leaf.NotAfter.Sub(leaf.NotBefore) / 3))
		}
	}
	if n := r.names; n != nil {
		s.Identity.OrgName, s.Identity.HostName = n.GetOrgName(), n.GetHostName()
	}

	keep := make(map[string]bool, len(running))
	for _, c := range running {
		keep[c.Key] = true
		sc := statuspage.Check{Key: c.Key, Plugin: c.Plugin, Local: c.Local, IntervalSeconds: int(c.Interval / time.Second)}
		if b, ok := r.checks[c.Key]; ok {
			sc.LastRun, sc.Results = tptr(b.at), b.values
		}
		s.Checks = append(s.Checks, sc)
	}
	for k := range r.checks { // a check that stopped is not shown again
		if !keep[k] {
			delete(r.checks, k)
		}
	}

	s.Probes.Enabled = l.probes.Enabled()
	if r.state == secrets.StateOK {
		for _, p := range r.set.Probes().DenyCIDRs {
			s.Probes.DenyCIDRs = append(s.Probes.DenyCIDRs, p.String())
		}
	}
	keepSlots := make(map[probeSlot]bool, len(assigned))
	for _, a := range assigned {
		k := probeSlot{a.TargetHostID, a.CheckKey}
		keepSlots[k] = true
		p := statuspage.Probe{TargetHostID: a.TargetHostID.String(), CheckKey: a.CheckKey, Plugin: a.Plugin,
			Address: a.Address, IntervalSeconds: int(a.IntervalSeconds)}
		if b, ok := r.probes[k]; ok {
			p.LastRun, p.Results = tptr(b.at), b.values
		}
		s.Probes.Assigned = append(s.Probes.Assigned, p)
	}
	for k := range r.probes {
		if !keepSlots[k] {
			delete(r.probes, k)
		}
	}

	if hb := r.heartbeat; hb != nil {
		s.Heartbeat = statuspage.Heartbeat{
			SentAt: r.heartbeatAt, Version: hb.GetVersion(), ConfigVersion: hb.GetConfigVersion(),
			SecretNames: hb.GetSecretNames(), SecretsState: hb.GetSecretsState(),
			LocalCheckProblems: hb.GetLocalCheckProblems(), ProbesSupported: hb.GetProbesSupported(),
			ProbeConfigVersion: hb.GetProbeConfigVersion(), StatusPage: statusPageModeNames[hb.GetStatusPage()],
		}
		for _, c := range hb.GetLocalChecks() {
			s.Heartbeat.LocalChecks = append(s.Heartbeat.LocalChecks, statuspage.LocalCheck{
				Key: c.GetKey(), Plugin: c.GetPlugin(), ParamsJSON: c.GetParamsJson(), IntervalSeconds: int(c.GetIntervalSeconds()),
			})
		}
	}

	s.Abilities = statuspage.Abilities{
		ConfigFile: l.cfg.SecretsPath, ConfigState: string(r.state),
		ScriptCommands: r.set.CommandNames(), SecretNames: r.set.Names(),
	}
	for _, c := range r.set.LocalChecks() {
		s.Abilities.LocalChecks = append(s.Abilities.LocalChecks, c.Key)
	}
	if l.logRing != nil {
		s.Log = l.logRing.Lines()
	}
	return s
}

// orgIDOf is the organization ID in the certificate's URI SAN.
func orgIDOf(uris []*url.URL) string {
	for _, u := range uris {
		if rest, ok := strings.CutPrefix(u.String(), orgSANPrefix); ok {
			return rest
		}
	}
	return ""
}

// oldestOf is the earlier of two times, a zero one ignored; nil for none.
func oldestOf(a, b time.Time) *time.Time {
	ts := slices.DeleteFunc([]time.Time{a, b}, time.Time.IsZero)
	if len(ts) == 0 {
		return nil
	}
	return tptr(slices.MinFunc(ts, time.Time.Compare))
}
