package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

type fakeCheckResolver struct {
	set       checks.ResolvedSet
	groups    []checks.Group
	resolved  error
	groupsErr error
}

func (f *fakeCheckResolver) ResolveHost(context.Context, uuid.UUID, uuid.UUID) (checks.ResolvedSet, error) {
	return f.set, f.resolved
}

func (f *fakeCheckResolver) GroupsForHost(context.Context, uuid.UUID, uuid.UUID) ([]checks.Group, error) {
	return f.groups, f.groupsErr
}

// TestRulesHostChecksContextForHost pins the translation the evaluation
// depends on: an interval per check key (the thing staleness is measured
// against) and the group ranking the rule resolver tie-breaks on.
func TestRulesHostChecksContextForHost(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	group := uuid.Must(uuid.NewV7())
	r := &fakeCheckResolver{
		set: checks.ResolvedSet{Checks: []checks.Resolved{
			{Key: "cpu", Interval: time.Minute},
			{Key: "disk", Interval: 5 * time.Minute},
		}},
		groups: []checks.Group{{ID: group, Priority: 7, CreatedAt: created, Name: "web"}},
	}
	got, err := rulesHostChecks{checks: r}.ContextForHost(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatal(err)
	}
	if got.Intervals["cpu"] != time.Minute || got.Intervals["disk"] != 5*time.Minute {
		t.Fatalf("intervals = %+v", got.Intervals)
	}
	if len(got.Groups) != 1 || got.Groups[0].ID != group || got.Groups[0].Priority != 7 || !got.Groups[0].CreatedAt.Equal(created) {
		t.Fatalf("groups = %+v", got.Groups)
	}
}

// TestRulesHostChecksContextForHostIncludesSyntheticChecks: a synthetic
// check (backlog 3.13) is never pushed to the agent, but its results land
// in the same tuples, so rules on its metrics need its interval exactly
// like an agent check's — without it the evaluation would drop the tuple.
func TestRulesHostChecksContextForHostIncludesSyntheticChecks(t *testing.T) {
	r := &fakeCheckResolver{set: checks.ResolvedSet{
		Checks:    []checks.Resolved{{Key: "cpu", Interval: time.Minute}},
		Synthetic: []checks.Resolved{{Key: "web", Plugin: checks.PluginHTTP, Interval: 30 * time.Second}},
	}}
	got, err := rulesHostChecks{checks: r}.ContextForHost(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Intervals) != 2 || got.Intervals["cpu"] != time.Minute || got.Intervals["web"] != 30*time.Second {
		t.Fatalf("intervals = %+v, want cpu and the synthetic web", got.Intervals)
	}
}

// TestRulesHostChecksPropagatesFailures: neither half may be silently
// treated as "this host has no checks" — that would take every tuple out
// of the evaluation and delete the host's roll-up.
func TestRulesHostChecksPropagatesFailures(t *testing.T) {
	boom := errors.New("resolver down")
	for name, r := range map[string]*fakeCheckResolver{
		"resolve": {resolved: boom},
		"groups":  {groupsErr: boom},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := rulesHostChecks{checks: r}.ContextForHost(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the resolver's own", err)
			}
		})
	}
}

// TestRulesHostChecksContextForHostAsksPerItem (backlog 3.55): the
// evaluation leaves out the items a check's list no longer names, with
// the very predicate the host page hides them by. A disk check listing
// "/" asks for "/" and not "/data"; the whole-host item "" is asked
// whatever the list says (it carries a check-level error, not a mount);
// a check without a list, and a synthetic check, ask for everything.
func TestRulesHostChecksContextForHostAsksPerItem(t *testing.T) {
	r := &fakeCheckResolver{set: checks.ResolvedSet{
		Checks: []checks.Resolved{
			{Key: "disk", Plugin: checks.PluginDisk, Params: map[string]any{"mounts": []any{"/"}}, Interval: time.Minute},
			{Key: "disk-all", Plugin: checks.PluginDisk, Interval: time.Minute},
			{Key: "cpu", Plugin: checks.PluginCPU, Interval: time.Minute},
		},
		Synthetic: []checks.Resolved{{Key: "web", Plugin: checks.PluginHTTP, Interval: 30 * time.Second}},
	}}
	got, err := rulesHostChecks{checks: r}.ContextForHost(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		key, item string
		want      bool
	}{
		{"disk", "/", true},
		{"disk", "", true},
		{"disk", "/data", false},
		{"disk-all", "/data", true},
		{"cpu", "", true},
		{"web", "", true},
		{"web", "https://example.invalid/", true},
	} {
		asks, ok := got.Asks[c.key]
		if !ok {
			t.Fatalf("no predicate for %q; Asks = %v", c.key, got.Asks)
		}
		if asks(c.item) != c.want {
			t.Errorf("Asks[%q](%q) = %v, want %v", c.key, c.item, !c.want, c.want)
		}
	}
}
