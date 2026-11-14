package checks_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

// fakeLocalChecks is the checks.LocalCheckSource test double: the checks
// each host's agent reported, and how often each method was called.
type fakeLocalChecks struct {
	byHost     map[uuid.UUID][]checks.LocalCheck
	err        error
	hostCalls  int
	hostsCalls int
	// asked is the id list of the last LocalChecksByHosts call.
	asked []uuid.UUID
}

func (f *fakeLocalChecks) LocalChecksByHost(_ context.Context, _, hostID uuid.UUID) ([]checks.LocalCheck, error) {
	f.hostCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.byHost[hostID], nil
}

func (f *fakeLocalChecks) LocalChecksByHosts(_ context.Context, _ uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID][]checks.LocalCheck, error) {
	f.hostsCalls++
	f.asked = slices.Clone(hostIDs)
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID][]checks.LocalCheck{}
	for _, id := range hostIDs {
		if c, ok := f.byHost[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func localKeys(rs []checks.Resolved) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Key
	}
	return out
}

// TestResolveHostLocalChecks (backlog 3.5a): a check defined in the host's
// agent.yaml resolves into Local, sorted by key, bound at BindingLocal with
// no source id; All() lists it after Checks and Synthetic; and Version —
// what the agent compares the manager's push against — is byte for byte
// what it is without the local check, since the push never carries it.
func TestResolveHostLocalChecks(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putDef(store, checks.Definition{OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	putDef(store, checks.Definition{
		OrgID: org, Key: "web", Plugin: checks.PluginHTTP, Params: map[string]any{"url": "https://{address}/"},
		Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg,
	})
	without, err := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster)).ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}

	local := &fakeLocalChecks{byHost: map[uuid.UUID][]checks.LocalCheck{host: {
		{Key: "local-x", Plugin: "script", Params: map[string]any{"commands": []any{"backup_age"}}, Interval: 5 * time.Minute},
		{Key: "local-a", Plugin: checks.PluginCPU, Interval: time.Minute},
	}}}
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster), checks.WithLocalChecks(local))
	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if got := localKeys(rs.Local); !slices.Equal(got, []string{"local-a", "local-x"}) {
		t.Fatalf("Local = %v, want local-a then local-x", got)
	}
	x := rs.Local[1]
	if x.Plugin != "script" || x.Interval != 5*time.Minute || x.Source != checks.BindingLocal || x.SourceID != nil ||
		len(x.Params) != 1 {
		t.Fatalf("Local local-x = %+v, want the script check bound at local with no source id", x)
	}
	if checks.BindingLocal != "local" {
		t.Fatalf("BindingLocal = %q, want local", checks.BindingLocal)
	}
	if rs.Version != without.Version {
		t.Fatalf("Version = %s, want %s: local checks are never pushed, so they must not change it", rs.Version, without.Version)
	}
	if len(rs.Checks) != 1 || len(rs.Synthetic) != 1 {
		t.Fatalf("Checks = %+v, Synthetic = %+v, want cpu and web untouched", rs.Checks, rs.Synthetic)
	}
	if got := localKeys(rs.All()); !slices.Equal(got, []string{"cpu", "web", "local-a", "local-x"}) {
		t.Fatalf("All() = %v, want Checks, then Synthetic, then Local", got)
	}
	if local.hostCalls != 1 || local.hostsCalls != 0 {
		t.Fatalf("source calls = %d by host, %d by hosts; want exactly one by host", local.hostCalls, local.hostsCalls)
	}
}

// TestResolveHostLocalKeyLosesToTheManagersCheck: a local key equal to a
// key the manager resolves — an agent check or a synthetic one — is left
// out of Local; the manager's check wins, as on the agent. Such a
// definition can only predate the local- reservation.
func TestResolveHostLocalKeyLosesToTheManagersCheck(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putDef(store, checks.Definition{OrgID: org, Key: "local-dup", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	putDef(store, checks.Definition{
		OrgID: org, Key: "local-probe", Plugin: checks.PluginHTTP, Params: map[string]any{"url": "https://{address}/"},
		Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg,
	})
	local := &fakeLocalChecks{byHost: map[uuid.UUID][]checks.LocalCheck{host: {
		{Key: "local-dup", Plugin: checks.PluginMemory, Interval: time.Minute},
		{Key: "local-ok", Plugin: checks.PluginMemory, Interval: time.Minute},
		{Key: "local-probe", Plugin: checks.PluginMemory, Interval: time.Minute},
	}}}
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster), checks.WithLocalChecks(local))

	rs, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if got := localKeys(rs.Local); !slices.Equal(got, []string{"local-ok"}) {
		t.Fatalf("Local = %v, want only local-ok", got)
	}
	if rs.Checks[0].Plugin != checks.PluginCPU || rs.Synthetic[0].Plugin != checks.PluginHTTP {
		t.Fatalf("Checks = %+v, Synthetic = %+v, want the manager's definitions", rs.Checks, rs.Synthetic)
	}
}

// TestResolveHostLocalChecksEmpty: an unmonitored host resolves to an
// empty set, local checks included (the agent still runs them; their
// values are stored, not judged), and a Service built without
// WithLocalChecks has none. Local is a non-nil empty slice either way.
func TestResolveHostLocalChecksEmpty(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	monitored, cold := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	roster.monitored[monitored], roster.monitored[cold] = true, false
	putDef(store, checks.Definition{OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	local := &fakeLocalChecks{byHost: map[uuid.UUID][]checks.LocalCheck{
		monitored: {{Key: "local-a", Plugin: checks.PluginCPU, Interval: time.Minute}},
		cold:      {{Key: "local-a", Plugin: checks.PluginCPU, Interval: time.Minute}},
	}}

	withSource := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster), checks.WithLocalChecks(local))
	rs, err := withSource.ResolveHost(ctx, org, cold)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Local == nil || len(rs.Local) != 0 {
		t.Fatalf("unmonitored host: Local = %#v, want a non-nil empty slice", rs.Local)
	}
	if local.hostCalls != 0 {
		t.Fatalf("an unmonitored host read the local checks %d times, want none", local.hostCalls)
	}

	withoutSource := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster))
	rs, err = withoutSource.ResolveHost(ctx, org, monitored)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Local == nil || len(rs.Local) != 0 || len(rs.Checks) != 1 {
		t.Fatalf("without WithLocalChecks: %+v, want cpu and a non-nil empty Local", rs)
	}
}

// TestResolveOrgAndHostsFillLocal: the batch paths fill Local per host
// from one read for all their hosts — the monitored ones only — and agree
// with ResolveHost host for host.
func TestResolveOrgAndHostsFillLocal(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	h1, h2, cold := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	roster.monitored[h1], roster.monitored[h2], roster.monitored[cold] = true, true, false
	putDef(store, checks.Definition{OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	local := &fakeLocalChecks{byHost: map[uuid.UUID][]checks.LocalCheck{
		h1:   {{Key: "local-one", Plugin: checks.PluginCPU, Interval: time.Minute}},
		cold: {{Key: "local-cold", Plugin: checks.PluginCPU, Interval: time.Minute}},
	}}
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster), checks.WithLocalChecks(local))

	byOrg, err := svc.ResolveOrg(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if local.hostsCalls != 1 || local.hostCalls != 0 {
		t.Fatalf("ResolveOrg: %d batch and %d single reads, want one batch read", local.hostsCalls, local.hostCalls)
	}
	byHosts, err := svc.ResolveHosts(ctx, org, []uuid.UUID{h1, h2, cold, h1})
	if err != nil {
		t.Fatal(err)
	}
	if local.hostsCalls != 2 || local.hostCalls != 0 {
		t.Fatalf("ResolveHosts: %d batch and %d single reads in all, want one more batch read", local.hostsCalls, local.hostCalls)
	}
	if slices.Contains(local.asked, cold) {
		t.Fatalf("ResolveHosts asked for %v, want the monitored hosts only", local.asked)
	}
	for name, sets := range map[string]map[uuid.UUID]checks.ResolvedSet{"ResolveOrg": byOrg, "ResolveHosts": byHosts} {
		if got := localKeys(sets[h1].Local); !slices.Equal(got, []string{"local-one"}) || sets[h1].Local[0].Source != checks.BindingLocal {
			t.Fatalf("%s: h1 Local = %+v, want local-one", name, sets[h1].Local)
		}
		if sets[h2].Local == nil || len(sets[h2].Local) != 0 {
			t.Fatalf("%s: h2 Local = %#v, want a non-nil empty slice", name, sets[h2].Local)
		}
		one, err := svc.ResolveHost(ctx, org, h1)
		if err != nil {
			t.Fatal(err)
		}
		if one.Version != sets[h1].Version || !slices.Equal(localKeys(one.Local), localKeys(sets[h1].Local)) {
			t.Fatalf("%s: h1 = %+v, ResolveHost = %+v", name, sets[h1], one)
		}
	}
	if _, ok := byOrg[cold]; ok {
		t.Fatal("ResolveOrg resolved an unmonitored host")
	}
	if got := byHosts[cold].Local; got == nil || len(got) != 0 {
		t.Fatalf("ResolveHosts: unmonitored Local = %#v, want a non-nil empty slice", got)
	}
}

// TestResolveLocalChecksPropagatesErrors: a failing read is an error, not
// "this host defines no local checks" — the rule evaluation would take its
// local tuples out of the evaluation on that answer.
func TestResolveLocalChecksPropagatesErrors(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	boom := errors.New("agents store down")
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster), checks.WithLocalChecks(&fakeLocalChecks{err: boom}))

	if _, err := svc.ResolveHost(ctx, org, host); !errors.Is(err, boom) {
		t.Fatalf("ResolveHost = %v, want the source's error", err)
	}
	if _, err := svc.ResolveHosts(ctx, org, []uuid.UUID{host}); !errors.Is(err, boom) {
		t.Fatalf("ResolveHosts = %v, want the source's error", err)
	}
	if _, err := svc.ResolveOrg(ctx, org); !errors.Is(err, boom) {
		t.Fatalf("ResolveOrg = %v, want the source's error", err)
	}
}
