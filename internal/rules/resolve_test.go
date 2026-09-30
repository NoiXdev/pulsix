package rules_test

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/rules"
)

// fixedTime gives every resolve test a distinct, comparable timestamp
// without depending on time.Now or the system clock — n orders the
// timestamps (fixedTime(1) is before fixedTime(2)), the same helper
// internal/checks/resolver_test.go uses for its own tie-break tests.
func fixedTime(n int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Hour)
}

// baseRule returns an enabled, org-scoped rule for (checkKey, metric,
// severity) with a fresh id and fixedTime(1) timestamps — the common
// starting point every test below narrows with its own scope fields.
func baseRule(checkKey, metric string, severity rules.Severity) rules.Rule {
	return rules.Rule{
		ID:        uuid.Must(uuid.NewV7()),
		OrgID:     uuid.Must(uuid.NewV7()),
		Name:      "test rule",
		Enabled:   true,
		Origin:    "user",
		Scope:     rules.ScopeOrg,
		CheckKey:  checkKey,
		Metric:    metric,
		Severity:  severity,
		Mode:      "expression",
		Source:    checkKey + "." + metric,
		CreatedAt: fixedTime(1),
		UpdatedAt: fixedTime(1),
	}
}

func TestResolveOrgOnly(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	r := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	hc := rules.HostContext{HostID: host, Tuples: []rules.Tuple{tuple}}

	got := rules.Resolve([]rules.Rule{r}, hc)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.Tuple != tuple || e.Severity != rules.SeverityWarn || e.Winner.ID != r.ID || len(e.Shadowed) != 0 || !e.Observed {
		t.Fatalf("got %+v, want the org rule to win alone, observed", e)
	}
}

func TestResolveGroupOverridesOrg(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	hc := rules.HostContext{
		HostID: host,
		Groups: []rules.GroupRank{{ID: uuid.Must(uuid.NewV7()), Priority: 0, CreatedAt: fixedTime(1)}},
		Tuples: []rules.Tuple{tuple},
	}
	orgRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	groupRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	groupRule.Scope, groupRule.GroupID = rules.ScopeGroup, &hc.Groups[0].ID

	got := rules.Resolve([]rules.Rule{orgRule, groupRule}, hc)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.Winner.ID != groupRule.ID {
		t.Fatalf("winner = %s, want the group rule %s", e.Winner.ID, groupRule.ID)
	}
	if len(e.Shadowed) != 1 || e.Shadowed[0].ID != orgRule.ID {
		t.Fatalf("shadowed = %+v, want exactly the org rule", e.Shadowed)
	}
}

func TestResolveHostOverridesGroup(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	groupID := uuid.Must(uuid.NewV7())
	hc := rules.HostContext{
		HostID: host,
		Groups: []rules.GroupRank{{ID: groupID, Priority: 100, CreatedAt: fixedTime(1)}},
		Tuples: []rules.Tuple{tuple},
	}
	groupRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	groupRule.Scope, groupRule.GroupID = rules.ScopeGroup, &groupID
	hostRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	hostRule.Scope, hostRule.HostID = rules.ScopeHost, &host

	got := rules.Resolve([]rules.Rule{groupRule, hostRule}, hc)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.Winner.ID != hostRule.ID {
		t.Fatalf("winner = %s, want the host rule %s (host beats group regardless of priority)", e.Winner.ID, hostRule.ID)
	}
	if len(e.Shadowed) != 1 || e.Shadowed[0].ID != groupRule.ID {
		t.Fatalf("shadowed = %+v, want exactly the group rule", e.Shadowed)
	}
}

func TestResolveItemOverridesHost(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	hc := rules.HostContext{HostID: host, Tuples: []rules.Tuple{tuple}}
	hostRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	hostRule.Scope, hostRule.HostID = rules.ScopeHost, &host
	itemRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	itemRule.Scope, itemRule.HostID, itemRule.Item = rules.ScopeItem, &host, "/"

	got := rules.Resolve([]rules.Rule{hostRule, itemRule}, hc)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.Winner.ID != itemRule.ID {
		t.Fatalf("winner = %s, want the item rule %s", e.Winner.ID, itemRule.ID)
	}
	if len(e.Shadowed) != 1 || e.Shadowed[0].ID != hostRule.ID {
		t.Fatalf("shadowed = %+v, want exactly the host rule", e.Shadowed)
	}
	if !e.Observed {
		t.Fatal("observed = false, want true: the item's tuple is in host.Tuples")
	}
}

func TestResolveTwoGroupsHigherPriorityWins(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	low := rules.GroupRank{ID: uuid.Must(uuid.NewV7()), Priority: 1, CreatedAt: fixedTime(1)}
	high := rules.GroupRank{ID: uuid.Must(uuid.NewV7()), Priority: 5, CreatedAt: fixedTime(1)}
	hc := rules.HostContext{HostID: host, Groups: []rules.GroupRank{low, high}, Tuples: []rules.Tuple{tuple}}

	lowRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	lowRule.Scope, lowRule.GroupID = rules.ScopeGroup, &low.ID
	highRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	highRule.Scope, highRule.GroupID = rules.ScopeGroup, &high.ID

	got := rules.Resolve([]rules.Rule{lowRule, highRule}, hc)
	if len(got) != 1 || got[0].Winner.ID != highRule.ID {
		t.Fatalf("got %+v, want the higher-priority group's rule to win", got)
	}
	if len(got[0].Shadowed) != 1 || got[0].Shadowed[0].ID != lowRule.ID {
		t.Fatalf("shadowed = %+v, want exactly the low-priority group's rule", got[0].Shadowed)
	}
}

// TestResolveTwoGroupsEqualPriorityOlderWins deliberately gives the older
// group the *larger* id and the newer group the *smaller* one — the
// opposite of what uuid.NewV7's monotonic ordering would naturally hand
// out — so this only passes by actually consulting CreatedAt, not by the
// id tie-break coincidentally agreeing with it (mirrors
// internal/checks/resolver_test.go's TestResolveHostTwoGroupsEqualPriorityOlderWins).
func TestResolveTwoGroupsEqualPriorityOlderWins(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	older := rules.GroupRank{
		ID:       uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff"),
		Priority: 5, CreatedAt: fixedTime(1),
	}
	newer := rules.GroupRank{
		ID:       uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Priority: 5, CreatedAt: fixedTime(2),
	}
	hc := rules.HostContext{HostID: host, Groups: []rules.GroupRank{older, newer}, Tuples: []rules.Tuple{tuple}}

	olderRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	olderRule.Scope, olderRule.GroupID = rules.ScopeGroup, &older.ID
	newerRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	newerRule.Scope, newerRule.GroupID = rules.ScopeGroup, &newer.ID

	got := rules.Resolve([]rules.Rule{olderRule, newerRule}, hc)
	if len(got) != 1 || got[0].Winner.ID != olderRule.ID {
		t.Fatalf("got %+v, want the older group's rule to win an equal-priority tie", got)
	}
}

// TestResolveTwoGroupsEqualPriorityAndCreatedAtAcross100Runs is the
// determinism test (review focus 1): two groups tied on both Priority and
// CreatedAt must resolve to the same winner — the smaller id — every time,
// regardless of the order the rules and groups happen to be given in. The
// input slices are shuffled with a seeded RNG before every one of the 100
// calls (Fisher-Yates via rand.Shuffle), and the test asserts that the
// shuffle actually produced more than one distinct order before trusting
// the "always identical" assertion that follows — otherwise a resolver
// that silently depended on slice order could pass by accident.
func TestResolveTwoGroupsEqualPriorityAndCreatedAtAcross100Runs(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	tie := fixedTime(1)
	groupA := rules.GroupRank{ID: uuid.Must(uuid.NewV7()), Priority: 5, CreatedAt: tie}
	groupB := rules.GroupRank{ID: uuid.Must(uuid.NewV7()), Priority: 5, CreatedAt: tie}

	ruleA := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	ruleA.Scope, ruleA.GroupID = rules.ScopeGroup, &groupA.ID
	ruleB := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	ruleB.Scope, ruleB.GroupID = rules.ScopeGroup, &groupB.ID

	wantWinner := ruleA.ID
	if bytes.Compare(groupB.ID[:], groupA.ID[:]) < 0 {
		wantWinner = ruleB.ID
	}

	rnd := rand.New(rand.NewSource(1))
	var firstResult []rules.Effective
	orders := map[string]bool{}
	for i := range 100 {
		inputRules := []rules.Rule{ruleA, ruleB}
		groups := []rules.GroupRank{groupA, groupB}
		rnd.Shuffle(len(inputRules), func(a, b int) { inputRules[a], inputRules[b] = inputRules[b], inputRules[a] })
		rnd.Shuffle(len(groups), func(a, b int) { groups[a], groups[b] = groups[b], groups[a] })
		orders[inputRules[0].ID.String()+"|"+groups[0].ID.String()] = true

		hc := rules.HostContext{HostID: host, Groups: groups, Tuples: []rules.Tuple{tuple}}
		got := rules.Resolve(inputRules, hc)
		if len(got) != 1 {
			t.Fatalf("run %d: len(got) = %d, want 1: %+v", i, len(got), got)
		}
		if got[0].Winner.ID != wantWinner {
			t.Fatalf("run %d: winner = %s, want the smaller-id group's rule %s", i, got[0].Winner.ID, wantWinner)
		}
		if i == 0 {
			firstResult = got
		} else if !reflect.DeepEqual(got, firstResult) {
			t.Fatalf("run %d: result diverged from run 0:\n  run 0: %+v\n  run %d: %+v", i, firstResult, i, got)
		}
	}
	if len(orders) < 2 {
		t.Fatalf("shuffling never varied the input order across 100 runs (%d distinct orders) — the determinism assertion above would not catch an order-dependent bug", len(orders))
	}
}

// TestResolveSeveritiesResolveIndependently is the brief's own example: a
// host-scoped warn and an org-scoped crit for the same metric must both
// survive as their own Effective, neither shadowing the other.
func TestResolveSeveritiesResolveIndependently(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	hc := rules.HostContext{HostID: host, Tuples: []rules.Tuple{tuple}}

	warnRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	warnRule.Scope, warnRule.HostID = rules.ScopeHost, &host
	critRule := baseRule("disk", "disk.used_pct", rules.SeverityCrit)

	got := rules.Resolve([]rules.Rule{warnRule, critRule}, hc)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (warn and crit resolve independently): %+v", len(got), got)
	}
	bySeverity := map[rules.Severity]rules.Effective{}
	for _, e := range got {
		bySeverity[e.Severity] = e
	}
	if w, ok := bySeverity[rules.SeverityWarn]; !ok || w.Winner.ID != warnRule.ID || len(w.Shadowed) != 0 {
		t.Fatalf("warn effective = %+v, want the host rule to win alone", w)
	}
	if c, ok := bySeverity[rules.SeverityCrit]; !ok || c.Winner.ID != critRule.ID || len(c.Shadowed) != 0 {
		t.Fatalf("crit effective = %+v, want the org rule to win alone", c)
	}
}

func TestResolveDisabledRuleDoesNotShadow(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	groupID := uuid.Must(uuid.NewV7())
	hc := rules.HostContext{
		HostID: host,
		Groups: []rules.GroupRank{{ID: groupID, Priority: 0, CreatedAt: fixedTime(1)}},
		Tuples: []rules.Tuple{tuple},
	}
	groupRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	groupRule.Scope, groupRule.GroupID = rules.ScopeGroup, &groupID
	hostRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	hostRule.Scope, hostRule.HostID, hostRule.Enabled = rules.ScopeHost, &host, false

	got := rules.Resolve([]rules.Rule{groupRule, hostRule}, hc)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.Winner.ID != groupRule.ID {
		t.Fatalf("winner = %s, want the group rule to win once the host rule is disabled", e.Winner.ID)
	}
	if len(e.Shadowed) != 0 {
		t.Fatalf("shadowed = %+v, want none — a disabled rule must not shadow anything", e.Shadowed)
	}
}

// TestResolveItemRuleForVanishedItemYieldsUnobserved is review focus 6: an
// item-scoped rule whose item the host no longer reports still produces an
// Effective, labelled Observed: false, rather than vanishing from the view.
func TestResolveItemRuleForVanishedItemYieldsUnobserved(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	// host.Tuples reports a different mount; "/backup" is gone.
	hc := rules.HostContext{
		HostID: host,
		Tuples: []rules.Tuple{{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}},
	}
	itemRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	itemRule.Scope, itemRule.HostID, itemRule.Item = rules.ScopeItem, &host, "/backup"

	got := rules.Resolve([]rules.Rule{itemRule}, hc)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	e := got[0]
	wantTuple := rules.Tuple{CheckKey: "disk", Item: "/backup", Metric: "disk.used_pct"}
	if e.Tuple != wantTuple {
		t.Fatalf("tuple = %+v, want the rule's own tuple %+v", e.Tuple, wantTuple)
	}
	if e.Observed {
		t.Fatal("observed = true, want false: /backup is not in host.Tuples")
	}
	if e.Winner.ID != itemRule.ID || len(e.Shadowed) != 0 {
		t.Fatalf("got %+v, want the item rule to win alone", e)
	}
}

// TestResolveRuleWhoseMetricNoTupleReportsYieldsNoEffective: an org-scoped
// rule for a metric the host does not currently report at all produces no
// Effective — there is nothing for it to judge, and (unlike an item rule)
// it names no specific tuple that could survive on its own.
func TestResolveRuleWhoseMetricNoTupleReportsYieldsNoEffective(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	hc := rules.HostContext{
		HostID: host,
		Tuples: []rules.Tuple{{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}},
	}
	r := baseRule("memory", "mem.used_pct", rules.SeverityWarn)

	got := rules.Resolve([]rules.Rule{r}, hc)
	if len(got) != 0 {
		t.Fatalf("got %+v, want no Effective at all", got)
	}
}

// TestResolveHostRuleForAnotherHostDoesNotApply and
// TestResolveGroupRuleForNonMatchingGroupDoesNotApply pin the filtering
// HostContext exists to express: Resolve is handed an organization's whole
// rule set (not pre-filtered per host by its caller), so it must ignore a
// host-scoped rule bound to a different host and a group-scoped rule bound
// to a group that does not currently match this host (absent from
// host.Groups) on its own.
func TestResolveHostRuleForAnotherHostDoesNotApply(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	otherHost := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	hc := rules.HostContext{HostID: host, Tuples: []rules.Tuple{tuple}}
	otherHostRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	otherHostRule.Scope, otherHostRule.HostID = rules.ScopeHost, &otherHost

	got := rules.Resolve([]rules.Rule{otherHostRule}, hc)
	if len(got) != 0 {
		t.Fatalf("got %+v, want a rule bound to a different host to be ignored", got)
	}
}

func TestResolveGroupRuleForNonMatchingGroupDoesNotApply(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	hc := rules.HostContext{HostID: host, Tuples: []rules.Tuple{tuple}} // no Groups
	notMatchingGroupID := uuid.Must(uuid.NewV7())
	groupRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	groupRule.Scope, groupRule.GroupID = rules.ScopeGroup, &notMatchingGroupID

	got := rules.Resolve([]rules.Rule{groupRule}, hc)
	if len(got) != 0 {
		t.Fatalf("got %+v, want a rule bound to a group that does not match this host to be ignored", got)
	}
}

// TestResolveItemRuleForAnotherHostDoesNotApply mirrors
// TestResolveHostRuleForAnotherHostDoesNotApply for scope item: an
// item-scoped rule is still bound to one host, and a host's own
// HostContext must never see another host's item rule.
func TestResolveItemRuleForAnotherHostDoesNotApply(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	otherHost := uuid.Must(uuid.NewV7())
	hc := rules.HostContext{HostID: host, Tuples: []rules.Tuple{{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}}}
	otherHostItemRule := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	otherHostItemRule.Scope, otherHostItemRule.HostID, otherHostItemRule.Item = rules.ScopeItem, &otherHost, "/"

	got := rules.Resolve([]rules.Rule{otherHostItemRule}, hc)
	if len(got) != 0 {
		t.Fatalf("got %+v, want an item rule bound to a different host to be ignored", got)
	}
}

// TestResolveGroupRuleWithNilGroupIDDoesNotApply is a defensive-code test:
// Service.buildRule's own validation never produces a group-scoped Rule
// with a nil GroupID (the same way checks.normalizeDefinition guarantees
// its own BindingGroup rows always carry a GroupID — see
// loadResolveBundle's "unreachable given normalizeDefinition, guarded
// rather than trusted" comment), but Resolve is an exported pure function
// callable with a hand-built Rule too, so it must not panic on one.
func TestResolveGroupRuleWithNilGroupIDDoesNotApply(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	hc := rules.HostContext{HostID: host, Tuples: []rules.Tuple{{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}}}
	r := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	r.Scope = rules.ScopeGroup // GroupID left nil

	got := rules.Resolve([]rules.Rule{r}, hc)
	if len(got) != 0 {
		t.Fatalf("got %+v, want a group-scoped rule with no GroupID to be ignored, not to panic", got)
	}
}

// TestResolveTwoRulesAtTheSameNonGroupScopeSmallerIDWins exercises
// candidate.beats's own fallback tie-break: the schema does not forbid two
// rules at the same non-group scope (here, org) judging the identical
// (check_key, metric, severity) — Resolve still owes a single, deterministic
// winner rather than an arbitrary one, the older rule and then the smaller
// id, the same fallback a same-group tie (two rules bound to the very same
// group) would also fall through to.
func TestResolveTwoRulesAtTheSameNonGroupScopeSmallerIDWins(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	hc := rules.HostContext{HostID: host, Tuples: []rules.Tuple{tuple}}

	a := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	a.ID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	b := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	b.ID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	// Both a and b keep baseRule's identical CreatedAt (fixedTime(1)), so
	// the only thing left to decide the winner is the id.

	got := rules.Resolve([]rules.Rule{b, a}, hc) // b (larger id) listed first
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	if got[0].Winner.ID != a.ID {
		t.Fatalf("winner = %s, want the smaller id %s", got[0].Winner.ID, a.ID)
	}
	if len(got[0].Shadowed) != 1 || got[0].Shadowed[0].ID != b.ID {
		t.Fatalf("shadowed = %+v, want exactly the larger-id rule", got[0].Shadowed)
	}
}

// TestResolveTwoRulesAtTheSameNonGroupScopeOlderWins is
// TestResolveTwoRulesAtTheSameNonGroupScopeSmallerIDWins's companion: when
// the two same-scope rules do not even tie on CreatedAt, the older one
// wins outright and id is never consulted — the same order
// candidate.beats gives a same-group tie, mirrored here for two rules that
// share no group at all (org scope).
func TestResolveTwoRulesAtTheSameNonGroupScopeOlderWins(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuple := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	hc := rules.HostContext{HostID: host, Tuples: []rules.Tuple{tuple}}

	older := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	older.CreatedAt = fixedTime(1)
	// Deliberately the larger id on the older rule, so this only passes by
	// consulting CreatedAt, not by the id tie-break coincidentally
	// agreeing with it.
	older.ID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	newer := baseRule("disk", "disk.used_pct", rules.SeverityWarn)
	newer.CreatedAt = fixedTime(2)
	newer.ID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

	got := rules.Resolve([]rules.Rule{newer, older}, hc)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	if got[0].Winner.ID != older.ID {
		t.Fatalf("winner = %s, want the older rule %s", got[0].Winner.ID, older.ID)
	}
}

// TestResolveOrdersMultipleEffectivesByTupleThenSeverity exercises the
// final ordering Resolve promises across genuinely different tuples (not
// merely different severities of the same tuple, which every other test
// above already covers): CheckKey, then Item, then Metric, then Severity —
// so that two Resolve calls given the same rules in a different order (or
// a caller that ranges a map to build its own report) can always compare
// the result byte-for-byte.
func TestResolveOrdersMultipleEffectivesByTupleThenSeverity(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	tuples := []rules.Tuple{
		{CheckKey: "disk", Item: "/backup", Metric: "disk.inode_pct"},
		{CheckKey: "disk", Item: "/backup", Metric: "disk.used_pct"},
		{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"},
		{CheckKey: "cpu", Item: "", Metric: "cpu.used_pct"},
	}
	hc := rules.HostContext{HostID: host, Tuples: tuples}

	var input []rules.Rule
	for _, t := range tuples {
		r := baseRule(t.CheckKey, t.Metric, rules.SeverityWarn)
		input = append(input, r)
	}

	got := rules.Resolve(input, hc)
	if len(got) != len(tuples) {
		t.Fatalf("len(got) = %d, want %d: %+v", len(got), len(tuples), got)
	}
	want := []rules.Tuple{
		{CheckKey: "cpu", Item: "", Metric: "cpu.used_pct"},
		{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"},
		{CheckKey: "disk", Item: "/backup", Metric: "disk.inode_pct"},
		{CheckKey: "disk", Item: "/backup", Metric: "disk.used_pct"},
	}
	for i, e := range got {
		if e.Tuple != want[i] {
			t.Fatalf("got[%d].Tuple = %+v, want %+v (full result: %+v)", i, e.Tuple, want[i], got)
		}
	}
}
