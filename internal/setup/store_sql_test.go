package setup_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/setup"
	"github.com/noixdev/pulsix/internal/tenancy"
)

func newSQLService(t *testing.T) (*setup.Service, *gen.Queries) {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	sessions := auth.NewSessions(q, time.Now)
	store := setup.NewSQLStore(d)
	return setup.NewService(store, sessions, crypto.NewBox([32]byte{}), time.Now, testToken), q
}

func TestSQLStoreCompleteCreatesAdminOrgAndMembership(t *testing.T) {
	svc, q := newSQLService(t)
	ctx := context.Background()

	res, err := svc.Complete(ctx, setup.Input{SetupToken: testToken,
		AdminEmail: "root@example.test", AdminPassword: "correct horse battery", AdminName: "Root",
		OrgSlug: "acme", OrgName: "Acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cookie == "" {
		t.Fatal("Complete did not return a session cookie")
	}

	u, err := q.GetUserByID(ctx, res.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if !u.InstanceAdmin || u.PasswordHash == nil {
		t.Fatalf("stored admin = %+v", u)
	}

	m, err := q.GetMembership(ctx, gen.GetMembershipParams{OrgID: res.OrgID, UserID: res.UserID})
	if err != nil {
		t.Fatal(err)
	}
	if tenancy.Role(m.Role) != tenancy.RoleOwner {
		t.Fatalf("membership role = %s, want owner", m.Role)
	}

	if _, err := svc.Complete(ctx, setup.Input{SetupToken: testToken,
		AdminEmail: "again@example.test", AdminPassword: "correct horse battery", AdminName: "Again",
		OrgSlug: "again", OrgName: "Again",
	}); !errors.Is(err, setup.ErrAlreadyCompleted) {
		t.Fatalf("second Complete: %v, want ErrAlreadyCompleted", err)
	}
}

// TestSQLStoreConcurrentCompleteOnlyOneSucceeds is the property the whole
// advisory-lock design in SQLStore.InTx exists for: two callers racing to
// complete first-run setup at the same instant must never both succeed —
// exactly one instance administrator, and exactly one first organization,
// may ever be created, however the two requests interleave.
func TestSQLStoreConcurrentCompleteOnlyOneSucceeds(t *testing.T) {
	svc, q := newSQLService(t)
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := svc.Complete(ctx, setup.Input{SetupToken: testToken,
				AdminEmail: fmt.Sprintf("racer-%d@example.test", i), AdminPassword: "correct horse battery",
				AdminName: fmt.Sprintf("Racer %d", i), OrgSlug: fmt.Sprintf("racer-%d", i), OrgName: fmt.Sprintf("Racer %d", i),
			})
			results[i] = err
		}(i)
	}
	wg.Wait()

	successes, conflicts, other, winner := 0, 0, 0, -1
	for i, err := range results {
		switch {
		case err == nil:
			successes++
			winner = i
		case errors.Is(err, setup.ErrAlreadyCompleted):
			conflicts++
		default:
			other++
			t.Errorf("unexpected error from a concurrent Complete: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1 (conflicts=%d other=%d)", successes, conflicts, other)
	}
	if conflicts != n-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, n-1)
	}

	winnerUser, err := q.GetUserByEmail(ctx, fmt.Sprintf("racer-%d@example.test", winner))
	if err != nil {
		t.Fatal(err)
	}
	orgs, err := q.ListMembershipsForUser(ctx, winnerUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != 1 {
		t.Fatalf("winning admin belongs to %d orgs, want exactly 1", len(orgs))
	}
}

func TestSQLStoreAdminMFAEnrolledReflectsConfirmedTOTP(t *testing.T) {
	svc, q := newSQLService(t)
	ctx := context.Background()

	res, err := svc.Complete(ctx, setup.Input{SetupToken: testToken,
		AdminEmail: "root@example.test", AdminPassword: "correct horse battery", AdminName: "Root",
		OrgSlug: "acme", OrgName: "Acme",
	})
	if err != nil {
		t.Fatal(err)
	}

	before, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.AdminMFAEnrolled {
		t.Fatal("AdminMFAEnrolled reported true before the admin has any second factor")
	}

	if err := q.UpsertTOTPSecret(ctx, gen.UpsertTOTPSecretParams{UserID: res.UserID, SecretSealed: []byte("sealed")}); err != nil {
		t.Fatal(err)
	}
	// ConfirmTOTP promotes the pending secret and reports how many rows it
	// touched (backlog 1.27), so the seeded row above is confirmed only if
	// it actually had one in flight — which UpsertTOTPSecret gives it.
	if n, err := q.ConfirmTOTP(ctx, res.UserID); err != nil || n != 1 {
		t.Fatalf("ConfirmTOTP = %d rows, %v", n, err)
	}

	after, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !after.AdminMFAEnrolled {
		t.Fatal("AdminMFAEnrolled reported false after the admin confirmed a TOTP secret")
	}
}

func TestSQLStoreLoadSettingNotFound(t *testing.T) {
	svc, _ := newSQLService(t)
	if _, err := svc.LoadSMTP(context.Background()); !errors.Is(err, setup.ErrNotFound) {
		t.Fatalf("LoadSMTP before any save: %v, want ErrNotFound", err)
	}
}

func TestSQLStoreSMTPRoundTrip(t *testing.T) {
	svc, _ := newSQLService(t)
	ctx := context.Background()
	in := mail.SMTPSettings{Host: "smtp.example.test", Port: 587, Username: "pulsix", Password: "s3cret", From: "pulsix@example.test", StartTLS: true}
	if err := svc.SaveSMTP(ctx, in); err != nil {
		t.Fatal(err)
	}
	out, err := svc.LoadSMTP(ctx)
	if err != nil || out != in {
		t.Fatalf("LoadSMTP = %+v, %v; want %+v, nil", out, err, in)
	}
}

func TestSQLStoreDefaultLocaleRoundTrip(t *testing.T) {
	svc, _ := newSQLService(t)
	ctx := context.Background()
	if got, err := svc.DefaultLocale(ctx); err != nil || got != "en" {
		t.Fatalf("DefaultLocale before any save = %q, %v; want en, nil", got, err)
	}
	if err := svc.SetDefaultLocale(ctx, "de"); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.DefaultLocale(ctx); err != nil || got != "de" {
		t.Fatalf("DefaultLocale = %q, %v; want de, nil", got, err)
	}
	if err := svc.SetDefaultLocale(ctx, "en"); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.DefaultLocale(ctx); err != nil || got != "en" {
		t.Fatalf("DefaultLocale after overwrite = %q, %v; want en, nil", got, err)
	}
}
