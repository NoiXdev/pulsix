package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestEvaluationCapturesWhyAnAlertFired drives the alert detail through
// the production path (backlog 3.13b): a real ticker, a real alerts.Service
// with this package's DetailSource over the real checks store and
// service, an http check with an assertion whose key is not the plugin's
// name. The down check's alert carries its failure reason, the failed
// assertion's alert its expression and the value seen, and a severity
// change re-captures it while an alert whose severity holds keeps its own.
func TestEvaluationCapturesWhyAnAlertFired(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d, "app-alert-detail")
	zone, err := gen.New(d.Pool()).CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: "z", Name: "z"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := gen.New(d.Pool()).CreateHost(ctx, gen.CreateHostParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "shop", Facts: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	host := h.ID
	if _, err := d.Pool().Exec(ctx, `UPDATE hosts SET monitored = true WHERE id = $1`, host); err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	checksStore := checks.NewSQLStore(d)
	checksSvc := checks.NewService(checksStore,
		checks.WithMatcher(NewChecksHostMatcher(hostsSvc)),
		checks.WithHostRoster(NewChecksHostRoster(hostsSvc)))
	rulesSvc := rules.NewService(rules.NewSQLStore(d))
	itemsStore := items.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	clock := func() time.Time { return now }
	alertsSvc := alerts.NewService(alerts.NewSQLStore(d), clock)
	alertsSvc.SetDetailSource(NewAlertDetails(checksStore, checksSvc), quiet)
	ticker := rules.NewTicker(
		rules.NewEvalSQLStore(d, NewRulesHostChecks(checksSvc), itemsStore, rulesAgentWindow(), quiet),
		alertsSvc, rules.WithTickerClock(clock), rules.WithTickerLogger(quiet))

	scoped := db.WithOrgScope(ctx, org)
	if _, err := checksSvc.CreateDefinition(scoped, org, checks.DefinitionInput{
		Key: "shop", Plugin: checks.PluginHTTP, Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg,
		Params: map[string]any{"url": "https://shop.example", "assertions": []any{"home=status == 200 @warn"}},
	}); err != nil {
		t.Fatalf("create http definition: %v", err)
	}
	for _, in := range []rules.RuleInput{
		{Name: "down", Metric: "http.up", Form: &rules.Form{Metric: "http.up", Op: "<", Value: 1}},
		{Name: "warn", Metric: "http.assertion", Form: &rules.Form{Metric: "http.assertion", Op: "in", Labels: []string{"warn"}}, Severity: rules.SeverityWarn},
		{Name: "crit", Metric: "http.assertion", Form: &rules.Form{Metric: "http.assertion", Op: "in", Labels: []string{"crit"}}},
	} {
		in.Enabled, in.Scope, in.Mode = true, rules.ScopeOrg, "form"
		if in.Severity == "" {
			in.Severity = rules.SeverityCrit
		}
		if _, err := rulesSvc.Create(scoped, org, in); err != nil {
			t.Fatalf("create rule %s: %v", in.Name, err)
		}
	}

	q := gen.New(d.Pool())
	report := func(item, metric string, value *float64, label string) {
		t.Helper()
		p := gen.UpsertCheckCurrentParams{OrgID: org, HostID: host, CheckKey: "shop", Item: item, Metric: metric, Value: value, At: now}
		if label != "" {
			p.Label = &label
		}
		if err := q.UpsertCheckCurrent(ctx, p); err != nil {
			t.Fatalf("upsert %s: %v", metric, err)
		}
	}
	tick := func() {
		t.Helper()
		if err := q.MarkHostDirty(ctx, gen.MarkHostDirtyParams{OrgID: org, HostID: host}); err != nil {
			t.Fatal(err)
		}
		if _, err := ticker.Tick(ctx); err != nil {
			t.Fatalf("tick: %v", err)
		}
	}
	details := func() map[string]string {
		t.Helper()
		views, _, err := alertsSvc.List(ctx, org, alerts.Filter{}, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, v := range views {
			if v.ClosedAt == nil {
				out[v.Subject.Metric] = v.Detail
			}
		}
		return out
	}
	zero := 0.0

	report("", "http.up", &zero, "")
	report("", "http.failure", nil, "timeout")
	report("home", "http.assertion", nil, "warn")
	report("home", "http.assertion_actual", nil, "503")
	tick()
	got := details()
	if got["http.up"] != "timeout" || got["http.assertion"] != "status == 200 — saw 503" {
		t.Fatalf("open alerts' details = %q, want the failure reason and the assertion", got)
	}

	// The assertion turns crit: the severity change re-captures the
	// detail with the new actual. The down alert's severity holds, so it
	// keeps the reason it opened with.
	now = now.Add(time.Minute)
	report("", "http.up", &zero, "")
	report("", "http.failure", nil, "none")
	report("home", "http.assertion", nil, "crit")
	report("home", "http.assertion_actual", nil, "500")
	tick()
	if got := details(); got["http.assertion"] != "status == 200 — saw 500" || got["http.up"] != "timeout" {
		t.Fatalf("details after the change = %q", got)
	}
}
