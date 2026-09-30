package rules_test

import (
	"context"
	"errors"
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

// TestSeedRulesExactSet pins the brief's own five-row table verbatim: the
// metric, severity, trigger, clear condition and for-duration of every
// entry, so a future edit that silently changes a threshold (rather than
// deliberately updating both this test and the migration) fails loudly.
func TestSeedRulesExactSet(t *testing.T) {
	want := []struct {
		metric     string
		severity   rules.Severity
		op         string
		value      float64
		clearOp    string
		clearValue float64
		forDur     time.Duration
	}{
		{"disk.used_pct", rules.SeverityWarn, ">=", 85, "<", 80, 0},
		{"disk.used_pct", rules.SeverityCrit, ">=", 95, "<", 90, 0},
		{"mem.used_pct", rules.SeverityWarn, ">=", 90, "", 0, 10 * time.Minute},
		{"mem.used_pct", rules.SeverityCrit, ">=", 97, "", 0, 5 * time.Minute},
		{"cpu.used_pct", rules.SeverityWarn, ">=", 90, "", 0, 15 * time.Minute},
	}

	got := rules.SeedRules()
	if len(got) != len(want) {
		t.Fatalf("SeedRules() has %d entries, want exactly %d", len(got), len(want))
	}
	for i, w := range want {
		in := got[i]
		if in.Metric != w.metric || in.Severity != w.severity {
			t.Fatalf("entry %d = (metric %q, severity %q), want (%q, %q)", i, in.Metric, in.Severity, w.metric, w.severity)
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
		if in.Form.Op != w.op || in.Form.Value != w.value {
			t.Fatalf("entry %d (%s %s) trigger = %s %v, want %s %v", i, in.Metric, in.Severity, in.Form.Op, in.Form.Value, w.op, w.value)
		}
		if in.Form.ClearOp != w.clearOp || (w.clearOp != "" && in.Form.ClearValue != w.clearValue) {
			t.Fatalf("entry %d (%s %s) clear = %s %v, want %s %v", i, in.Metric, in.Severity, in.Form.ClearOp, in.Form.ClearValue, w.clearOp, w.clearValue)
		}
	}
}

// TestSeedForOrgWritesFiveSeedRows proves the service side of Task 5: a
// fresh organization's rules, seeded through Service.SeedForOrg against an
// in-memory store (no database needed), are exactly the five SeedRules()
// entries, each org-scoped and stamped origin "seed".
func TestSeedForOrgWritesFiveSeedRows(t *testing.T) {
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
	if len(got) != 5 {
		t.Fatalf("seeded %d rules, want exactly 5: %+v", len(got), got)
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
	for _, want := range []string{"disk.used_pct/warn", "disk.used_pct/crit", "mem.used_pct/warn", "mem.used_pct/crit", "cpu.used_pct/warn"} {
		if !seen[want] {
			t.Fatalf("seed set missing %q, got %+v", want, got)
		}
	}
}

// failingCreateStore wraps fakeStore, failing every Create from the nth
// call onward — the shape needed to prove SeedForOrg stops and propagates
// on a store failure partway through the five inserts, rather than
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
// SeedForOrg's five inserts is reported to the caller (CreateOrg's own
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
