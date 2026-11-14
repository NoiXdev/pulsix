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
// each host's agent reported, and how often it was read.
type fakeLocalChecks struct {
	byHost    map[uuid.UUID][]checks.LocalCheck
	err       error
	hostCalls int
}

func (f *fakeLocalChecks) LocalChecksByHost(_ context.Context, _, hostID uuid.UUID) ([]checks.LocalCheck, error) {
	f.hostCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.byHost[hostID], nil
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
	rs, err := svc.ResolveHostWithLocal(ctx, org, host)
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
	if local.hostCalls != 1 {
		t.Fatalf("source read %d times, want once", local.hostCalls)
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
	// Disabled, a definition runs nowhere, so it shadows nothing: the
	// agent runs its own check under that key.
	putDef(store, checks.Definition{OrgID: org, Key: "local-off", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: false, BindingKind: checks.BindingOrg})
	local.byHost[host] = append(local.byHost[host], checks.LocalCheck{Key: "local-off", Plugin: checks.PluginMemory, Interval: time.Minute})
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster), checks.WithLocalChecks(local))

	rs, err := svc.ResolveHostWithLocal(ctx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	if got := localKeys(rs.Local); !slices.Equal(got, []string{"local-off", "local-ok"}) {
		t.Fatalf("Local = %v, want local-off and local-ok", got)
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
	rs, err := withSource.ResolveHostWithLocal(ctx, org, cold)
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
	rs, err = withoutSource.ResolveHostWithLocal(ctx, org, monitored)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Local == nil || len(rs.Local) != 0 || len(rs.Checks) != 1 {
		t.Fatalf("without WithLocalChecks: %+v, want cpu and a non-nil empty Local", rs)
	}
}

// TestOnlyResolveHostWithLocalReadsLocalChecks: ResolveHost, ResolveHosts
// and ResolveOrg feed the push, the heartbeat comparison and the probes,
// which never use Local — so they never read the agents' reports, and a
// failing read cannot fail them. ResolveHostWithLocal, the resolve of the
// callers that judge or show tuples, reads them and reports a failing read
// as an error, not as "this host defines no local checks" (the rule
// evaluation would take its local tuples out on that answer). The sets
// agree otherwise, Version included.
func TestOnlyResolveHostWithLocalReadsLocalChecks(t *testing.T) {
	store, matcher, roster, org := newResolverFixture()
	host := uuid.Must(uuid.NewV7())
	roster.monitored[host] = true
	putDef(store, checks.Definition{OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: 30 * time.Second, Enabled: true, BindingKind: checks.BindingOrg})
	local := &fakeLocalChecks{byHost: map[uuid.UUID][]checks.LocalCheck{host: {{Key: "local-a", Plugin: checks.PluginCPU, Interval: time.Minute}}}}
	svc := checks.NewService(store, checks.WithMatcher(matcher), checks.WithHostRoster(roster), checks.WithLocalChecks(local))

	with, err := svc.ResolveHostWithLocal(ctx, org, host)
	if err != nil || len(with.Local) != 1 {
		t.Fatalf("ResolveHostWithLocal = %+v, %v; want local-a", with, err)
	}
	local.hostCalls = 0
	local.err = errors.New("agents store down")

	one, err := svc.ResolveHost(ctx, org, host)
	if err != nil {
		t.Fatalf("ResolveHost with a failing local source = %v, want nil", err)
	}
	byHosts, err := svc.ResolveHosts(ctx, org, []uuid.UUID{host})
	if err != nil {
		t.Fatalf("ResolveHosts with a failing local source = %v, want nil", err)
	}
	byOrg, err := svc.ResolveOrg(ctx, org)
	if err != nil {
		t.Fatalf("ResolveOrg with a failing local source = %v, want nil", err)
	}
	for name, rs := range map[string]checks.ResolvedSet{"ResolveHost": one, "ResolveHosts": byHosts[host], "ResolveOrg": byOrg[host]} {
		if rs.Local == nil || len(rs.Local) != 0 {
			t.Fatalf("%s: Local = %#v, want a non-nil empty slice", name, rs.Local)
		}
		if rs.Version != with.Version || len(rs.Checks) != 1 {
			t.Fatalf("%s = %+v, want the same Checks and Version as ResolveHostWithLocal", name, rs)
		}
	}
	if local.hostCalls != 0 {
		t.Fatalf("the push and probe resolves read the local checks %d times, want none", local.hostCalls)
	}

	if _, err := svc.ResolveHostWithLocal(ctx, org, host); !errors.Is(err, local.err) {
		t.Fatalf("ResolveHostWithLocal = %v, want the source's error", err)
	}
}
