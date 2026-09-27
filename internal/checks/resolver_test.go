package checks_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

// fakeHostRoster is the checks.HostRoster test double: monitored[id] = true
// means the id is reported by MonitoredHosts; absent or false means it is
// not — the resolver must treat both the same way ("not monitored"),
// per ResolveHost's own doc comment.
type fakeHostRoster struct {
	monitored map[uuid.UUID]bool
	err       error
}

func (f *fakeHostRoster) MonitoredHosts(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []uuid.UUID
	for id, m := range f.monitored {
		if m {
			out = append(out, id)
		}
	}
	return out, nil
}

// fakeGroupMatcher answers MatchingHosts per selector text. The package's
// own fakeMatcher (groups_test.go) gives one fixed answer regardless of
// selector, which is enough for Preview's tests but not the resolver's:
// a resolver test needs two different groups (two different selectors) to
// match two different — or overlapping, or now-empty — sets of hosts.
type fakeGroupMatcher struct {
	bySelector map[string][]uuid.UUID
	err        error
	calls      int
}

func (f *fakeGroupMatcher) MatchingHosts(_ context.Context, _ uuid.UUID, selector string) ([]uuid.UUID, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.bySelector[selector], nil
}

// failListStore wraps a *fakeStore and fails only List (groups), so a test
// can pin the "resolve: list groups" wrapped-error path independently from
// "resolve: list definitions" (both use the same fakeStore.err field,
// which fails on ListDefinitions first).
type failListStore struct{ *fakeStore }

func (failListStore) List(context.Context, uuid.UUID) ([]checks.Group, error) {
	return nil, errors.New("list groups down")
}

// newResolverFixture returns a store, a group matcher and a host roster
// with nothing in them yet, and a fresh organization id — the common
// starting point every resolver test builds its own scenario on top of.
func newResolverFixture() (*fakeStore, *fakeGroupMatcher, *fakeHostRoster, uuid.UUID) {
	return newFakeStore(), &fakeGroupMatcher{bySelector: map[string][]uuid.UUID{}}, &fakeHostRoster{monitored: map[uuid.UUID]bool{}}, uuid.Must(uuid.NewV7())
}

// putGroup inserts g directly into store's backing map, generating an id
// if g.ID is zero, and returns the stored value. Resolver tests build
// fixtures this way rather than through Service.CreateGroup: CreatedAt and
// Priority need exact, test-chosen values (including two groups sharing
// the identical timestamp, for the tie-break tests), which a service call
// stamped through fakeStore.now cannot express for more than one group at
// a time.
func putGroup(store *fakeStore, g checks.Group) checks.Group {
	if g.ID == uuid.Nil {
		g.ID = uuid.Must(uuid.NewV7())
	}
	store.byID[g.ID] = g
	return g
}

// putDef is putGroup's equivalent for definitions — direct store
// insertion, so a test controls Enabled/BindingKind/GroupID/HostID exactly
// without also exercising normalizeDefinition's validation (Task 2's own
// tests cover that).
func putDef(store *fakeStore, d checks.Definition) checks.Definition {
	if d.ID == uuid.Nil {
		d.ID = uuid.Must(uuid.NewV7())
	}
	store.defsByID[d.ID] = d
	return d
}

// fixedTime gives every resolver test a distinct, comparable timestamp
// without depending on time.Now or the system clock — n orders the
// timestamps (fixedTime(1) is before fixedTime(2)), which is all the
// tie-break tests need.
func fixedTime(n int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Hour)
}

func TestResolveHostOrgOnly(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putDef(store, checks.Definition{
		OrgID: org, Key: "cpu", Plugin: checks.PluginCPU,
		Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg,
	})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if rs.HostID != host {
		t.Fatalf("HostID = %s, want %s", rs.HostID, host)
	}
	if len(rs.Checks) != 1 {
		t.Fatalf("checks = %+v, want exactly 1", rs.Checks)
	}
	c := rs.Checks[0]
	if c.Key != "cpu" || c.Plugin != checks.PluginCPU || c.Interval != 30*time.Second ||
		c.Source != checks.BindingOrg || c.SourceID != nil {
		t.Fatalf("resolved check = %+v, want the organization's cpu definition with a nil SourceID", c)
	}
	if rs.Version == "" {
		t.Fatal("Version must not be empty")
	}
}

func TestResolveHostGroupOverridesOrg(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	g := putGroup(store, checks.Group{OrgID: org, Name: "g", Selector: "role=db", Priority: 0, CreatedAt: fixedTime(1), UpdatedAt: fixedTime(1)})
	matcher.bySelector["role=db"] = []uuid.UUID{host}
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 60 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 15 * time.Second, Enabled: true, BindingKind: checks.BindingGroup, GroupID: &g.ID})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Checks) != 1 || rs.Checks[0].Interval != 15*time.Second ||
		rs.Checks[0].Source != checks.BindingGroup || rs.Checks[0].SourceID == nil || *rs.Checks[0].SourceID != g.ID {
		t.Fatalf("checks = %+v, want the group's definition (15s, source group %s)", rs.Checks, g.ID)
	}
}

func TestResolveHostHostOverridesGroup(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	g := putGroup(store, checks.Group{OrgID: org, Name: "g", Selector: "role=db", Priority: 0, CreatedAt: fixedTime(1), UpdatedAt: fixedTime(1)})
	matcher.bySelector["role=db"] = []uuid.UUID{host}
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 60 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 15 * time.Second, Enabled: true, BindingKind: checks.BindingGroup, GroupID: &g.ID})
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 5 * time.Second, Enabled: true, BindingKind: checks.BindingHost, HostID: &host})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Checks) != 1 || rs.Checks[0].Interval != 5*time.Second ||
		rs.Checks[0].Source != checks.BindingHost || rs.Checks[0].SourceID == nil || *rs.Checks[0].SourceID != host {
		t.Fatalf("checks = %+v, want the host's own definition (5s, source host %s)", rs.Checks, host)
	}
}

func TestResolveHostTwoGroupsHigherPriorityWins(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	low := putGroup(store, checks.Group{OrgID: org, Name: "low", Selector: "a", Priority: 1, CreatedAt: fixedTime(1), UpdatedAt: fixedTime(1)})
	high := putGroup(store, checks.Group{OrgID: org, Name: "high", Selector: "b", Priority: 10, CreatedAt: fixedTime(2), UpdatedAt: fixedTime(2)})
	matcher.bySelector["a"] = []uuid.UUID{host}
	matcher.bySelector["b"] = []uuid.UUID{host}
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingGroup, GroupID: &low.ID})
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 90 * time.Second, Enabled: true, BindingKind: checks.BindingGroup, GroupID: &high.ID})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Checks) != 1 || rs.Checks[0].Interval != 90*time.Second ||
		rs.Checks[0].SourceID == nil || *rs.Checks[0].SourceID != high.ID {
		t.Fatalf("checks = %+v, want the higher-priority group (high, 90s) to win", rs.Checks)
	}
}

func TestResolveHostTwoGroupsEqualPriorityOlderWins(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	// Deliberately the opposite of what uuid.NewV7 (monotonic; an earlier
	// CreatedAt would naturally get the smaller id too) would hand out:
	// the older group gets the *larger* id, the newer group the *smaller*
	// one. If beats ever stopped consulting CreatedAt, the id clause alone
	// would then pick the wrong (newer) group, so this test only passes by
	// actually consulting created_at, not by the id tie-break coincidentally
	// agreeing with it.
	older := putGroup(store, checks.Group{
		ID:    uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff"),
		OrgID: org, Name: "older", Selector: "a", Priority: 5, CreatedAt: fixedTime(1), UpdatedAt: fixedTime(1),
	})
	newer := putGroup(store, checks.Group{
		ID:    uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		OrgID: org, Name: "newer", Selector: "b", Priority: 5, CreatedAt: fixedTime(2), UpdatedAt: fixedTime(2),
	})
	matcher.bySelector["a"] = []uuid.UUID{host}
	matcher.bySelector["b"] = []uuid.UUID{host}
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingGroup, GroupID: &older.ID})
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 90 * time.Second, Enabled: true, BindingKind: checks.BindingGroup, GroupID: &newer.ID})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Checks) != 1 || rs.Checks[0].Interval != 30*time.Second ||
		rs.Checks[0].SourceID == nil || *rs.Checks[0].SourceID != older.ID {
		t.Fatalf("checks = %+v, want the older group (older, 30s) to win an equal-priority tie", rs.Checks)
	}
}

// TestResolveOrgTwoGroupsEqualPriorityAndCreatedAtAcross100Runs is Review
// Focus 1: two groups tied on both Priority and CreatedAt must resolve to
// the same winner — the smaller id — every time. fakeStore.List ranges
// over a Go map to build its result, which the runtime deliberately
// re-randomizes on every range statement; across 100 calls that is enough
// to surface a resolver that picked its winner by iteration order instead
// of by Group.beats's actual comparator; a real map-order bug would make
// this test flake, not fail outright, which is exactly the failure mode a
// single run cannot catch.
func TestResolveOrgTwoGroupsEqualPriorityAndCreatedAtAcross100Runs(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	tie := fixedTime(1)
	groupA := putGroup(store, checks.Group{OrgID: org, Name: "a", Selector: "role=web", Priority: 5, CreatedAt: tie, UpdatedAt: tie})
	groupB := putGroup(store, checks.Group{OrgID: org, Name: "b", Selector: "env=prod", Priority: 5, CreatedAt: tie, UpdatedAt: tie})
	matcher.bySelector["role=web"] = []uuid.UUID{host}
	matcher.bySelector["env=prod"] = []uuid.UUID{host}
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingGroup, GroupID: &groupA.ID})
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 90 * time.Second, Enabled: true, BindingKind: checks.BindingGroup, GroupID: &groupB.ID})

	wantWinner := groupA.ID
	if bytes.Compare(groupB.ID[:], groupA.ID[:]) < 0 {
		wantWinner = groupB.ID
	}

	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	var firstVersion string
	versions := map[string]bool{}
	for i := range 100 {
		result, err := svc.ResolveOrg(ctx, org)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		rs, ok := result[host]
		if !ok {
			t.Fatalf("run %d: host missing from ResolveOrg's result", i)
		}
		if len(rs.Checks) != 1 {
			t.Fatalf("run %d: checks = %+v, want exactly 1", i, rs.Checks)
		}
		c := rs.Checks[0]
		if c.SourceID == nil || *c.SourceID != wantWinner {
			t.Fatalf("run %d: winner = %v, want the smaller id %s", i, c.SourceID, wantWinner)
		}
		versions[rs.Version] = true
		if i == 0 {
			firstVersion = rs.Version
		} else if rs.Version != firstVersion {
			t.Fatalf("run %d: version %s diverged from run 0's %s — a non-deterministic tie-break", i, rs.Version, firstVersion)
		}
	}
	if len(versions) != 1 {
		t.Fatalf("100 runs produced %d distinct versions, want exactly 1", len(versions))
	}
}

// TestResolveHostGroupSelectorNoLongerMatchingFallsBackToOrg is Review
// Focus 2: a group that used to contribute a key but whose selector no
// longer matches this host must not leave that key stranded — the host
// falls back to whichever broader scope still defines it, and the
// resolved entry names that scope as its Source, not the group's.
func TestResolveHostGroupSelectorNoLongerMatchingFallsBackToOrg(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	g := putGroup(store, checks.Group{OrgID: org, Name: "g", Selector: "role=db", Priority: 0, CreatedAt: fixedTime(1), UpdatedAt: fixedTime(1)})
	// The group's selector no longer matches host (its tag changed) —
	// MatchingHosts now answers with an empty set for it.
	matcher.bySelector["role=db"] = nil
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 60 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 15 * time.Second, Enabled: true, BindingKind: checks.BindingGroup, GroupID: &g.ID})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Checks) != 1 || rs.Checks[0].Interval != 60*time.Second ||
		rs.Checks[0].Source != checks.BindingOrg || rs.Checks[0].SourceID != nil {
		t.Fatalf("checks = %+v, want the organization's definition (60s, source org) now that the group no longer matches", rs.Checks)
	}
}

func TestResolveHostDisabledAtHostScopeRemovesOrgCheck(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 60 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 60 * time.Second, Enabled: false, BindingKind: checks.BindingHost, HostID: &host})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Checks) != 0 {
		t.Fatalf("checks = %+v, want none — the host scope disabled the organization's check", rs.Checks)
	}
	if rs.Checks == nil {
		t.Fatal("Checks is nil, want a non-nil empty slice (see TestResolveHostChecksIsNeverNil)")
	}
}

func TestResolveHostUnmonitoredResolvesToNothing(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	// roster.monitored has no entry for host at all: not monitored.
	putDef(store, checks.Definition{OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Checks) != 0 {
		t.Fatalf("checks = %+v, want none for an unmonitored host", rs.Checks)
	}
	if rs.Checks == nil {
		t.Fatal("Checks is nil, want a non-nil empty slice (see TestResolveHostChecksIsNeverNil)")
	}
	if rs.Version == "" {
		t.Fatal("Version must still be set (the hash of an empty set), got empty string")
	}
	if matcher.calls != 0 {
		t.Fatalf("matcher.calls = %d, want 0 — an unmonitored host must short-circuit before expanding any group", matcher.calls)
	}
}

// TestResolveHostChecksIsNeverNil is finding 3: an unmonitored host (which
// short-circuits before ever building a Resolved list) and a monitored
// host whose only check is disabled (which builds an empty list the long
// way, through resolveBundle.resolve) must produce byte-identical JSON —
// "[]", never the bare "null" a nil slice would serialise as — even
// though the two reach ResolveHost by entirely different code paths.
func TestResolveHostChecksIsNeverNil(t *testing.T) {
	unmonitoredStore, matcher1, roster1, org1 := newResolverFixture()
	unmonitoredHost := uuid.Must(uuid.NewV7())
	putDef(unmonitoredStore, checks.Definition{OrgID: org1, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	svc1 := checks.NewService(unmonitoredStore, checks.WithMatcher(matcher1), checks.WithHostRoster(roster1))
	rsUnmonitored, err := svc1.ResolveHost(ctx, org1, unmonitoredHost)
	if err != nil {
		t.Fatal(err)
	}

	disabledStore, matcher2, roster2, org2 := newResolverFixture()
	disabledHost := uuid.Must(uuid.NewV7())
	roster2.monitored[disabledHost] = true
	putDef(disabledStore, checks.Definition{OrgID: org2, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: false, BindingKind: checks.BindingOrg})
	svc2 := checks.NewService(disabledStore, checks.WithMatcher(matcher2), checks.WithHostRoster(roster2))
	rsDisabled, err := svc2.ResolveHost(ctx, org2, disabledHost)
	if err != nil {
		t.Fatal(err)
	}

	for name, rs := range map[string]checks.ResolvedSet{
		"unmonitored":                rsUnmonitored,
		"monitored-but-all-disabled": rsDisabled,
	} {
		if rs.Checks == nil {
			t.Fatalf("%s: Checks is nil, want a non-nil empty slice", name)
		}
		b, err := json.Marshal(rs.Checks)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(b) != "[]" {
			t.Fatalf("%s: json.Marshal(Checks) = %s, want []", name, b)
		}
	}
}

func TestResolveOrgOnlyIncludesMonitoredHosts(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	monitoredHost := uuid.Must(uuid.NewV7())
	unmonitoredHost := uuid.Must(uuid.NewV7())
	roster.monitored[monitoredHost] = true
	roster.monitored[unmonitoredHost] = false // present but false — must be excluded, same as absent
	putDef(store, checks.Definition{OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	result, err := svc.ResolveOrg(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result[unmonitoredHost]; ok {
		t.Fatalf("result contains an unmonitored host: %+v", result)
	}
	rs, ok := result[monitoredHost]
	if !ok || len(rs.Checks) != 1 {
		t.Fatalf("result[monitoredHost] = %+v, ok=%v, want the organization's cpu check", rs, ok)
	}
}

// TestVersionCanonicalEncodingIsPinned pins the exact byte sequence Version
// is hashed over, not merely "the hash changes when an input changes":
// checks sorted by key, each rendered as
// "key\x00plugin\x00interval_seconds\x00canonical-json(params)\x00", and
// concatenated in that order. The disk definition's params map is built
// with its keys in non-alphabetical Go order ("z" before "a") specifically
// to prove canonical JSON's sorted-key guarantee is what actually runs,
// not merely assumed of encoding/json.
func TestVersionCanonicalEncodingIsPinned(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putDef(store, checks.Definition{OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	putDef(store, checks.Definition{
		OrgID: org, Key: "disk", Plugin: checks.PluginDisk,
		Params: map[string]any{"z": 1.0, "a": 2.0}, Interval: 60 * time.Second, Enabled: true, BindingKind: checks.BindingOrg,
	})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}

	const want = "cpu\x00cpu\x0030\x00{}\x00" + "disk\x00disk\x0060\x00{\"a\":2,\"z\":1}\x00"
	sum := sha256.Sum256([]byte(want))
	wantVersion := hex.EncodeToString(sum[:])
	if rs.Version != wantVersion {
		t.Fatalf("version = %s, want %s (canonical encoding %q)", rs.Version, wantVersion, want)
	}
}

func TestVersionChangesWithKeyPluginIntervalOrParams(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	const defID = "11111111-1111-1111-1111-111111111111"
	id := uuid.MustParse(defID)
	base := checks.Definition{
		ID: id, OrgID: org, Key: "disk", Plugin: checks.PluginDisk,
		Params: map[string]any{"mounts": []any{"/"}}, Interval: 60 * time.Second,
		Enabled: true, BindingKind: checks.BindingOrg,
	}
	putDef(store, base)
	rs1, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	rs2, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if rs1.Version != rs2.Version {
		t.Fatalf("identical inputs produced different versions: %s vs %s", rs1.Version, rs2.Version)
	}
	baseVersion := rs1.Version

	cases := map[string]checks.Definition{
		"key": func() checks.Definition {
			d := base
			d.Key = "disk-var"
			return d
		}(),
		"plugin": func() checks.Definition {
			d := base
			d.Plugin = checks.PluginCPU
			d.Params = nil
			return d
		}(),
		"interval": func() checks.Definition {
			d := base
			d.Interval = 90 * time.Second
			return d
		}(),
		"params": func() checks.Definition {
			d := base
			d.Params = map[string]any{"mounts": []any{"/var"}}
			return d
		}(),
	}
	for name, mutated := range cases {
		t.Run(name, func(t *testing.T) {
			putDef(store, mutated)
			rs, err := svc.ResolveHost(ctx, org, host)
			if err != nil {
				t.Fatal(err)
			}
			if rs.Version == baseVersion {
				t.Fatalf("changing %s did not change the version (still %s)", name, rs.Version)
			}
			// Restore the base definition so the next subtest starts from
			// the same, known state.
			putDef(store, base)
		})
	}
}

func TestResolveHostWithoutHostRosterFails(t *testing.T) {
	store, matcher, _, org := newResolverFixture()
	svc := checks.NewService(store, checks.WithMatcher(matcher))

	if _, err := svc.ResolveHost(ctx, org, uuid.Must(uuid.NewV7())); err == nil {
		t.Fatal("want an error when the service was built without WithHostRoster")
	}
	if _, err := svc.ResolveOrg(ctx, org); err == nil {
		t.Fatal("want an error when the service was built without WithHostRoster")
	}
}

func TestResolveHostWithGroupsButWithoutMatcherFails(t *testing.T) {
	store, _, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putGroup(store, checks.Group{OrgID: org, Name: "g", Selector: "role=db", CreatedAt: fixedTime(1), UpdatedAt: fixedTime(1)})
	svc := checks.NewService(store, checks.WithHostRoster(roster))

	if _, err := svc.ResolveHost(ctx, org, host); err == nil {
		t.Fatal("want an error: a group exists but no WithMatcher was given")
	}
}

func TestResolveHostWithoutGroupsNeedsNoMatcher(t *testing.T) {
	store, _, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putDef(store, checks.Definition{OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	svc := checks.NewService(store, checks.WithHostRoster(roster)) // deliberately no WithMatcher

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatalf("resolving an organization with no groups must not need a matcher: %v", err)
	}
	if len(rs.Checks) != 1 {
		t.Fatalf("checks = %+v, want exactly 1", rs.Checks)
	}
}

func TestResolveHostPropagatesHostRosterError(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	roster.err = errors.New("roster down")
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	if _, err := svc.ResolveHost(ctx, org, uuid.Must(uuid.NewV7())); err == nil {
		t.Fatal("want the host roster's error propagated")
	}
	if _, err := svc.ResolveOrg(ctx, org); err == nil {
		t.Fatal("want the host roster's error propagated")
	}
}

func TestResolveHostPropagatesMatcherError(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putGroup(store, checks.Group{OrgID: org, Name: "g", Selector: "role=db", CreatedAt: fixedTime(1), UpdatedAt: fixedTime(1)})
	matcher.err = errors.New("matcher down")
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	if _, err := svc.ResolveHost(ctx, org, host); err == nil {
		t.Fatal("want the matcher's error propagated")
	}
}

func TestResolveHostPropagatesListDefinitionsError(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	store.err = errors.New("store down")
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	if _, err := svc.ResolveHost(ctx, org, host); err == nil {
		t.Fatal("want the store's ListDefinitions error propagated")
	}
}

// TestResolveHostPropagatesParamsEncodingError exercises the one error
// path canonicalEncoding/version/toResolvedSet carry for a params value
// json.Marshal itself refuses (a channel is never producible through
// CreateDefinition's own validation, which only ever accepts what came off
// JSON or a Go string-list literal — this reaches the store directly, the
// same way the rest of this file bypasses normalizeDefinition on purpose).
func TestResolveHostPropagatesParamsEncodingError(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putDef(store, checks.Definition{
		OrgID: org, Key: "disk", Plugin: checks.PluginDisk,
		Params: map[string]any{"bad": make(chan int)}, Interval: 60 * time.Second, Enabled: true, BindingKind: checks.BindingOrg,
	})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	if _, err := svc.ResolveHost(ctx, org, host); err == nil {
		t.Fatal("want an error: a channel value cannot be JSON-encoded")
	}
	if _, err := svc.ResolveOrg(ctx, org); err == nil {
		t.Fatal("want an error: a channel value cannot be JSON-encoded")
	}
}

func TestResolveHostPropagatesListGroupsError(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	svc := checks.NewService(failListStore{store}, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	if _, err := svc.ResolveHost(ctx, org, host); err == nil {
		t.Fatal("want the store's List (groups) error propagated")
	}
}

// TestEffectiveHostReportsADisabledWinnerWithItsSource is finding 1: the
// half of the enabled rule ResolveHost cannot express (Resolved has no
// Enabled field, verbatim from this package's brief) — a check disabled
// at the winning scope must still be visible, with the scope that
// disabled it, through EffectiveHost, even though ResolveHost's own
// Checks correctly omits it (TestResolveHostDisabledAtHostScopeRemovesOrgCheck
// already pins that half).
func TestEffectiveHostReportsADisabledWinnerWithItsSource(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 60 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 60 * time.Second, Enabled: false, BindingKind: checks.BindingHost, HostID: &host})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	// ResolveHost's own set has already dropped this key entirely.
	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Checks) != 0 {
		t.Fatalf("ResolveHost checks = %+v, want none", rs.Checks)
	}

	defs, err := svc.EffectiveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 {
		t.Fatalf("EffectiveHost = %+v, want exactly 1 (the disabled winner)", defs)
	}
	d := defs[0]
	if d.Key != "disk" || d.Enabled || d.BindingKind != checks.BindingHost || d.HostID == nil || *d.HostID != host {
		t.Fatalf("EffectiveHost[0] = %+v, want the disabled host-scope definition naming host %s as its source", d, host)
	}
}

// TestEffectiveHostAgreesWithResolveHostForEnabledChecks proves the two
// methods share one implementation of precedence rather than two that
// could drift: for a key that survives (Enabled = true at its winning
// scope), EffectiveHost's winning Definition and ResolveHost's Resolved
// entry must describe the exact same winner.
func TestEffectiveHostAgreesWithResolveHostForEnabledChecks(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	g := putGroup(store, checks.Group{OrgID: org, Name: "g", Selector: "role=db", Priority: 0, CreatedAt: fixedTime(1), UpdatedAt: fixedTime(1)})
	matcher.bySelector["role=db"] = []uuid.UUID{host}
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 60 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 15 * time.Second, Enabled: true, BindingKind: checks.BindingGroup, GroupID: &g.ID})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	defs, err := svc.EffectiveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Checks) != 1 || len(defs) != 1 {
		t.Fatalf("ResolveHost = %+v, EffectiveHost = %+v, want exactly 1 each", rs.Checks, defs)
	}
	c, d := rs.Checks[0], defs[0]
	if d.Key != c.Key || d.Plugin != c.Plugin || d.Interval != c.Interval || d.BindingKind != c.Source ||
		d.GroupID == nil || c.SourceID == nil || *d.GroupID != *c.SourceID {
		t.Fatalf("ResolveHost's winner %+v and EffectiveHost's winner %+v disagree", c, d)
	}
}

// TestEffectiveHostSortedByKey pins EffectiveHost's own ordering
// guarantee, independent of ResolvedSet.Checks's.
func TestEffectiveHostSortedByKey(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putDef(store, checks.Definition{OrgID: org, Key: "service", Plugin: checks.PluginService, Params: map[string]any{"units": []any{"sshd"}}, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	putDef(store, checks.Definition{OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	putDef(store, checks.Definition{OrgID: org, Key: "disk", Plugin: checks.PluginDisk, Interval: 30 * time.Second, Enabled: false, BindingKind: checks.BindingOrg})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	defs, err := svc.EffectiveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, d := range defs {
		keys = append(keys, d.Key)
	}
	want := []string{"cpu", "disk", "service"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
	}
}

// TestEffectiveHostIgnoresTheMonitoredGate proves EffectiveHost's
// documented, deliberate difference from ResolveHost: an unmonitored host
// still gets a definite per-key winner (a caller building a page for a
// specific host already knows separately whether it is monitored).
func TestEffectiveHostIgnoresTheMonitoredGate(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	// roster.monitored has no entry for host: not monitored.
	putDef(store, checks.Definition{OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))

	defs, err := svc.EffectiveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Key != "cpu" {
		t.Fatalf("EffectiveHost = %+v, want the organization's cpu definition even though host is unmonitored", defs)
	}
}

func TestEffectiveHostPropagatesErrors(t *testing.T) {
	store, matcher, _, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	store.err = errors.New("store down")
	svc := checks.NewService(store, checks.WithMatcher(matcher))

	if _, err := svc.EffectiveHost(ctx, org, host); err == nil {
		t.Fatal("want the store's error propagated")
	}
}
