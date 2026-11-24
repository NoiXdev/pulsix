package app

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// emptyHostChecks resolves no check and no group: the presence read below
// does not depend on either.
type emptyHostChecks struct{}

func (emptyHostChecks) ContextForHost(context.Context, uuid.UUID, uuid.UUID) (rules.CheckContext, error) {
	return rules.CheckContext{}, nil
}

// TestEvalStoreJudgesAgentsOnlineLikeTheAgentsList pins the one copy of
// the online window rule the evaluator has (backlog 3.57, 3.58): the store
// Build wires must read an agent online exactly while the agents list
// does, last_seen within agents.OnlineWindow of the agent's reported
// heartbeat interval, and hand the same window on as the unknown grace. A
// client whose agent is read as offline too early is frozen while the GUI
// still shows it online, and one read as online too late goes unknown
// while the GUI shows it off. Both timestamps are written by the
// database's own clock, the clock Load compares them with, so the margins
// only have to clear the time between the two statements.
func TestEvalStoreJudgesAgentsOnlineLikeTheAgentsList(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, d, "app-eval-online")
	zone, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: "z", Name: "z"})
	if err != nil {
		t.Fatal(err)
	}
	host, err := q.CreateHost(ctx, gen.CreateHostParams{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone.ID, Name: "nb", Facts: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE hosts SET kind = 'client' WHERE id = $1`, host.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO host_agents (host_id, org_id, agent_name, cert_serial) VALUES ($1, $2, 'nb', $3)`,
		host.ID, org, uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	store := newEvalStore(d, emptyHostChecks{}, items.NewSQLStore(d))
	twoMinutes := 2 * time.Minute
	for _, interval := range []*time.Duration{nil, &twoMinutes} {
		window := agents.OnlineWindow(interval)
		var seconds *int32
		if interval != nil {
			s := int32(interval.Seconds())
			seconds = &s
		}
		for _, tc := range []struct {
			ago    time.Duration
			online bool
		}{
			{window - 5*time.Second, true},
			{window + 5*time.Second, false},
		} {
			if _, err := d.Pool().Exec(ctx,
				`UPDATE host_agents SET last_seen = now() - make_interval(secs => $2), heartbeat_interval_s = $3
				 WHERE host_id = $1`,
				host.ID, tc.ago.Seconds(), seconds); err != nil {
				t.Fatal(err)
			}
			load, err := store.Load(ctx, rules.Due{OrgID: org, HostID: host.ID}, time.Now())
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if load.Presence.AgentOnline != tc.online {
				t.Errorf("interval %v, last seen %v ago: online = %v, want %v (agents.OnlineWindow = %v)",
					interval, tc.ago, load.Presence.AgentOnline, tc.online, window)
			}
			if load.Presence.OnlineWindow != window {
				t.Errorf("interval %v: the unknown grace's window = %v, want agents.OnlineWindow = %v",
					interval, load.Presence.OnlineWindow, window)
			}
		}
	}
}
