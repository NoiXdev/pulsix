package probes_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probes"
	"github.com/noixdev/pulsix/internal/schedule"
)

// TestRunnerVantageCheckEndToEnd wires the runner as internal/app does
// against the real database: a check naming a monitoring group gets its
// online, probe-capable agent assigned (never the target itself), the
// manager's probe lands as the vantage "manager" in probe_current and not
// directly in check_current, and the combined result (the manager up, the
// agent not reporting yet) is the check's http.up = 1.
func TestRunnerVantageCheckEndToEnd(t *testing.T) {
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
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "vantages", Name: "vantages"})
	if err != nil {
		t.Fatal(err)
	}
	zone, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Slug: "z", Name: "z"})
	if err != nil {
		t.Fatal(err)
	}
	newHost := func(name string) uuid.UUID {
		t.Helper()
		h, err := q.CreateHost(ctx, gen.CreateHostParams{
			ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, ZoneID: zone.ID, Name: name,
			Address: "127.0.0.1", Monitored: true, Facts: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return h.ID
	}
	target, agent := newHost("web"), newHost("probe-1")

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	scoped := db.WithOrgScope(ctx, org.ID)
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	for _, h := range []uuid.UUID{target, agent} {
		if _, err := hostsSvc.SetTags(scoped, org.ID, h, []hosts.Tag{{Key: "role", Value: "probe", Source: hosts.TagSourceManual}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	agentsSvc := agents.NewService(agents.NewSQLStore(d))
	for i, h := range []uuid.UUID{target, agent} {
		now := time.Now()
		if err := agentsSvc.Register(ctx, agents.Registration{
			OrgID: org.ID, HostID: h, AgentName: "agent-" + string(rune('a'+i)), Version: "v1", Serial: "77" + string(rune('0'+i)),
			NotBefore: now, NotAfter: now.Add(24 * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		if err := agentsSvc.SetProbesSupported(ctx, h, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE host_agents SET last_seen = now() WHERE org_id = $1`, org.ID); err != nil {
		t.Fatal(err)
	}

	checksSvc := checks.NewService(checks.NewSQLStore(d),
		checks.WithMatcher(app.NewChecksHostMatcher(hostsSvc)),
		checks.WithHostRoster(app.NewChecksHostRoster(hostsSvc)))
	group, err := checksSvc.CreateGroup(scoped, org.ID, checks.GroupInput{Name: "probes", Selector: `role = "probe"`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checksSvc.CreateDefinition(scoped, org.ID, checks.DefinitionInput{
		Key: "site", Plugin: checks.PluginHTTP,
		Params:   map[string]any{"url": "http://{address}:" + port + "/", "vantage_groups": []any{group.ID.String()}},
		Interval: time.Minute, Enabled: true, BindingKind: checks.BindingHost, HostID: &target,
	}); err != nil {
		t.Fatal(err)
	}

	runner := probes.NewRunner(probes.Deps{
		Orgs: hostsSvc, Resolver: checksSvc, Hosts: hostsSvc, Recorder: checksSvc,
		Locker: schedule.NewAdvisoryLocker(d.Pool(), probes.AdvisoryKeyProbes),
		Guard:  probe.NewGuardForTest(true, nil),
		Agents: agentsSvc, Groups: checksSvc, Vantages: probes.NewSQLStore(d),
		Tick: 20 * time.Millisecond, Log: quiet,
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

	var up float64
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := d.Pool().QueryRow(ctx, `SELECT value FROM check_current
			WHERE host_id = $1 AND check_key = 'site' AND item = '' AND metric = 'http.up'`, target).Scan(&up)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("no combined http.up in check_current after 15 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if up != 1 {
		t.Fatalf("combined http.up = %v, want 1", up)
	}
	stop()
	<-done

	rows, err := d.Pool().Query(ctx, `SELECT agent_host_id, params FROM probe_assignments WHERE target_host_id = $1 AND check_key = 'site'`, target)
	if err != nil {
		t.Fatal(err)
	}
	var assigned []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatal(err)
		}
		var params map[string]any
		if err := json.Unmarshal(raw, &params); err != nil {
			t.Fatal(err)
		}
		if _, ok := params["vantage_groups"]; ok || params["url"] == nil {
			t.Fatalf("assignment params = %v, want the check's params without the vantage params", params)
		}
		assigned = append(assigned, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(assigned) != 1 || assigned[0] != agent {
		t.Fatalf("assigned agents = %v, want only %v (never the target)", assigned, agent)
	}
	var managerUp float64
	if err := d.Pool().QueryRow(ctx, `SELECT value FROM probe_current
		WHERE host_id = $1 AND check_key = 'site' AND vantage = 'manager' AND item = '' AND metric = 'http.up'`, target).Scan(&managerUp); err != nil || managerUp != 1 {
		t.Fatalf("manager vantage http.up = %v (%v), want 1", managerUp, err)
	}
}
