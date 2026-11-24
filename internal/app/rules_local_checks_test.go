package app

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestAnOrgRuleJudgesALocalCheck (backlog 3.5a), through the wiring Build
// uses: a check defined in the host's agent.yaml reaches the resolver from
// the agent's stored heartbeat (NewChecksLocalChecks), the evaluator's
// context resolves it an interval (ContextForHost over set.All()), and the
// seeded organization rule on disk.used_pct judges its tuple crit like any
// other check's. Without the local checks wired, the same tuple has no
// interval and is not judged at all.
func TestAnOrgRuleJudgesALocalCheck(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org := seedOrg(t, d, "app-local-check-rule")
	scoped := db.WithOrgScope(ctx, org)
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	zone, err := hostsSvc.CreateZone(scoped, org, "z", "Z")
	if err != nil {
		t.Fatal(err)
	}
	host, err := hostsSvc.CreateHost(scoped, org, hosts.HostInput{Name: "db-1", ZoneID: zone.ID, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}

	agentsSvc := agents.NewService(agents.NewSQLStore(d))
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := agentsSvc.Register(scoped, agents.Registration{
		OrgID: org, HostID: host.ID, AgentName: "db-1", Version: "v1", Serial: "serial-local-rule",
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := agentsSvc.Heartbeat(ctx, "serial-local-rule", agents.HeartbeatReport{
		Version: "v1", SecretsState: "absent",
		LocalChecks: []agents.LocalCheckInput{{Key: "local-disk", Plugin: "disk", ParamsJSON: `{}`, IntervalSeconds: 60}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := rules.NewService(rules.NewSQLStore(d)).SeedForOrg(scoped, org); err != nil {
		t.Fatal(err)
	}

	base := []checks.Option{checks.WithMatcher(NewChecksHostMatcher(hostsSvc)), checks.WithHostRoster(NewChecksHostRoster(hostsSvc))}
	withLocal := checks.NewService(checks.NewSQLStore(d), append(base, checks.WithLocalChecks(NewChecksLocalChecks(agentsSvc)))...)
	withoutLocal := checks.NewService(checks.NewSQLStore(d), base...)

	tuple := rules.Tuple{CheckKey: "local-disk", Item: "/", Metric: "disk.used_pct"}
	value := 97.0
	if res, err := withLocal.RecordObservations(scoped, org, []checks.Observation{{
		HostID: host.ID, CheckKey: tuple.CheckKey, Item: tuple.Item, Metric: tuple.Metric, At: now.Add(-10 * time.Second), Value: &value,
	}}); err != nil || res.Accepted != 1 {
		t.Fatalf("record observation: %+v, %v", res, err)
	}

	evaluate := func(svc *checks.Service) (rules.TupleState, bool) {
		t.Helper()
		load, err := newEvalStore(d, NewRulesHostChecks(svc), items.NewSQLStore(d)).Load(ctx, rules.Due{OrgID: org, HostID: host.ID}, time.Now())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		res := rules.Evaluate(rules.Snapshot{
			Now: now, Tuples: load.Tuples, Effective: load.Effective, Facts: load.Facts, Previous: load.Previous,
			Changes30: load.Changes30, PreviousHost: load.PreviousHost, Ignored: load.Ignored, Presence: load.Presence,
		})
		for _, st := range res.States {
			if st.Tuple == tuple {
				return st, true
			}
		}
		return rules.TupleState{}, false
	}

	st, ok := evaluate(withLocal)
	if !ok || st.State != rules.StateCrit || st.RuleID == nil {
		t.Fatalf("local-disk at 97 %% = %+v (judged %v), want crit by the seeded org rule", st, ok)
	}
	if st, ok := evaluate(withoutLocal); ok {
		t.Fatalf("without local checks wired, local-disk was judged: %+v", st)
	}
}
