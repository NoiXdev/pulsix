package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

var pauses = audit.StopActions{Stop: []string{"POST pauseSchedule"}, All: []string{"POST pauseAllSchedules"}, Start: []string{"POST resumeSchedule"}}

// TestSQLStoreLastActor: who stopped an object (spec 2026-11-25 C2: who
// paused a schedule before an assistant's change) is the newest stop on it,
// or a stop of every object of its kind (pause all), after its newest
// start (resume); other objects do not count; nothing found is nil. A stop
// made with an assistant token names the token and says so.
func TestSQLStoreLastActor(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	org := seedOrg(t, q, "audit-last-actor")
	first, second, third := seedUser(t, q, "first@example.test"), seedUser(t, q, "second@example.test"), seedUser(t, q, "third@example.test")
	ctx := db.WithOrgScope(context.Background(), org)
	target := uuid.Must(uuid.NewV7()).String()
	at := time.Now().UTC().Truncate(time.Microsecond)
	n := 0
	insert := func(actor uuid.UUID, token *uuid.UUID, action, object string) {
		t.Helper()
		n++
		if err := store.Insert(ctx, audit.Event{ID: uuid.Must(uuid.NewV7()), OrgID: &org, ActorID: &actor, ActorKind: audit.ActorKindUser, TokenID: token,
			Action: action, ObjectType: "schedule", ObjectID: object, At: at.Add(time.Duration(n) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	insert(first, nil, "POST pauseSchedule", target)
	insert(second, nil, "PUT updateSchedule", target)
	insert(second, nil, "POST pauseSchedule", uuid.Must(uuid.NewV7()).String())
	got, err := store.LastActor(ctx, org, "schedule", target, pauses)
	if err != nil || got == nil || got.UserID == nil || *got.UserID != first || got.Assistant {
		t.Fatalf("last actor = %+v, %v", got, err)
	}
	// Resumed, then paused for all: the pause-all's actor.
	insert(first, nil, "POST resumeSchedule", target)
	if got, err = store.LastActor(ctx, org, "schedule", target, pauses); err != nil || got != nil {
		t.Fatalf("after a resume = %+v, %v", got, err)
	}
	insert(third, nil, "POST pauseAllSchedules", "")
	if got, err = store.LastActor(ctx, org, "schedule", target, pauses); err != nil || got == nil || *got.UserID != third {
		t.Fatalf("pause all = %+v, %v", got, err)
	}
	// Paused with an assistant token: the token is named.
	tok := uuid.Must(uuid.NewV7())
	if _, err := d.Pool().Exec(context.Background(), `INSERT INTO api_tokens (id, org_id, user_id, name, role, secret_hash, kind, areas)
		VALUES ($1, $2, $3, 'claude-ops', 'operator', $4, 'assistant', '{automation.request}')`, tok, org, second, []byte(tok.String())); err != nil {
		t.Fatal(err)
	}
	insert(second, &tok, "POST pauseSchedule", target)
	if got, err = store.LastActor(ctx, org, "schedule", target, pauses); err != nil || got == nil || !got.Assistant || got.TokenName != "claude-ops" {
		t.Fatalf("assistant = %+v, %v", got, err)
	}
}
