// The runner against the real database: definitions resolved through
// checks.Service, hosts read through hosts.Service, results landing in
// check_current, and the advisory lock excluding a second runner.
package probes_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probes"
	"github.com/noixdev/pulsix/internal/rules"
	"github.com/noixdev/pulsix/internal/schedule"
)

// roster is checks.HostRoster over hosts.Service, like internal/app's.
type roster struct{ hosts *hosts.Service }

func (r roster) MonitoredHosts(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error) {
	all, err := r.hosts.ListAllHosts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for _, h := range all {
		if h.Monitored {
			ids = append(ids, h.ID)
		}
	}
	return ids, nil
}

func TestRunnerProbesAndRecords(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	q := gen.New(d.Pool())
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "probes", Name: "probes"})
	if err != nil {
		t.Fatal(err)
	}
	zone, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Slug: "z", Name: "z"})
	if err != nil {
		t.Fatal(err)
	}
	host, err := q.CreateHost(ctx, gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone.ID, Name: "web",
		Address: "127.0.0.1", Monitored: true, Facts: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	checksSvc := checks.NewService(checks.NewSQLStore(d), checks.WithHostRoster(roster{hostsSvc}))
	scoped := db.WithOrgScope(ctx, org.ID)
	if _, err := checksSvc.CreateDefinition(scoped, org.ID, checks.DefinitionInput{
		Key: "site", Plugin: checks.PluginHTTP, Params: map[string]any{"url": "http://{address}:" + port + "/"},
		Interval: time.Minute, Enabled: true, BindingKind: checks.BindingHost, HostID: &host.ID,
	}); err != nil {
		t.Fatal(err)
	}

	first := probes.NewRunner(probes.Deps{
		Orgs: hostsSvc, Resolver: checksSvc, Hosts: hostsSvc, Recorder: checksSvc,
		Locker: schedule.NewAdvisoryLocker(d.Pool(), probes.AdvisoryKeyProbes),
		Guard:  probe.NewGuardForTest(true, nil),
		Tick:   20 * time.Millisecond,
	})
	runCtx, stopFirst := context.WithCancel(ctx)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		first.Run(runCtx)
	}()
	defer func() {
		stopFirst()
		<-firstDone
	}()

	// One tick probes the server; check_current then holds http.up = 1.
	var up float64
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := d.Pool().QueryRow(ctx, `SELECT value FROM check_current
			WHERE host_id = $1 AND check_key = 'site' AND item = '' AND metric = 'http.up'`, host.ID).Scan(&up)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("no http.up in check_current after 15 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if up != 1 {
		t.Fatalf("http.up = %v, want 1", up)
	}

	// A second runner on its own locker for the same key never probes
	// while the first holds the lock.
	var secondProbes atomic.Int32
	second := probes.NewRunner(probes.Deps{
		Orgs: hostsSvc, Resolver: checksSvc, Hosts: hostsSvc, Recorder: checksSvc,
		Locker: schedule.NewAdvisoryLocker(d.Pool(), probes.AdvisoryKeyProbes),
		Probe: func(context.Context, string, map[string]any, string, map[string]string) []probe.Observation {
			secondProbes.Add(1)
			return nil
		},
		Tick: 20 * time.Millisecond,
	})
	secondCtx, stopSecond := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stopSecond()
	second.Run(secondCtx)
	if n := secondProbes.Load(); n != 0 {
		t.Fatalf("the second runner probed %d times while the first held the lock", n)
	}

	// Stopping the first releases the lock.
	stopFirst()
	<-firstDone
	after := schedule.NewAdvisoryLocker(d.Pool(), probes.AdvisoryKeyProbes)
	locked, err := after.TryLock(ctx)
	if err != nil || !locked {
		t.Fatalf("after Run returned: TryLock = %v, %v; the lock must be released", locked, err)
	}
	after.Unlock(ctx)
}

// TestProbeResultDrivesARule is the chain end to end: a probe of a
// refused port records http.up = 0, which marks the host dirty, and the
// rules ticker (built as internal/app builds it) turns an http.up == 0
// rule's state crit.
func TestProbeResultDrivesARule(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close() // nothing listens: the probe is refused

	q := gen.New(d.Pool())
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "probes-rule", Name: "probes-rule"})
	if err != nil {
		t.Fatal(err)
	}
	zone, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Slug: "z", Name: "z"})
	if err != nil {
		t.Fatal(err)
	}
	host, err := q.CreateHost(ctx, gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone.ID, Name: "web",
		Address: "127.0.0.1", Monitored: true, Facts: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	checksSvc := checks.NewService(checks.NewSQLStore(d),
		checks.WithMatcher(app.NewChecksHostMatcher(hostsSvc)),
		checks.WithHostRoster(app.NewChecksHostRoster(hostsSvc)))
	rulesSvc := rules.NewService(rules.NewSQLStore(d))
	scoped := db.WithOrgScope(ctx, org.ID)
	if _, err := checksSvc.CreateDefinition(scoped, org.ID, checks.DefinitionInput{
		Key: checks.PluginHTTP, Plugin: checks.PluginHTTP,
		Params:   map[string]any{"url": "http://{address}:" + port + "/", "timeout_s": float64(2)},
		Interval: time.Minute, Enabled: true, BindingKind: checks.BindingHost, HostID: &host.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rulesSvc.Create(scoped, org.ID, rules.RuleInput{
		Name: "site down", Enabled: true, Scope: rules.ScopeOrg,
		Metric: "http.up", Severity: rules.SeverityCrit, Mode: "form",
		Form: &rules.Form{Metric: "http.up", Op: "==", Value: 0},
	}); err != nil {
		t.Fatal(err)
	}

	runner := probes.NewRunner(probes.Deps{
		Orgs: hostsSvc, Resolver: checksSvc, Hosts: hostsSvc, Recorder: checksSvc,
		Locker: schedule.NewAdvisoryLocker(d.Pool(), probes.AdvisoryKeyProbes),
		Guard:  probe.NewGuardForTest(true, nil),
		Tick:   20 * time.Millisecond, Log: quiet,
	})
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(runCtx)
	}()
	defer func() {
		stop()
		<-done
	}()

	ticker := rules.NewTicker(
		rules.NewEvalSQLStore(d, app.NewRulesHostChecks(checksSvc), items.NewSQLStore(d), rules.AgentOnlineWindow{Floor: agents.OnlineWindowFloor, HeartbeatsPerWindow: agents.HeartbeatsPerOnlineWindow}, quiet),
		alerts.NewService(alerts.NewSQLStore(d), time.Now),
		rules.WithTickerLogger(quiet),
	)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := ticker.Tick(ctx); err != nil {
			t.Fatalf("rules tick: %v", err)
		}
		var state string
		err := d.Pool().QueryRow(ctx, `SELECT state FROM check_states
			WHERE host_id = $1 AND check_key = 'http' AND item = '' AND metric = 'http.up'`, host.ID).Scan(&state)
		if err == nil && state == "crit" {
			return
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("http.up == 0 rule state = %q (err %v) after 15 s, want crit", state, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
