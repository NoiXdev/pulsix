package alerts_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestReadsCarryTheAgentsLastContact (backlog 3.57): GetAlert and
// ListAlerts join the host's agent for the absent warning's "not seen
// since …" — its last_seen, the registration when it has not touched
// since, and nothing for a host without an agent.
func TestReadsCarryTheAgentsLastContact(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()

	tx := beginTx(t, d)
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: alerts.Subject{HostID: host, Scope: alerts.ScopeHost}, At: time.Now().UTC(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	read := func() (get, list time.Time) {
		t.Helper()
		rows, _, err := st.ListAlerts(ctx, org, alerts.Filter{}, "", 10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("ListAlerts: %v / %d rows", err, len(rows))
		}
		one, err := st.GetAlert(ctx, org, rows[0].ID)
		if err != nil {
			t.Fatalf("GetAlert: %v", err)
		}
		return one.AgentLastContact, rows[0].AgentLastContact
	}

	if get, list := read(); !get.IsZero() || !list.IsZero() {
		t.Fatalf("no agent: last contact get=%v list=%v, want zero", get, list)
	}

	var created time.Time
	if err := d.Pool().QueryRow(ctx, `
		INSERT INTO host_agents (host_id, org_id, agent_name, cert_serial)
		VALUES ($1, $2, 'agent', 'serial-contact') RETURNING created_at`, host, org).Scan(&created); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if get, list := read(); !get.Equal(created) || !list.Equal(created) {
		t.Fatalf("never touched: last contact get=%v list=%v, want the registration %v", get, list, created)
	}

	var seen time.Time
	if err := d.Pool().QueryRow(ctx,
		`UPDATE host_agents SET last_seen = now() - interval '15 days' WHERE host_id = $1 RETURNING last_seen`,
		host).Scan(&seen); err != nil {
		t.Fatalf("set last_seen: %v", err)
	}
	if get, list := read(); !get.Equal(seen) || !list.Equal(seen) {
		t.Fatalf("last contact get=%v list=%v, want last_seen %v", get, list, seen)
	}
}
