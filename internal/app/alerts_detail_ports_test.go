package app

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
)

type labelKey struct{ checkKey, item, metric string }

// fakeLabels serves current labels from a map and records the
// transaction each read ran on.
type fakeLabels struct {
	labels map[labelKey]string
	err    error
	txs    []pgx.Tx
}

func (f *fakeLabels) CurrentLabel(_ context.Context, tx pgx.Tx, _, _ uuid.UUID, checkKey, item, metric string) (string, error) {
	f.txs = append(f.txs, tx)
	return f.labels[labelKey{checkKey, item, metric}], f.err
}

// fakeHostChecks serves one host's resolved set and records whether it
// was asked on an org-scoped context.
type fakeHostChecks struct {
	set    checks.ResolvedSet
	err    error
	calls  int
	scoped bool
}

func (f *fakeHostChecks) ResolveHostWithLocal(ctx context.Context, orgID, _ uuid.UUID) (checks.ResolvedSet, error) {
	f.calls++
	got, ok := db.OrgScope(ctx)
	f.scoped = ok && got == orgID
	return f.set, f.err
}

// detailTx is a distinct pgx.Tx value: the adapter only passes it through.
type detailTx struct{ pgx.Tx }

func httpCheck(key string, assertions ...any) checks.Resolved {
	return checks.Resolved{Key: key, Plugin: checks.PluginHTTP, Params: map[string]any{
		"url": "https://example.com", "assertions": assertions,
	}}
}

func TestAlertDetailsSaysWhyAnAlertFired(t *testing.T) {
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	sub := func(checkKey, item, metric string) alerts.Subject {
		return alerts.Subject{HostID: host, Scope: alerts.ScopeTuple, CheckKey: checkKey, Item: item, Metric: metric}
	}
	set := checks.ResolvedSet{HostID: host, Synthetic: []checks.Resolved{
		httpCheck("shop", "home=status == 200", "title=html(\"h1\") contains \"Welcome\" @warn", "broken=nonsense"),
		{Key: "api", Plugin: checks.PluginTCP, Params: map[string]any{"assertions": []any{"home=status == 200"}}},
	}}
	labels := map[labelKey]string{
		{"ping", "", "icmp.failure"}:               "timeout",
		{"web", "", "tls.failure"}:                 "none",
		{"shop", "", "http.failure"}:               "certificate",
		{"shop", "home", "http.assertion_actual"}:  "503",
		{"shop", "title", "http.assertion_actual"}: `"Maintenance"`,
		{"shop", "gone", "http.assertion_actual"}:  "absent",
		{"api", "home", "http.assertion_actual"}:   "500",
		{"box", "", "cpu.failure"}:                 "timeout",
		{"box", "", "disk.used_pct"}:               "99",
	}

	for _, tc := range []struct {
		name string
		sub  alerts.Subject
		want string
	}{
		{"a down synthetic check says why", sub("ping", "", "icmp.up"), "timeout"},
		{"the failure of its own check key", sub("shop", "", "http.up"), "certificate"},
		{"none is no reason", sub("web", "", "tls.up"), ""},
		{"no failure reported", sub("dns", "", "dns.up"), ""},
		{"a failed assertion: expression and actual", sub("shop", "home", "http.assertion"), "status == 200 — saw 503"},
		{"the expression is canonical, without name or severity", sub("shop", "title", "http.assertion"), `html("h1") contains "Welcome" — saw "Maintenance"`},
		{"an assertion no longer defined: the actual alone", sub("shop", "gone", "http.assertion"), "saw absent"},
		{"an entry that does not parse, nothing reported: nothing", sub("shop", "broken", "http.assertion"), ""},
		{"a check key that is not http", sub("api", "home", "http.assertion"), "saw 500"},
		{"an agent plugin's up is not a synthetic failure", sub("box", "", "cpu.up"), ""},
		{"any other metric", sub("box", "", "disk.used_pct"), ""},
		{"a metric without a plugin", sub("box", "", "up"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := alertDetails{labels: &fakeLabels{labels: labels}, checks: &fakeHostChecks{set: set}}
			got, err := src.Lookup().Detail(context.Background(), detailTx{}, org, tc.sub)
			if err != nil || got != tc.want {
				t.Fatalf("Detail = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// An assertion whose actual has not been reported says its expression
// alone; one with neither says nothing.
func TestAlertDetailsAssertionWithoutAnActual(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	hc := &fakeHostChecks{set: checks.ResolvedSet{Synthetic: []checks.Resolved{httpCheck("shop", "home=status in 200..299")}}}
	src := alertDetails{labels: &fakeLabels{}, checks: hc}
	for item, want := range map[string]string{"home": "status in 200..299", "other": ""} {
		got, err := src.Lookup().Detail(context.Background(), detailTx{}, uuid.Must(uuid.NewV7()),
			alerts.Subject{HostID: host, Scope: alerts.ScopeTuple, CheckKey: "shop", Item: item, Metric: "http.assertion"})
		if err != nil || got != want {
			t.Errorf("item %s: Detail = %q, %v; want %q", item, got, err, want)
		}
	}
}

// Params decoded from JSON hold []any; a Go caller may hand []string.
func TestAlertDetailsReadsAStringList(t *testing.T) {
	c := httpCheck("shop")
	c.Params["assertions"] = []string{"home=status == 204"}
	src := alertDetails{labels: &fakeLabels{}, checks: &fakeHostChecks{set: checks.ResolvedSet{Synthetic: []checks.Resolved{c}}}}
	got, err := src.Lookup().Detail(context.Background(), detailTx{}, uuid.Must(uuid.NewV7()),
		alerts.Subject{Scope: alerts.ScopeTuple, CheckKey: "shop", Item: "home", Metric: "http.assertion"})
	if err != nil || got != "status == 204" {
		t.Fatalf("Detail = %q, %v", got, err)
	}
}

// The current values are read on the transaction Apply hands over (its
// savepoint); the check definitions resolve on an org-scoped context, the
// same way the evaluation's own load resolves them.
func TestAlertDetailsReadsOnTheSavepointAndResolvesScoped(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	tx := &detailTx{}
	labels := &fakeLabels{labels: map[labelKey]string{{"shop", "home", "http.assertion_actual"}: "503"}}
	hc := &fakeHostChecks{set: checks.ResolvedSet{Synthetic: []checks.Resolved{httpCheck("shop", "home=status == 200")}}}
	src := alertDetails{labels: labels, checks: hc}
	if _, err := src.Lookup().Detail(context.Background(), tx, org,
		alerts.Subject{Scope: alerts.ScopeTuple, CheckKey: "shop", Item: "home", Metric: "http.assertion"}); err != nil {
		t.Fatal(err)
	}
	if len(labels.txs) != 1 || labels.txs[0] != tx {
		t.Errorf("label reads ran on %v, want the savepoint", labels.txs)
	}
	if hc.calls != 1 || !hc.scoped {
		t.Errorf("resolve calls = %d, scoped to the organization = %v", hc.calls, hc.scoped)
	}

	// An up alert resolves nothing.
	hc.calls = 0
	if _, err := src.Lookup().Detail(context.Background(), tx, org, alerts.Subject{Scope: alerts.ScopeTuple, CheckKey: "ping", Metric: "icmp.up"}); err != nil {
		t.Fatal(err)
	}
	if hc.calls != 0 {
		t.Errorf("an up alert resolved the host's checks %d times", hc.calls)
	}
}

// A failing read is an error, which Apply logs and turns into no detail.
func TestAlertDetailsReportsFailures(t *testing.T) {
	boom := errors.New("boom")
	ctx := context.Background()
	up := alerts.Subject{Scope: alerts.ScopeTuple, CheckKey: "ping", Metric: "icmp.up"}
	assertion := alerts.Subject{Scope: alerts.ScopeTuple, CheckKey: "shop", Item: "home", Metric: "http.assertion"}

	src := alertDetails{labels: &fakeLabels{err: boom}, checks: &fakeHostChecks{}}
	if _, err := src.Lookup().Detail(ctx, detailTx{}, uuid.Nil, up); !errors.Is(err, boom) {
		t.Errorf("up with a failing label read: err = %v", err)
	}
	if _, err := src.Lookup().Detail(ctx, detailTx{}, uuid.Nil, assertion); !errors.Is(err, boom) {
		t.Errorf("assertion with a failing label read: err = %v", err)
	}
	src = alertDetails{labels: &fakeLabels{}, checks: &fakeHostChecks{err: boom}}
	if _, err := src.Lookup().Detail(ctx, detailTx{}, uuid.Nil, assertion); !errors.Is(err, boom) {
		t.Errorf("assertion with a failing resolve: err = %v", err)
	}
}

// One lookup resolves the host's checks once, however many assertions it
// is asked about; a failed resolution is not retried inside it. A new
// lookup (the next evaluation) resolves afresh.
func TestAlertDetailsResolvesOncePerLookup(t *testing.T) {
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hc := &fakeHostChecks{set: checks.ResolvedSet{Synthetic: []checks.Resolved{httpCheck("shop", "a=status == 200", "b=status == 201")}}}
	src := alertDetails{labels: &fakeLabels{}, checks: hc}
	lookup := src.Lookup()
	for item, want := range map[string]string{"a": "status == 200", "b": "status == 201"} {
		got, err := lookup.Detail(context.Background(), detailTx{}, org,
			alerts.Subject{HostID: host, Scope: alerts.ScopeTuple, CheckKey: "shop", Item: item, Metric: "http.assertion"})
		if err != nil || got != want {
			t.Errorf("item %s: Detail = %q, %v; want %q", item, got, err, want)
		}
	}
	if hc.calls != 1 {
		t.Errorf("resolved %d times in one lookup, want 1", hc.calls)
	}

	hc.calls, hc.err = 0, errors.New("boom")
	lookup = src.Lookup()
	for range 2 {
		if _, err := lookup.Detail(context.Background(), detailTx{}, org,
			alerts.Subject{HostID: host, Scope: alerts.ScopeTuple, CheckKey: "shop", Item: "a", Metric: "http.assertion"}); err == nil {
			t.Error("a failed resolution reported no error")
		}
	}
	if hc.calls != 1 {
		t.Errorf("a failed resolution was retried: %d calls in one lookup, want 1", hc.calls)
	}
}
