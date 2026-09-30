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
