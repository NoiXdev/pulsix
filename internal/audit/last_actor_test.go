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

// TestSQLStoreLastActor: who last did one of the actions to an object
// (spec 2026-11-25 C2: who paused a schedule before an assistant's
// change); other actions and objects do not count; none found is nil.
func TestSQLStoreLastActor(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	org := seedOrg(t, q, "audit-last-actor")
	first, second := seedUser(t, q, "first@example.test"), seedUser(t, q, "second@example.test")
	ctx := db.WithOrgScope(context.Background(), org)
	target := uuid.Must(uuid.NewV7()).String()
	at := time.Now().UTC().Truncate(time.Microsecond)
	for i, e := range []struct {
		actor          uuid.UUID
		action, object string
	}{
		{first, "POST pauseSchedule", target},
		{second, "PUT updateSchedule", target},
		{second, "POST pauseSchedule", uuid.Must(uuid.NewV7()).String()},
	} {
		actor := e.actor
		if err := store.Insert(ctx, audit.Event{ID: uuid.Must(uuid.NewV7()), OrgID: &org, ActorID: &actor, ActorKind: audit.ActorKindUser,
			Action: e.action, ObjectType: "schedule", ObjectID: e.object, At: at.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.LastActor(ctx, org, "schedule", target, []string{"POST pauseSchedule"})
	if err != nil || got == nil || *got != first {
		t.Fatalf("last actor = %v, %v", got, err)
	}
	got, err = store.LastActor(ctx, org, "schedule", uuid.Must(uuid.NewV7()).String(), []string{"POST pauseSchedule"})
	if err != nil || got != nil {
		t.Fatalf("nobody = %v, %v", got, err)
	}
}
