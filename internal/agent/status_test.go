// Tests for the local status page in the running agent (backlog 3.132):
// only agent.yaml turns it on, the heartbeat reports where it is, and it
// shows what the agent sent, as sent.
package agent

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/probes"
	"github.com/noixdev/pulsix/internal/agent/statuspage"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
	"github.com/noixdev/pulsix/internal/probeconfig"
)

// freePort is a loopback port nothing listens on right now.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func fetchStatus(t *testing.T, addr string) statuspage.Snapshot {
	t.Helper()
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/status.json")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status.json = %d %s", resp.StatusCode, b)
	}
	var s statuspage.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestLoopStatusPageOffWithoutSection: no status_page, no page — the
// heartbeat says off, nothing listens, and nothing the manager sends turns
// it on.
func TestLoopStatusPageOffWithoutSection(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	writeSecretsFile(t, lt.cfg.SecretsPath, twoSecrets)
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	stream.pushManagerMessage(&agentpb.ManagerMessage{Ack: true, AgentNames: &agentpb.AgentNames{OrgName: "o", HostName: "h"}})
	if got := heartbeats(stream)[0].GetStatusPage(); got != agentpb.StatusPageMode_STATUS_PAGE_MODE_OFF {
		t.Fatalf("heartbeat status_page = %v, want OFF", got)
	}
	if addr := l.statusPage.Addr(); addr != "" {
		t.Fatalf("a page listens on %s", addr)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestLoopServesStatusPageOnLoopback: status_page with a loopback listen
// address serves the page from the start, the heartbeat says loopback, the
// page names what the manager said, and the page stops with the agent.
func TestLoopServesStatusPageOnLoopback(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	addr := "127.0.0.1:" + strconv.Itoa(freePort(t))
	writeSecretsFile(t, lt.cfg.SecretsPath, twoSecrets+"status_page:\n  listen: \""+addr+"\"\n")
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	l := lt.newLoop()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	if got := heartbeats(stream)[0].GetStatusPage(); got != agentpb.StatusPageMode_STATUS_PAGE_MODE_LOOPBACK {
		t.Fatalf("heartbeat status_page = %v, want LOOPBACK", got)
	}
	stream.pushManagerMessage(&agentpb.ManagerMessage{Ack: true, AgentNames: &agentpb.AgentNames{OrgName: "Acme", HostName: "Web One"}})
	waitFor(t, 2*time.Second, func() bool { return fetchStatus(t, addr).Identity.HostName == "Web One" })

	s := fetchStatus(t, addr)
	if s.Identity.OrgName != "Acme" || s.Identity.HostID != "host-1" || s.Identity.OrgID != testOrgID.String() || s.Identity.CertNotAfter == nil {
		t.Errorf("identity = %+v", s.Identity)
	}
	if !s.Connection.Connected || s.Connection.Manager != lt.cfg.ManagerAddr || s.Connection.LastHeartbeat == nil || s.Connection.LastContact == nil {
		t.Errorf("connection = %+v", s.Connection)
	}
	if s.Heartbeat.StatusPage != "loopback" || s.Heartbeat.SecretsState != "ok" {
		t.Errorf("heartbeat = %+v", s.Heartbeat)
	}
	if strings.Join(s.Abilities.SecretNames, ",") != "mysql_shop,pg_main" || s.Abilities.ConfigFile != lt.cfg.SecretsPath {
		t.Errorf("abilities = %+v", s.Abilities)
	}
	if s.Agent.Version != lt.cfg.Version || s.Agent.StartedAt.IsZero() {
		t.Errorf("agent = %+v", s.Agent)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("the page still listens after the agent stopped")
	}
}

// TestLoopRefusedStatusPageKeepsTheRest: a refused section is reported as
// failed and the rest of the file still applies.
func TestLoopRefusedStatusPageKeepsTheRest(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	writeSecretsFile(t, lt.cfg.SecretsPath, twoSecrets+"status_page:\n  listen: \"0.0.0.0:8742\"\n")
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	cancel, done := runLoop(t, lt.newLoop())
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	hb := heartbeats(stream)[0]
	if hb.GetStatusPage() != agentpb.StatusPageMode_STATUS_PAGE_MODE_FAILED || hb.GetSecretsState() != "ok" || len(hb.GetSecretNames()) != 2 {
		t.Fatalf("heartbeat = %v", hb)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lt.logText(), "refused the status_page settings") {
		t.Errorf("log = %s", lt.logText())
	}
}

// TestStatusSnapshotShowsWhatWasSent: the snapshot carries every check's
// and probe's last results and the facts exactly as they went out —
// secrets replaced — and the buffer's count while disconnected.
func TestStatusSnapshotShowsWhatWasSent(t *testing.T) {
	lt, l := redactLoop(t)
	lt.facts.(*fakeCollector).facts = map[string]string{"motd": leaky("motd")}
	l.scheduler.Apply(checks.Config{Version: "v1", Checks: []checks.Spec{{Key: "cpu", Plugin: "cpu", Interval: time.Minute}}})
	target := uuid.New()
	l.probes.Apply([]probeconfig.Spec{{TargetHostID: target, CheckKey: "web", Plugin: "tcp", IntervalSeconds: 60, Address: "192.0.2.7", ParamsJSON: "{}"}})

	// Disconnected: into the buffer, but recorded as produced.
	l.handleResults([]checks.Result{{CheckKey: "cpu", Metric: "cpu.used_pct", Err: leaky("dial"), At: lt.now()}})
	l.handleProbeResults([]probes.Result{{TargetHostID: target, CheckKey: "web", Metric: "tcp.up", Value: floatPtr(1), At: lt.now()}})
	s := l.Snapshot()
	if s.Buffer.CheckResults != 1 || s.Buffer.ProbeResults != 1 || s.Buffer.Oldest == nil || s.Connection.Connected {
		t.Fatalf("buffer = %+v connected %v", s.Buffer, s.Connection.Connected)
	}
	if len(s.Checks) != 1 || s.Checks[0].Key != "cpu" || s.Checks[0].IntervalSeconds != 60 || len(s.Checks[0].Results) != 1 ||
		s.Checks[0].Results[0].Error != "dial [secret] tail" || s.Checks[0].LastRun == nil {
		t.Fatalf("checks = %+v", s.Checks)
	}
	if len(s.Probes.Assigned) != 1 || s.Probes.Assigned[0].Address != "192.0.2.7" || len(s.Probes.Assigned[0].Results) != 1 || !s.Probes.Enabled {
		t.Fatalf("probes = %+v", s.Probes)
	}

	// Connected: facts and the heartbeat as sent.
	stream := newFakeStream()
	sender := newStreamSender(stream)
	if err := l.activateSender(sender); err != nil {
		t.Fatal(err)
	}
	if err := l.pushFacts(context.Background(), sender); err != nil {
		t.Fatal(err)
	}
	if err := l.sendHeartbeat(sender); err != nil {
		t.Fatal(err)
	}
	s = l.Snapshot()
	if !s.Connection.Connected || s.Connection.ConnectedSince == nil || s.Buffer.CheckResults != 0 {
		t.Fatalf("connection %+v buffer %+v", s.Connection, s.Buffer)
	}
	if s.Facts.Values["motd"] != "motd [secret] tail" || s.Facts.SentAt == nil {
		t.Fatalf("facts = %+v", s.Facts)
	}
	if s.Heartbeat.SentAt == nil || s.Heartbeat.StatusPage != "off" || len(s.Heartbeat.SecretNames) != 2 {
		t.Fatalf("heartbeat = %+v", s.Heartbeat)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), secretValue) {
		t.Fatal("the snapshot carries a secret value")
	}

	// A check that stops is not shown again.
	l.scheduler.Apply(checks.Config{Version: "v2"})
	if s := l.Snapshot(); len(s.Checks) != 0 {
		t.Fatalf("checks after removal = %+v", s.Checks)
	}
}
