package rules_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/rules"
)

// The tests in this file pin backlog 3.13e: org, group and host rules
// judge every check that reports their metric, whatever its key, and an
// item rule names the check's key (ItemCheckKey), the plugin's own when
// empty.

// nginxHost is one host running three nginx checks under three keys and an
// apache check, each reporting its plugin's "up" metric.
func nginxHost(host uuid.UUID, groups ...rules.GroupRank) rules.HostContext {
	return rules.HostContext{HostID: host, Groups: groups, Tuples: []rules.Tuple{
		{CheckKey: "nginx", Metric: "nginx.up"},
		{CheckKey: "nginx-internal", Metric: "nginx.up"},
		{CheckKey: "nginx-2", Metric: "nginx.up"},
		{CheckKey: "apache", Metric: "apache.up"},
	}}
}

// effectiveKeys reduces got to the check keys of its tuples for metric, in
// Resolve's own order, each with its winner's id.
func effectiveKeys(got []rules.Effective, metric string) map[string]uuid.UUID {
	out := map[string]uuid.UUID{}
	for _, e := range got {
		if e.Tuple.Metric == metric {
			out[e.Tuple.CheckKey] = e.Winner.ID
		}
	}
	return out
}

func TestResolveNonItemRulesMatchEveryKeyOfTheirMetric(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	group := rules.GroupRank{ID: uuid.Must(uuid.NewV7()), CreatedAt: fixedTime(1)}

	orgRule := baseRule("nginx", "nginx.up", rules.SeverityCrit)
	groupRule := baseRule("nginx", "nginx.up", rules.SeverityCrit)
	groupRule.Scope, groupRule.GroupID = rules.ScopeGroup, &group.ID
	hostRule := baseRule("nginx", "nginx.up", rules.SeverityCrit)
	hostRule.Scope, hostRule.HostID = rules.ScopeHost, &host

	for _, tt := range []struct {
		name string
		rule rules.Rule
	}{
		{"org", orgRule},
		{"group", groupRule},
		{"host", hostRule},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := rules.Resolve([]rules.Rule{tt.rule}, nginxHost(host, group))
			keys := effectiveKeys(got, "nginx.up")
			for _, k := range []string{"nginx", "nginx-internal", "nginx-2"} {
				if keys[k] != tt.rule.ID {
					t.Errorf("tuple under key %q: winner %v, want the %s rule %v", k, keys[k], tt.name, tt.rule.ID)
				}
			}
			if len(got) != 3 {
				t.Fatalf("got %d effectives, want 3 (the apache tuple is another plugin's metric): %+v", len(got), got)
			}
			for _, e := range got {
				if e.Tuple.Metric != "nginx.up" || !e.Observed || len(e.Shadowed) != 0 {
					t.Fatalf("effective %+v: want only nginx.up tuples, observed, unshadowed", e)
				}
			}
		})
	}
}

// TestResolveNonItemRulesNeverReachAnotherPluginsMetric is review focus 1:
// widening the match to every key must not widen it to another plugin.
func TestResolveNonItemRulesNeverReachAnotherPluginsMetric(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	r := baseRule("apache", "apache.up", rules.SeverityCrit)

	got := rules.Resolve([]rules.Rule{r}, nginxHost(host))
	if len(got) != 1 || got[0].Tuple != (rules.Tuple{CheckKey: "apache", Metric: "apache.up"}) {
		t.Fatalf("got %+v, want exactly the apache tuple", got)
	}
}

// TestResolveScopeFiltersStillHoldUnderAnyKey is review focus 1 again: a
// host rule for another host and a group rule for a group this host is not
// in reach no tuple, whatever its key.
func TestResolveScopeFiltersStillHoldUnderAnyKey(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	other := uuid.Must(uuid.NewV7())
	foreignGroup := uuid.Must(uuid.NewV7())

	otherHostRule := baseRule("nginx", "nginx.up", rules.SeverityCrit)
	otherHostRule.Scope, otherHostRule.HostID = rules.ScopeHost, &other
	groupRule := baseRule("nginx", "nginx.up", rules.SeverityCrit)
	groupRule.Scope, groupRule.GroupID = rules.ScopeGroup, &foreignGroup
	otherHostItem := baseRule("nginx", "nginx.up", rules.SeverityCrit)
	otherHostItem.Scope, otherHostItem.HostID, otherHostItem.ItemCheckKey = rules.ScopeItem, &other, "nginx-internal"

	got := rules.Resolve([]rules.Rule{otherHostRule, groupRule, otherHostItem}, nginxHost(host))
	if len(got) != 0 {
		t.Fatalf("got %+v, want no effective at all", got)
	}
}

// TestResolveHostRuleBeatsOrgRuleOnANonDefaultKey is review focus 2: the
// deeper scope still wins on a tuple under a key other than the plugin's,
// and every key is its own tuple with its own decision.
func TestResolveHostRuleBeatsOrgRuleOnANonDefaultKey(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	orgRule := baseRule("nginx", "nginx.up", rules.SeverityCrit)
	hostRule := baseRule("nginx", "nginx.up", rules.SeverityCrit)
	hostRule.Scope, hostRule.HostID = rules.ScopeHost, &host
	hostRule.CreatedAt = fixedTime(2) // younger, so only scope can make it win

	got := rules.Resolve([]rules.Rule{orgRule, hostRule}, nginxHost(host))
	if len(got) != 3 {
		t.Fatalf("got %d effectives, want one per nginx key: %+v", len(got), got)
	}
	for _, e := range got {
		if e.Winner.ID != hostRule.ID || len(e.Shadowed) != 1 || e.Shadowed[0].ID != orgRule.ID {
			t.Fatalf("tuple %+v: winner %v shadowed %+v, want the host rule over the org rule", e.Tuple, e.Winner.ID, e.Shadowed)
		}
	}
}

func TestResolveItemRuleNamesItsCheckKey(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	hc := rules.HostContext{HostID: host, Tuples: []rules.Tuple{
		{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"},
		{CheckKey: "disk-data", Item: "/", Metric: "disk.used_pct"},
	}}
	orgRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)

	t.Run("with item_check_key it judges only that key's tuple", func(t *testing.T) {
		item := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
		item.Scope, item.HostID, item.Item, item.ItemCheckKey = rules.ScopeItem, &host, "/", "disk-data"

		keys := effectiveKeys(rules.Resolve([]rules.Rule{orgRule, item}, hc), "disk.used_pct")
		if keys["disk-data"] != item.ID {
			t.Errorf("disk-data winner = %v, want the item rule", keys["disk-data"])
		}
		if keys["disk"] != orgRule.ID {
			t.Errorf("disk winner = %v, want the org rule: the item rule names another key", keys["disk"])
		}
	})

	t.Run("without item_check_key it judges the plugin's key", func(t *testing.T) {
		item := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
		item.Scope, item.HostID, item.Item = rules.ScopeItem, &host, "/"

		keys := effectiveKeys(rules.Resolve([]rules.Rule{orgRule, item}, hc), "disk.used_pct")
		if keys["disk"] != item.ID {
			t.Errorf("disk winner = %v, want the item rule", keys["disk"])
		}
		if keys["disk-data"] != orgRule.ID {
			t.Errorf("disk-data winner = %v, want the org rule", keys["disk-data"])
		}
	})
}

// TestResolveItemRuleForAMissingKeyIsUnobserved is review focus 3: an item
// rule naming a key the host does not have still appears, Observed false.
func TestResolveItemRuleForAMissingKeyIsUnobserved(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	item := baseRule("nginx", "nginx.up", rules.SeverityCrit)
	item.Scope, item.HostID, item.Item, item.ItemCheckKey = rules.ScopeItem, &host, "x", "nginx-gone"

	got := rules.Resolve([]rules.Rule{item}, nginxHost(host))
	if len(got) != 1 {
		t.Fatalf("got %+v, want exactly the item rule's own tuple", got)
	}
	want := rules.Tuple{CheckKey: "nginx-gone", Item: "x", Metric: "nginx.up"}
	if got[0].Tuple != want || got[0].Observed || got[0].Winner.ID != item.ID {
		t.Fatalf("got %+v, want %+v unobserved, won by the item rule", got[0], want)
	}
}
