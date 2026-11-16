package assistant_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistant"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func seedOrg(t *testing.T, d *db.DB, slug string) uuid.UUID {
	t.Helper()
	org, err := gen.New(d.Pool()).CreateOrganization(context.Background(), gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func TestSQLStoreDefaultRoundTripAndIsolation(t *testing.T) {
	d := testdb.Open(t)
	svc := assistant.NewService(assistant.NewSQLStore(d))
	a, b := seedOrg(t, d, "asst-a"), seedOrg(t, d, "asst-b")
	ctxA, ctxB := db.WithOrgScope(context.Background(), a), db.WithOrgScope(context.Background(), b)

	if on, err := svc.Enabled(ctxA, a); err != nil || on {
		t.Fatalf("before any write = %v, %v; want off", on, err)
	}
	if err := svc.SetEnabled(ctxA, a, true); err != nil {
		t.Fatal(err)
	}
	if on, err := svc.Enabled(ctxA, a); err != nil || !on {
		t.Fatalf("after turning on = %v, %v", on, err)
	}
	if on, err := svc.Enabled(ctxB, b); err != nil || on {
		t.Fatalf("the other org = %v, %v; want off", on, err)
	}
	// Row-level security: org b's scope cannot see org a's row.
	if on, err := svc.Enabled(ctxB, a); err != nil || on {
		t.Fatalf("org a read inside org b's scope = %v, %v; want off", on, err)
	}
	if err := svc.SetEnabled(ctxA, a, false); err != nil {
		t.Fatal(err)
	}
	if on, err := svc.Enabled(ctxA, a); err != nil || on {
		t.Fatalf("after turning off = %v, %v", on, err)
	}
}

func TestStartsFollowUps(t *testing.T) {
	d := testdb.Open(t)
	svc := assistant.NewService(assistant.NewSQLStore(d))
	a, b := seedOrg(t, d, "fu-a"), seedOrg(t, d, "fu-b")
	ctxA, ctxB := db.WithOrgScope(context.Background(), a), db.WithOrgScope(context.Background(), b)

	if on, err := svc.StartsFollowUps(ctxA, a); err != nil || on {
		t.Fatalf("absent row = %v, %v; want false", on, err)
	}
	if err := svc.SetStartsFollowUps(ctxA, a, true); err != nil {
		t.Fatal(err)
	}
	if on, err := svc.StartsFollowUps(ctxA, a); err != nil || !on {
		t.Fatalf("after setting = %v, %v; want true", on, err)
	}
	if on, err := svc.Enabled(ctxA, a); err != nil || on {
		t.Fatalf("assistant switch after setting follow-ups = %v, %v; want off", on, err)
	}
	if err := svc.SetEnabled(ctxA, a, true); err != nil {
		t.Fatal(err)
	}
	if on, err := svc.StartsFollowUps(ctxA, a); err != nil || !on {
		t.Fatalf("follow-ups after SetEnabled = %v, %v; want kept true", on, err)
	}
	if on, err := svc.StartsFollowUps(ctxB, b); err != nil || on {
		t.Fatalf("the other org = %v, %v; want false", on, err)
	}
	if on, err := svc.StartsFollowUps(ctxB, a); err != nil || on {
		t.Fatalf("org a read inside org b's scope = %v, %v; want false", on, err)
	}
}
