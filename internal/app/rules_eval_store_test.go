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
// the online window the evaluator has (backlog 3.57): the store Build
// wires must read an agent online exactly while the agents list does,
// last_seen within agents.StaleAfter. A client whose agent is read as
// offline too early is frozen while the GUI still shows it online, and
// one read as online too late goes unknown while the GUI shows it off.
// Both timestamps are written by the database's own clock, the clock Load
// compares them with, so the margins only have to clear the time between
// the two statements.
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
	if _, err := d.Pool().Exec(ctx, `
		INSERT INTO host_agents (host_id, org_id, agent_name, cert_serial) VALUES ($1, $2, 'nb', $3)`,
		host.ID, org, uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	store := newEvalStore(d, emptyHostChecks{}, items.NewSQLStore(d))
	for _, tc := range []struct {
		ago    time.Duration
		online bool
	}{
		{agents.StaleAfter - 5*time.Second, true},
		{agents.StaleAfter + 5*time.Second, false},
	} {
		if _, err := d.Pool().Exec(ctx,
			`UPDATE host_agents SET last_seen = now() - make_interval(secs => $2) WHERE host_id = $1`,
			host.ID, tc.ago.Seconds()); err != nil {
			t.Fatal(err)
		}
		load, err := store.Load(ctx, rules.Due{OrgID: org, HostID: host.ID})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if load.Presence.AgentOnline != tc.online {
			t.Errorf("last seen %v ago: online = %v, want %v (agents.StaleAfter = %v)",
				tc.ago, load.Presence.AgentOnline, tc.online, agents.StaleAfter)
		}
	}
}
