package rules_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/rules"
)

// TestSeedRulesCompileAgainstCatalogue is the test that matters most (the
// task brief's own words): every SeedRules() entry must compile through
// Form.Compile without error and name a metric the catalogue actually
// knows, so a typo here is caught by `go test`, not by an operator running
// the migration this set is also encoded into.
func TestSeedRulesCompileAgainstCatalogue(t *testing.T) {
	for _, in := range rules.SeedRules() {
		in := in
		t.Run(in.Name, func(t *testing.T) {
			entry, ok := rules.Catalogue()[in.Metric]
			if !ok {
				t.Fatalf("metric %q is not in Catalogue()", in.Metric)
			}
			if entry.CheckKey == "" {
				t.Fatalf("metric %q has no check key", in.Metric)
			}
			if in.Form == nil {
				t.Fatalf("entry %q has no form", in.Name)
			}
			if in.Form.Metric != in.Metric {
				t.Fatalf("form metric %q does not match rule metric %q", in.Form.Metric, in.Metric)
			}
			if _, _, err := in.Form.Compile(); err != nil {
				t.Fatalf("SeedRules() entry %q does not compile: %v", in.Name, err)
			}
		})
	}
}

// TestSeedRulesExcludesServiceState is backlog 4.3's standing ruling made
// executable: no seed entry may ever name service.state, since a default
// rule on a check that legitimately does not exist on every host would
// turn a harmless oddity into a page. This is what stops a later
// contributor from "completing" the set by adding one back.
func TestSeedRulesExcludesServiceState(t *testing.T) {
	for _, in := range rules.SeedRules() {
		if in.Metric == "service.state" {
			t.Fatalf("SeedRules() must never seed service.state, found entry %q", in.Name)
		}
	}
}

// TestSeedRulesExactSet pins the seed table verbatim: the name, metric,
// severity, trigger, clear condition and for-duration of every entry, so a
// future edit that silently changes a threshold (rather than deliberately
// updating both this test and the migrations) fails loudly. The first five
// rows are migration 00045's, the two script rows migration 00094's
// (backlog 3.6g), the two SNMP rows migration 00097's (backlog 3.13d), the seven synthetic-check rows migration 00104's (backlog 3.13b), the six
// assertion rows migration 00134's (backlog 3.87), the two snmp assertion
// rows migration 00135's (backlog 3.134).
func TestSeedRulesExactSet(t *testing.T) {
	want := []struct {
		name       string
		metric     string
		severity   rules.Severity
		op         string
		value      float64
		labels     []string
		clearOp    string
		clearValue float64
		forDur     time.Duration
	}{
		{"Disk usage warning", "disk.used_pct", rules.SeverityWarn, ">=", 85, nil, "<", 80, 0},
		{"Disk usage critical", "disk.used_pct", rules.SeverityCrit, ">=", 95, nil, "<", 90, 0},
		{"Memory usage warning", "mem.used_pct", rules.SeverityWarn, ">=", 90, nil, "", 0, 10 * time.Minute},
		{"Memory usage critical", "mem.used_pct", rules.SeverityCrit, ">=", 97, nil, "", 0, 5 * time.Minute},
		{"CPU usage warning", "cpu.used_pct", rules.SeverityWarn, ">=", 90, nil, "", 0, 15 * time.Minute},
		{"Script reports critical", "script.status", rules.SeverityCrit, "in", 0, []string{"crit"}, "", 0, 0},
		{"Script reports warning", "script.status", rules.SeverityWarn, "in", 0, []string{"warn", "unknown"}, "", 0, 0},
		{"SNMP device unreachable", "snmp.up", rules.SeverityCrit, "<", 1, nil, "", 0, 0},
		{"Interface down", "snmp.if.status", rules.SeverityWarn, "in", 0, []string{"down"}, "", 0, 0},
		{"Web assertion failed", "http.assertion", rules.SeverityWarn, "in", 0, []string{"warn"}, "", 0, 0},
		{"Web assertion failed (critical)", "http.assertion", rules.SeverityCrit, "in", 0, []string{"crit"}, "", 0, 0},
		{"Ping check down", "icmp.up", rules.SeverityCrit, "<", 1, nil, "", 0, 0},
		{"TCP check down", "tcp.up", rules.SeverityCrit, "<", 1, nil, "", 0, 0},
		{"HTTP check down", "http.up", rules.SeverityCrit, "<", 1, nil, "", 0, 0},
		{"TLS check down", "tls.up", rules.SeverityCrit, "<", 1, nil, "", 0, 0},
		{"DNS check down", "dns.up", rules.SeverityCrit, "<", 1, nil, "", 0, 0},
		{"TCP assertion failed", "tcp.assertion", rules.SeverityWarn, "in", 0, []string{"warn"}, "", 0, 0},
		{"TCP assertion failed (critical)", "tcp.assertion", rules.SeverityCrit, "in", 0, []string{"crit"}, "", 0, 0},
		{"DNS assertion failed", "dns.assertion", rules.SeverityWarn, "in", 0, []string{"warn"}, "", 0, 0},
		{"DNS assertion failed (critical)", "dns.assertion", rules.SeverityCrit, "in", 0, []string{"crit"}, "", 0, 0},
		{"TLS assertion failed", "tls.assertion", rules.SeverityWarn, "in", 0, []string{"warn"}, "", 0, 0},
		{"TLS assertion failed (critical)", "tls.assertion", rules.SeverityCrit, "in", 0, []string{"crit"}, "", 0, 0},
		{"SNMP assertion failed", "snmp.assertion", rules.SeverityWarn, "in", 0, []string{"warn"}, "", 0, 0},
		{"SNMP assertion failed (critical)", "snmp.assertion", rules.SeverityCrit, "in", 0, []string{"crit"}, "", 0, 0},
	}

	got := rules.SeedRules()
	if len(got) != len(want) {
		t.Fatalf("SeedRules() has %d entries, want exactly %d", len(got), len(want))
	}
	for i, w := range want {
		in := got[i]
		if in.Name != w.name || in.Metric != w.metric || in.Severity != w.severity {
			t.Fatalf("entry %d = (%q, metric %q, severity %q), want (%q, %q, %q)", i, in.Name, in.Metric, in.Severity, w.name, w.metric, w.severity)
		}
		if !in.Enabled {
			t.Fatalf("entry %d (%s %s) is not enabled", i, in.Metric, in.Severity)
		}
		if in.Scope != rules.ScopeOrg {
			t.Fatalf("entry %d (%s %s) scope = %q, want org", i, in.Metric, in.Severity, in.Scope)
		}
		if in.For != w.forDur {
			t.Fatalf("entry %d (%s %s) for = %s, want %s", i, in.Metric, in.Severity, in.For, w.forDur)
		}
		if in.Form == nil {
			t.Fatalf("entry %d (%s %s) has no form", i, in.Metric, in.Severity)
		}
		if in.Form.Op != w.op || in.Form.Value != w.value || !slices.Equal(in.Form.Labels, w.labels) {
			t.Fatalf("entry %d (%s %s) trigger = %s %v %v, want %s %v %v", i, in.Metric, in.Severity, in.Form.Op, in.Form.Value, in.Form.Labels, w.op, w.value, w.labels)
		}
		if in.Form.ClearOp != w.clearOp || (w.clearOp != "" && in.Form.ClearValue != w.clearValue) {
			t.Fatalf("entry %d (%s %s) clear = %s %v, want %s %v", i, in.Metric, in.Severity, in.Form.ClearOp, in.Form.ClearValue, w.clearOp, w.clearValue)
		}
	}
}

// TestSeedForOrgWritesTwentyFourSeedRows proves the service side of the seed: a
// fresh organization's rules, seeded through Service.SeedForOrg against an
// in-memory store (no database needed), are exactly the twenty-four SeedRules()
// entries, each org-scoped and stamped origin "seed".
func TestSeedForOrgWritesTwentyFourSeedRows(t *testing.T) {
	store := newFakeStore()
	svc := rules.NewService(store)
	orgID := uuid.Must(uuid.NewV7())

	if err := svc.SeedForOrg(context.Background(), orgID); err != nil {
		t.Fatalf("SeedForOrg: %v", err)
	}

	got, err := svc.List(context.Background(), orgID, rules.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 24 {
		t.Fatalf("seeded %d rules, want exactly 24: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, r := range got {
		if r.Origin != "seed" {
			t.Fatalf("rule %s/%s origin = %q, want seed", r.Metric, r.Severity, r.Origin)
		}
		if r.OrgID != orgID {
			t.Fatalf("rule %s/%s org = %s, want %s", r.Metric, r.Severity, r.OrgID, orgID)
		}
		if r.Scope != rules.ScopeOrg {
			t.Fatalf("rule %s/%s scope = %q, want org", r.Metric, r.Severity, r.Scope)
		}
		seen[r.Metric+"/"+string(r.Severity)] = true
	}
	for _, want := range []string{"disk.used_pct/warn", "disk.used_pct/crit", "mem.used_pct/warn", "mem.used_pct/crit", "cpu.used_pct/warn", "script.status/crit", "script.status/warn", "snmp.up/crit", "snmp.if.status/warn", "http.assertion/warn", "http.assertion/crit", "icmp.up/crit", "tcp.up/crit", "http.up/crit", "tls.up/crit", "dns.up/crit"} {
		if !seen[want] {
			t.Fatalf("seed set missing %q, got %+v", want, got)
		}
	}
}

// failingCreateStore wraps fakeStore, failing every Create from the nth
// call onward — the shape needed to prove SeedForOrg stops and propagates
// on a store failure partway through the twenty-four inserts, rather than
// swallowing it and leaving a partial seed set unreported.
type failingCreateStore struct {
	*fakeStore
	failFrom int
	calls    int
}

func (f *failingCreateStore) Create(ctx context.Context, r rules.Rule) error {
	f.calls++
	if f.calls >= f.failFrom {
		return errors.New("store exploded")
	}
	return f.fakeStore.Create(ctx, r)
}

// TestSeedForOrgPropagatesStoreError proves a store failure partway through
// SeedForOrg's twenty-four inserts is reported to the caller (CreateOrg's own
// propagation, tenancy.OrgCreatedHook) instead of silently leaving an
// organization with an incomplete seed set nobody is told about.
func TestSeedForOrgPropagatesStoreError(t *testing.T) {
	store := &failingCreateStore{fakeStore: newFakeStore(), failFrom: 3}
	svc := rules.NewService(store)

	err := svc.SeedForOrg(context.Background(), uuid.Must(uuid.NewV7()))
	if err == nil {
		t.Fatal("SeedForOrg with a failing store: got nil error")
	}
}
