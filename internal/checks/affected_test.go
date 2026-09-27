package checks_test

import (
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

// affectedFixture builds a service whose roster reports two monitored
// hosts out of three, and whose matcher answers per selector.
type affectedFixture struct {
	svc                     *checks.Service
	org                     uuid.UUID
	web, dbHost, unmonitored uuid.UUID
	store                   *fakeStore
	matcher                 *fakeGroupMatcher
	roster                  *fakeHostRoster
}

func newAffectedFixture(t *testing.T) affectedFixture {
	t.Helper()
	org := uuid.Must(uuid.NewV7())
	web, dbHost, cold := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store := newFakeStore()
	matcher := &fakeGroupMatcher{bySelector: map[string][]uuid.UUID{
		"role=web": {web},
		"role=db":  {dbHost},
		"role=all": {web, dbHost, cold},
	}}
	roster := &fakeHostRoster{monitored: map[uuid.UUID]bool{web: true, dbHost: true, cold: false}}
	return affectedFixture{
		svc: checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster)),
		org: org, web: web, dbHost: dbHost, unmonitored: cold,
		store: store, matcher: matcher, roster: roster,
	}
}

func sorted(ids []uuid.UUID) []uuid.UUID {
	out := append([]uuid.UUID(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func sameIDs(got, want []uuid.UUID) bool {
	if len(got) != len(want) {
		return false
	}
	g, w := sorted(got), sorted(want)
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

// TestAffectedHostsPerBindingKind: which hosts a write at each scope
// reaches. An organization-bound definition reaches every monitored host,
// which the caller pushes in one pass rather than host by host — so it is
// reported as WholeOrg, not as a list.
func TestAffectedHostsPerBindingKind(t *testing.T) {
	f := newAffectedFixture(t)
	g, err := f.svc.CreateGroup(ctx, f.org, checks.GroupInput{Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatal(err)
	}

	orgBound, err := f.svc.AffectedHosts(ctx, f.org, checks.Binding{Kind: checks.BindingOrg})
	if err != nil {
		t.Fatal(err)
	}
	if !orgBound.WholeOrg || len(orgBound.Hosts) != 0 {
		t.Fatalf("org binding = %+v, want WholeOrg with no host list", orgBound)
	}
	// The roster is not consulted for it: the whole point of WholeOrg is
	// that the caller never materialises the list.
	if f.roster.err = errors.New("roster must not be read"); true {
		if _, err := f.svc.AffectedHosts(ctx, f.org, checks.Binding{Kind: checks.BindingOrg}); err != nil {
			t.Fatalf("an org binding must not read the host roster: %v", err)
		}
		f.roster.err = nil
	}

	hostBound, err := f.svc.AffectedHosts(ctx, f.org, checks.Binding{Kind: checks.BindingHost, HostID: &f.web})
	if err != nil {
		t.Fatal(err)
	}
	if hostBound.WholeOrg || !sameIDs(hostBound.Hosts, []uuid.UUID{f.web}) {
		t.Fatalf("host binding = %+v, want exactly that host", hostBound)
	}

	groupBound, err := f.svc.AffectedHosts(ctx, f.org, checks.Binding{Kind: checks.BindingGroup, GroupID: &g.ID})
	if err != nil {
		t.Fatal(err)
	}
	if groupBound.WholeOrg || !sameIDs(groupBound.Hosts, []uuid.UUID{f.web}) {
		t.Fatalf("group binding = %+v, want the group's members", groupBound)
	}
}

// TestAffectedHostsUnionsSeveralBindings: a write that moves a definition
// from one scope to another affects both the hosts that lose it and the
// ones that gain it, each pushed once.
func TestAffectedHostsUnionsSeveralBindings(t *testing.T) {
	f := newAffectedFixture(t)
	both, err := f.svc.AffectedHosts(ctx, f.org,
		checks.Binding{Kind: checks.BindingHost, HostID: &f.web},
		checks.Binding{Kind: checks.BindingHost, HostID: &f.dbHost},
		checks.Binding{Kind: checks.BindingHost, HostID: &f.web},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !sameIDs(both.Hosts, []uuid.UUID{f.web, f.dbHost}) {
		t.Fatalf("union = %+v, want each host once", both.Hosts)
	}

	// An organization-bound side swallows the rest: every monitored host
	// is already in it.
	mixed, err := f.svc.AffectedHosts(ctx, f.org,
		checks.Binding{Kind: checks.BindingHost, HostID: &f.web},
		checks.Binding{Kind: checks.BindingOrg},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !mixed.WholeOrg || len(mixed.Hosts) != 0 {
		t.Fatalf("mixed = %+v, want WholeOrg to absorb the single host", mixed)
	}
}

// TestAffectedHostsForGroupNeedsABinding: a group carries no configuration
// of its own, so neither its selector nor its priority can change what any
// host resolves to while no definition binds to it. That rule is the
// domain's, not the caller's.
func TestAffectedHostsForGroupNeedsABinding(t *testing.T) {
	f := newAffectedFixture(t)
	g, err := f.svc.CreateGroup(ctx, f.org, checks.GroupInput{Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatal(err)
	}

	before, err := f.svc.AffectedHostsForGroup(ctx, f.org, g.ID, "role=web")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Hosts) != 0 || before.WholeOrg {
		t.Fatalf("a group nothing binds to = %+v, want nothing", before)
	}
	if f.matcher.calls != 0 {
		t.Fatalf("the selector was expanded %d times for a group nothing binds to", f.matcher.calls)
	}

	if _, err := f.svc.CreateDefinition(ctx, f.org, checks.DefinitionInput{
		Plugin: checks.PluginCPU, Interval: time.Minute, Enabled: true,
		BindingKind: checks.BindingGroup, GroupID: &g.ID,
	}); err != nil {
		t.Fatal(err)
	}
	// Both the old and the new selector text: the hosts that stop matching
	// lose the group's checks, and the ones that start matching gain them.
	after, err := f.svc.AffectedHostsForGroup(ctx, f.org, g.ID, "role=web", "role=db")
	if err != nil {
		t.Fatal(err)
	}
	if after.WholeOrg || !sameIDs(after.Hosts, []uuid.UUID{f.web, f.dbHost}) {
		t.Fatalf("a bound group = %+v, want both selectors' hosts", after)
	}

	// A definition bound to a *different* group does not make this one
	// matter.
	other, err := f.svc.CreateGroup(ctx, f.org, checks.GroupInput{Name: "db", Selector: "role=db"})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := f.svc.AffectedHostsForGroup(ctx, f.org, other.ID, "role=db")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Hosts) != 0 {
		t.Fatalf("an unbound sibling group = %+v, want nothing", empty)
	}
}

// TestAffectedHostsRefusalsAndFaults: a malformed binding and a failing
// port are reported, never silently answered with an empty set — the
// caller logs them, and a silent empty answer would look like "nothing to
// push".
func TestAffectedHostsRefusalsAndFaults(t *testing.T) {
	f := newAffectedFixture(t)
	if _, err := f.svc.AffectedHosts(ctx, f.org, checks.Binding{Kind: checks.BindingHost}); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("a host binding with no host = %v, want ErrInvalidInput", err)
	}
	if _, err := f.svc.AffectedHosts(ctx, f.org, checks.Binding{Kind: checks.BindingGroup}); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("a group binding with no group = %v, want ErrInvalidInput", err)
	}
	if _, err := f.svc.AffectedHosts(ctx, f.org, checks.Binding{Kind: checks.BindingKind("nonsense")}); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("an unknown binding kind = %v, want ErrInvalidInput", err)
	}
	unknown := uuid.Must(uuid.NewV7())
	if _, err := f.svc.AffectedHosts(ctx, f.org, checks.Binding{Kind: checks.BindingGroup, GroupID: &unknown}); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("a binding to a group that does not exist = %v, want ErrNotFound", err)
	}

	g, err := f.svc.CreateGroup(ctx, f.org, checks.GroupInput{Name: "web", Selector: "role=web"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateDefinition(ctx, f.org, checks.DefinitionInput{
		Plugin: checks.PluginCPU, Interval: time.Minute, Enabled: true,
		BindingKind: checks.BindingGroup, GroupID: &g.ID,
	}); err != nil {
		t.Fatal(err)
	}
	f.matcher.err = errors.New("hosts store down")
	if _, err := f.svc.AffectedHostsForGroup(ctx, f.org, g.ID, "role=web"); err == nil {
		t.Fatal("a failing matcher must surface, not answer an empty set")
	}
}

// TestBindingOfADefinition: a stored definition and the input that
// produced it describe the same scope, so a write can ask about its own
// before and after state without either caller rebuilding the rule.
func TestBindingOfADefinition(t *testing.T) {
	hostID := uuid.Must(uuid.NewV7())
	in := checks.DefinitionInput{
		Plugin: checks.PluginCPU, Interval: time.Minute, Enabled: true,
		BindingKind: checks.BindingHost, HostID: &hostID,
	}
	svc := checks.NewService(newFakeStore())
	d, err := svc.CreateDefinition(ctx, uuid.Must(uuid.NewV7()), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.Binding() != in.Binding() {
		t.Fatalf("definition binding %+v != input binding %+v", d.Binding(), in.Binding())
	}
	if d.Binding().Kind != checks.BindingHost || d.Binding().HostID == nil || *d.Binding().HostID != hostID {
		t.Fatalf("binding = %+v", d.Binding())
	}
}
