package items_test

import (
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/items"
)

// fixedTime gives every decide test a distinct, comparable timestamp without
// depending on time.Now or the system clock — n orders the timestamps
// (fixedTime(1) is before fixedTime(2)), the same helper
// internal/rules/resolve_test.go uses for its own tie-break tests.
func fixedTime(n int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Hour)
}

// baseDecision returns an org-scoped, glob decision for checkKey with a
// fresh id and fixedTime(1) timestamps — the common starting point every
// test below narrows with its own scope, match kind and verdict.
func baseDecision(checkKey, pattern string, verdict items.Verdict) items.Decision {
	return items.Decision{
		ID:        uuid.Must(uuid.NewV7()),
		OrgID:     uuid.Must(uuid.NewV7()),
		Scope:     items.ScopeOrg,
		CheckKey:  checkKey,
		MatchKind: items.MatchGlob,
		Pattern:   pattern,
		Verdict:   verdict,
		CreatedAt: fixedTime(1),
		UpdatedAt: fixedTime(1),
	}
}

func TestDecideNoDecisionsWatchesByDefault(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	target := items.Target{HostID: host, CheckKey: "disk", Item: "/var"}

	got := items.Decide(nil, target)
	if got.Verdict != items.Watch || got.Decision != nil {
		t.Fatalf("Decide(nil, %+v) = %+v, want Watch with a nil Decision", target, got)
	}
	if got.Ignored() {
		t.Fatal("Ignored() = true, want false: no decision matched")
	}
}

func TestDecideOneOrgGlobMatches(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	target := items.Target{HostID: host, CheckKey: "disk", Item: "/mnt/scratch-db"}
	d := baseDecision("disk", "/mnt/scratch-*", items.Ignore)

	got := items.Decide([]items.Decision{d}, target)
	if got.Verdict != items.Ignore || got.Decision == nil || got.Decision.ID != d.ID {
		t.Fatalf("Decide(%+v) = %+v, want the org glob to govern with verdict ignore", target, got)
	}
	if !got.Ignored() {
		t.Fatal("Ignored() = false, want true")
	}
}

func TestDecideOneOrgExactMatches(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	target := items.Target{HostID: host, CheckKey: "service", Item: "sshd"}
	d := baseDecision("service", "sshd", items.Watch)
	d.MatchKind = items.MatchExact

	got := items.Decide([]items.Decision{d}, target)
	if got.Verdict != items.Watch || got.Decision == nil || got.Decision.ID != d.ID {
		t.Fatalf("Decide(%+v) = %+v, want the org exact decision to govern", target, got)
	}
}

// TestDecideHostBeatsOrg is review focus 4: a host decision always outranks
// an organization one, regardless of which one carries the "more severe"
// verdict — here the host says watch and the org says ignore, and the host
// still wins, both in its verdict and in which Decision is reported.
func TestDecideHostBeatsOrg(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	target := items.Target{HostID: host, CheckKey: "disk", Item: "/mnt/scratch-db"}

	org := baseDecision("disk", "/mnt/scratch-*", items.Ignore)
	hostDecision := baseDecision("disk", "/mnt/scratch-*", items.Watch)
	hostDecision.Scope, hostDecision.HostID = items.ScopeHost, &host

	got := items.Decide([]items.Decision{org, hostDecision}, target)
	if got.Verdict != items.Watch {
		t.Fatalf("verdict = %s, want watch (the host decision)", got.Verdict)
	}
	if got.Decision == nil || got.Decision.ID != hostDecision.ID {
		t.Fatalf("decision = %+v, want the host decision %s reported, not the org one", got.Decision, hostDecision.ID)
	}
}

// TestDecideExactBeatsGlobWithinScope mirrors TestDecideHostBeatsOrg one
// level down: within the same scope (here, org), an exact match outranks a
// glob match even though both match the same item.
func TestDecideExactBeatsGlobWithinScope(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	target := items.Target{HostID: host, CheckKey: "disk", Item: "/mnt/scratch-db"}

	glob := baseDecision("disk", "/mnt/scratch-*", items.Ignore)
	exact := baseDecision("disk", "/mnt/scratch-db", items.Watch)
	exact.MatchKind = items.MatchExact

	got := items.Decide([]items.Decision{glob, exact}, target)
	if got.Verdict != items.Watch || got.Decision == nil || got.Decision.ID != exact.ID {
		t.Fatalf("Decide(%+v) = %+v, want the exact decision to beat the glob", target, got)
	}
}

func TestDecideAnotherCheckKeyIsIgnored(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	target := items.Target{HostID: host, CheckKey: "disk", Item: "/mnt/scratch-db"}
	d := baseDecision("service", "*", items.Ignore) // a different check entirely

	got := items.Decide([]items.Decision{d}, target)
	if got.Verdict != items.Watch || got.Decision != nil {
		t.Fatalf("Decide(%+v) = %+v, want no match: the decision is for a different check_key", target, got)
	}
}

// TestDecideHostDecisionForAnotherHostDoesNotApply pins the scope
// applicability Decide owes before it ranks anything: a ScopeHost decision
// bound to a different host must never govern this host's item, the same
// way rules.Resolve ignores a host-scoped rule bound to a different host.
func TestDecideHostDecisionForAnotherHostDoesNotApply(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	otherHost := uuid.Must(uuid.NewV7())
	target := items.Target{HostID: host, CheckKey: "disk", Item: "/mnt/scratch-db"}
	d := baseDecision("disk", "/mnt/scratch-*", items.Ignore)
	d.Scope, d.HostID = items.ScopeHost, &otherHost

	got := items.Decide([]items.Decision{d}, target)
	if got.Verdict != items.Watch || got.Decision != nil {
		t.Fatalf("Decide(%+v) = %+v, want no match: the decision is bound to a different host", target, got)
	}
}

// TestDecideInvalidScopeDoesNotApply is a defensive-code test: the schema's
// own CHECK constraint on check_item_decisions.scope never produces a
// Decision whose Scope is neither ScopeOrg nor ScopeHost, but Decide is an
// exported pure function callable with a hand-built Decision too (mirrors
// rules_test.go's TestResolveGroupRuleWithNilGroupIDDoesNotApply), so it must
// not treat an unrecognized scope as applicable.
func TestDecideInvalidScopeDoesNotApply(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	target := items.Target{HostID: host, CheckKey: "disk", Item: "/mnt/scratch-db"}
	d := baseDecision("disk", "*", items.Ignore)
	d.Scope = items.Scope("bogus")

	got := items.Decide([]items.Decision{d}, target)
	if got.Verdict != items.Watch || got.Decision != nil {
		t.Fatalf("Decide(%+v) = %+v, want no match: an unrecognized scope must not apply, not panic", target, got)
	}
}

// TestDecideDoesNotMutateInput pins the controller's own requirement: Decide
// is called from inside the evaluation path with a slice reused across every
// item of a host, so it must rank without reordering (or mutating) the
// caller's slice.
func TestDecideDoesNotMutateInput(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	target := items.Target{HostID: host, CheckKey: "disk", Item: "/mnt/scratch-db"}

	first := baseDecision("disk", "/mnt/scratch-*", items.Ignore)
	first.CreatedAt = fixedTime(2)
	second := baseDecision("disk", "/mnt/scratch-*", items.Watch)
	second.CreatedAt = fixedTime(1) // older, so second should be ranked first internally

	ds := []items.Decision{first, second}
	want := []items.Decision{first, second}

	_ = items.Decide(ds, target)

	if !reflect.DeepEqual(ds, want) {
		t.Fatalf("Decide mutated or reordered its input slice: got %+v, want %+v", ds, want)
	}
}

// TestDecideOlderCreatedAtWinsWithinScope pins beats's own CreatedAt
// tie-break in isolation: two decisions at the same scope and the same
// match kind, differing only in CreatedAt, must resolve to the older one —
// not merely to whichever happens to sort first on id. The older decision
// is deliberately given the larger id (the opposite of what uuid.NewV7's
// monotonic ordering would naturally hand out), so this only passes by
// actually consulting CreatedAt, not by the id tie-break coincidentally
// agreeing with it (mirrors internal/rules/resolve_test.go's
// TestResolveTwoRulesAtTheSameNonGroupScopeOlderWins).
func TestDecideOlderCreatedAtWinsWithinScope(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	target := items.Target{HostID: host, CheckKey: "disk", Item: "/mnt/scratch-db"}

	older := baseDecision("disk", "/mnt/scratch-*", items.Ignore)
	older.CreatedAt = fixedTime(1)
	older.ID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	newer := baseDecision("disk", "/mnt/scratch-*", items.Watch)
	newer.CreatedAt = fixedTime(2)
	newer.ID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

	got := items.Decide([]items.Decision{newer, older}, target)
	if got.Decision == nil || got.Decision.ID != older.ID {
		t.Fatalf("Decide(%+v) = %+v, want the older decision %s to win, not the newer one", target, got, older.ID)
	}
	if got.Verdict != items.Ignore {
		t.Fatalf("verdict = %s, want ignore (the older decision's own verdict)", got.Verdict)
	}
}

// TestDecideTwoEquallySpecificGlobsAcross100Shuffles is review focus 5: two
// decisions tied on scope, match kind and CreatedAt must resolve to the same
// Resolution — the smaller id wins — every time, regardless of the order the
// decisions happen to be given in. The input slice is shuffled with a
// seeded RNG before every one of the 100 calls, and the test asserts that
// the shuffle actually produced more than one distinct order before trusting
// the "always identical" assertion that follows — otherwise a resolver that
// silently depended on slice order could pass by accident. The whole
// Resolution is compared, not just the verdict, since two decisions could
// tie on verdict and still differ on which Decision is reported.
func TestDecideTwoEquallySpecificGlobsAcross100Shuffles(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	target := items.Target{HostID: host, CheckKey: "disk", Item: "/mnt/scratch-db"}
	tie := fixedTime(1)

	a := baseDecision("disk", "/mnt/scratch-*", items.Ignore)
	a.CreatedAt = tie
	b := baseDecision("disk", "/mnt/scratch-*", items.Watch)
	b.CreatedAt = tie

	wantWinner := a
	if bytesCompareUUID(b.ID, a.ID) < 0 {
		wantWinner = b
	}

	rnd := rand.New(rand.NewSource(1))
	var firstResult items.Resolution
	orders := map[string]bool{}
	for i := range 100 {
		ds := []items.Decision{a, b}
		rnd.Shuffle(len(ds), func(x, y int) { ds[x], ds[y] = ds[y], ds[x] })
		orders[ds[0].ID.String()] = true

		got := items.Decide(ds, target)
		if got.Decision == nil || got.Decision.ID != wantWinner.ID {
			t.Fatalf("run %d: decision = %+v, want the smaller-id decision %s", i, got.Decision, wantWinner.ID)
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

// bytesCompareUUID compares two uuid.UUID values byte for byte, the same
// order Decide's own tie-break falls back to.
func bytesCompareUUID(a, b uuid.UUID) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
