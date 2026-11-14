package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checks"
)

// fakeAgentLocalChecks answers like agents.Service: a report per host, an
// unknown report for a host without one.
type fakeAgentLocalChecks struct {
	reports map[uuid.UUID]agents.LocalChecksReport
	err     error
}

func (f fakeAgentLocalChecks) LocalChecksByHost(_ context.Context, _, hostID uuid.UUID) (agents.LocalChecksReport, error) {
	return f.reports[hostID], f.err
}

func (f fakeAgentLocalChecks) LocalChecksByHosts(_ context.Context, _ uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]agents.LocalChecksReport, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]agents.LocalChecksReport{}
	for _, id := range hostIDs {
		if r, ok := f.reports[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

// TestChecksLocalChecksAdapter (backlog 3.5a): the agent's stored report
// becomes the resolver's local checks field for field; an unknown report
// (an older agent, no heartbeat yet, no agent) is no local checks, and a
// failing read is the read's error.
func TestChecksLocalChecksAdapter(t *testing.T) {
	ctx := context.Background()
	org := uuid.Must(uuid.NewV7())
	known, unknown, none := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	params := map[string]any{"secret": "pg_main"}
	src := checksLocalChecks{agents: fakeAgentLocalChecks{reports: map[uuid.UUID]agents.LocalChecksReport{
		known: {Known: true, Checks: []agents.LocalCheck{
			{Key: "local-pg", Plugin: "postgres", Params: params, Interval: 5 * time.Minute},
		}, Problems: []string{"line 3: an unknown plugin"}},
		unknown: {},
	}}}

	want := []checks.LocalCheck{{Key: "local-pg", Plugin: "postgres", Params: params, Interval: 5 * time.Minute}}
	got, err := src.LocalChecksByHost(ctx, org, known)
	if err != nil || len(got) != 1 || got[0].Key != want[0].Key || got[0].Plugin != want[0].Plugin ||
		got[0].Interval != want[0].Interval || got[0].Params["secret"] != "pg_main" {
		t.Fatalf("LocalChecksByHost(known) = %+v, %v, want %+v", got, err, want)
	}
	for _, id := range []uuid.UUID{unknown, none} {
		if got, err := src.LocalChecksByHost(ctx, org, id); err != nil || len(got) != 0 {
			t.Fatalf("LocalChecksByHost(no report) = %+v, %v, want none", got, err)
		}
	}

	byHost, err := src.LocalChecksByHosts(ctx, org, []uuid.UUID{known, unknown, none})
	if err != nil {
		t.Fatal(err)
	}
	if len(byHost[known]) != 1 || byHost[known][0].Key != "local-pg" || len(byHost[unknown]) != 0 || len(byHost[none]) != 0 {
		t.Fatalf("LocalChecksByHosts = %+v, want local-pg for the known report only", byHost)
	}
	for _, id := range []uuid.UUID{unknown, none} {
		if _, ok := byHost[id]; ok {
			t.Fatalf("LocalChecksByHosts listed a host without local checks: %+v", byHost)
		}
	}

	boom := errors.New("agents store down")
	failing := checksLocalChecks{agents: fakeAgentLocalChecks{err: boom}}
	if _, err := failing.LocalChecksByHost(ctx, org, known); !errors.Is(err, boom) {
		t.Fatalf("LocalChecksByHost = %v, want the read's error", err)
	}
	if _, err := failing.LocalChecksByHosts(ctx, org, []uuid.UUID{known}); !errors.Is(err, boom) {
		t.Fatalf("LocalChecksByHosts = %v, want the read's error", err)
	}
}
