package accounts_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func seedUser(t *testing.T, d *db.DB, email string, hash *string) uuid.UUID {
	t.Helper()
	uid := uuid.Must(uuid.NewV7())
	if _, err := gen.New(d.Pool()).CreateUser(context.Background(), gen.CreateUserParams{
		ID: uid, Email: email, DisplayName: "U", PasswordHash: hash,
	}); err != nil {
		t.Fatal(err)
	}
	return uid
}

func sessionRevoked(t *testing.T, d *db.DB, id uuid.UUID) bool {
	t.Helper()
	var revoked bool
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL FROM sessions WHERE id = $1`, id).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	return revoked
}

func TestSQLStorePasswordRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	hash := "$argon2id$old"
	uid := seedUser(t, d, "store@example.test", &hash)
	expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	if _, err := d.Pool().Exec(ctx,
		`UPDATE users SET must_change_password = true, temp_password_expires_at = $2 WHERE id = $1`, uid, expires); err != nil {
		t.Fatal(err)
	}

	var st accounts.PasswordState
	if err := store.InTx(ctx, func(ctx context.Context) error {
		var err error
		st, err = store.LockPassword(ctx, uid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if st.Hash == nil || *st.Hash != hash || st.TempPasswordExpiresAt == nil || !st.TempPasswordExpiresAt.Equal(expires) {
		t.Fatalf("LockPassword = %+v, want hash %q and expiry %v", st, hash, expires)
	}

	if err := store.SetPassword(ctx, uid, "$argon2id$new", true); err != nil {
		t.Fatal(err)
	}
	var (
		gotHash    string
		mustChange bool
		tempUntil  *time.Time
	)
	if err := d.Pool().QueryRow(ctx,
		`SELECT password_hash, must_change_password, temp_password_expires_at FROM users WHERE id = $1`, uid).
		Scan(&gotHash, &mustChange, &tempUntil); err != nil {
		t.Fatal(err)
	}
	if gotHash != "$argon2id$new" || mustChange || tempUntil != nil {
		t.Fatalf("after SetPassword(clear): hash %q, must_change %v, temp_until %v", gotHash, mustChange, tempUntil)
	}

	t.Run("without clearing, the flags stay", func(t *testing.T) {
		if _, err := d.Pool().Exec(ctx,
			`UPDATE users SET must_change_password = true, temp_password_expires_at = $2 WHERE id = $1`, uid, expires); err != nil {
			t.Fatal(err)
		}
		if err := store.SetPassword(ctx, uid, "$argon2id$newer", false); err != nil {
			t.Fatal(err)
		}
		if err := d.Pool().QueryRow(ctx,
			`SELECT must_change_password, temp_password_expires_at FROM users WHERE id = $1`, uid).
			Scan(&mustChange, &tempUntil); err != nil {
			t.Fatal(err)
		}
		if !mustChange || tempUntil == nil {
			t.Fatalf("SetPassword(no clear) touched the flags: must_change %v, temp_until %v", mustChange, tempUntil)
		}
	})

	t.Run("unknown and deactivated accounts are not found", func(t *testing.T) {
		if err := store.SetPassword(ctx, uuid.Must(uuid.NewV7()), "$argon2id$x", true); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("SetPassword(unknown) err = %v, want ErrNotFound", err)
		}
		if _, err := store.LockPassword(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("LockPassword(unknown) err = %v, want ErrNotFound", err)
		}
		disabled := seedUser(t, d, "disabled@example.test", &hash)
		if _, err := d.Pool().Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, disabled); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LockPassword(ctx, disabled); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("LockPassword(deactivated) err = %v, want ErrNotFound", err)
		}
	})
}

func TestSQLStoreRevokeSessionsExcept(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	sessions := auth.NewSessions(gen.New(d.Pool()), time.Now)
	hash := "$argon2id$h"
	uid := seedUser(t, d, "revoke@example.test", &hash)
	other := seedUser(t, d, "bystander@example.test", &hash)

	keep, _, err := sessions.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	gone, _, err := sessions.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	bystander, _, err := sessions.Create(ctx, other, true)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.RevokeSessionsExcept(ctx, uid, keep); err != nil {
		t.Fatal(err)
	}
	if sessionRevoked(t, d, keep) {
		t.Fatal("the kept session was revoked")
	}
	if !sessionRevoked(t, d, gone) {
		t.Fatal("another session of the user survived")
	}
	if sessionRevoked(t, d, bystander) {
		t.Fatal("a session of another user was revoked")
	}
}

// TestSQLStoreInTxRollsBack: a failure after the writes inside InTx leaves
// neither the password nor the revocation behind.
func TestSQLStoreInTxRollsBack(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	sessions := auth.NewSessions(gen.New(d.Pool()), time.Now)
	hash := "$argon2id$before"
	uid := seedUser(t, d, "rollback@example.test", &hash)
	sid, _, err := sessions.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")
	err = store.InTx(ctx, func(ctx context.Context) error {
		if err := store.SetPassword(ctx, uid, "$argon2id$after", true); err != nil {
			return err
		}
		if err := store.RevokeSessionsExcept(ctx, uid, uuid.Nil); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx err = %v, want %v", err, boom)
	}
	var got string
	if err := d.Pool().QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, uid).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != hash {
		t.Fatalf("password_hash = %q after a rolled-back transaction, want %q", got, hash)
	}
	if sessionRevoked(t, d, sid) {
		t.Fatal("the session revocation survived a rolled-back transaction")
	}
}

// seedFactors gives uid a confirmed TOTP secret, three recovery codes (one
// of them spent) and a passkey.
func seedFactors(t *testing.T, d *db.DB, uid uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO totp_secrets (user_id, secret_sealed, confirmed_at) VALUES ($1, 'sealed', now())`,
		`INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1, 'code-1'), ($1, 'code-2')`,
		`INSERT INTO recovery_codes (user_id, code_hash, used_at) VALUES ($1, 'code-3', now())`,
		`INSERT INTO passkeys (id, user_id, name, credential_id, public_key, attestation)
		 VALUES (gen_random_uuid(), $1::uuid, 'key', convert_to('cred-' || $1::uuid::text, 'UTF8'), 'pk', 'none')`,
	} {
		if _, err := d.Pool().Exec(ctx, stmt, uid); err != nil {
			t.Fatal(err)
		}
	}
}

func countRows(t *testing.T, d *db.DB, table string, uid uuid.UUID) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM `+table+` WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestResetSecondFactorSQL: the reset removes the TOTP secret, the
// recovery codes and every passkey of the account and ends its sessions,
// in one transaction, and leaves every other account alone.
func TestResetSecondFactorSQL(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	sessions := auth.NewSessions(gen.New(d.Pool()), time.Now)
	hash := "$argon2id$h"
	uid := seedUser(t, d, "reset-factor@example.test", &hash)
	other := seedUser(t, d, "reset-bystander@example.test", &hash)
	seedFactors(t, d, uid)
	seedFactors(t, d, other)
	sid, _, err := sessions.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	otherSid, _, err := sessions.Create(ctx, other, true)
	if err != nil {
		t.Fatal(err)
	}

	svc := accounts.NewService(store, &fakeMailer{unconfigured: true}, "https://pulsix.example", time.Now, &cyclingReader{})
	if _, err := svc.ResetSecondFactor(ctx, true, uuid.Must(uuid.NewV7()), uid); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"totp_secrets", "recovery_codes", "passkeys"} {
		if n := countRows(t, d, table, uid); n != 0 {
			t.Errorf("%s: %d rows left for the user, want 0", table, n)
		}
	}
	if !sessionRevoked(t, d, sid) {
		t.Error("the user's session survived the reset")
	}
	if countRows(t, d, "totp_secrets", other) != 1 || countRows(t, d, "recovery_codes", other) != 3 || countRows(t, d, "passkeys", other) != 1 {
		t.Error("the reset touched another user's factors")
	}
	if sessionRevoked(t, d, otherSid) {
		t.Error("the reset ended another user's session")
	}
}

func TestSQLStoreAccountActions(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	sessions := auth.NewSessions(gen.New(d.Pool()), time.Now)
	hash := "$argon2id$h"
	uid := seedUser(t, d, "actions@example.test", &hash)
	pending := seedUser(t, d, "actions-pending@example.test", nil)
	if _, err := d.Pool().Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}

	t.Run("LockAccount", func(t *testing.T) {
		err := store.InTx(ctx, func(ctx context.Context) error {
			has, err := store.LockAccount(ctx, uid)
			if err != nil || !has {
				t.Errorf("LockAccount(deactivated with a password) = %v, %v; want true", has, err)
			}
			has, err = store.LockAccount(ctx, pending)
			if err != nil || has {
				t.Errorf("LockAccount(invite pending) = %v, %v; want false", has, err)
			}
			if _, err := store.LockAccount(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, accounts.ErrNotFound) {
				t.Errorf("LockAccount(unknown) err = %v, want ErrNotFound", err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("SetTemporaryPassword", func(t *testing.T) {
		expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
		if err := store.SetTemporaryPassword(ctx, uid, "$argon2id$temp", expires); err != nil {
			t.Fatal(err)
		}
		var (
			got        string
			mustChange bool
			until      *time.Time
		)
		if err := d.Pool().QueryRow(ctx,
			`SELECT password_hash, must_change_password, temp_password_expires_at FROM users WHERE id = $1`, uid).
			Scan(&got, &mustChange, &until); err != nil {
			t.Fatal(err)
		}
		if got != "$argon2id$temp" || !mustChange || until == nil || !until.Equal(expires) {
			t.Fatalf("after SetTemporaryPassword: hash %q, must change %v, until %v", got, mustChange, until)
		}
		if err := store.SetTemporaryPassword(ctx, uuid.Must(uuid.NewV7()), "$argon2id$x", expires); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("SetTemporaryPassword(unknown) err = %v, want ErrNotFound", err)
		}
	})

	t.Run("RevokeAllSessions", func(t *testing.T) {
		a, _, err := sessions.Create(ctx, pending, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.RevokeAllSessions(ctx, pending); err != nil {
			t.Fatal(err)
		}
		if !sessionRevoked(t, d, a) {
			t.Fatal("RevokeAllSessions left a session")
		}
	})

	t.Run("Reactivate", func(t *testing.T) {
		if err := store.Reactivate(ctx, uid); err != nil {
			t.Fatal(err)
		}
		var disabled bool
		if err := d.Pool().QueryRow(ctx, `SELECT disabled_at IS NOT NULL FROM users WHERE id = $1`, uid).Scan(&disabled); err != nil {
			t.Fatal(err)
		}
		if disabled {
			t.Fatal("Reactivate left disabled_at set")
		}
		if err := store.Reactivate(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("Reactivate(unknown) err = %v, want ErrNotFound", err)
		}
	})

	t.Run("NoticeRecipient", func(t *testing.T) {
		r, err := store.NoticeRecipient(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if r.Email != "actions@example.test" || r.Locale != nil || r.DefaultLocale != "" {
			t.Fatalf("NoticeRecipient = %+v, want the address and no languages", r)
		}
		if _, err := d.Pool().Exec(ctx, `UPDATE users SET locale = 'de' WHERE id = $1`, uid); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool().Exec(ctx, `INSERT INTO instance_settings (key, value) VALUES ('default_locale', '"en"')`); err != nil {
			t.Fatal(err)
		}
		r, err = store.NoticeRecipient(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if r.Locale == nil || *r.Locale != "de" || r.DefaultLocale != "en" {
			t.Fatalf("NoticeRecipient = %+v, want locale de and default en", r)
		}
		if _, err := store.NoticeRecipient(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("NoticeRecipient(unknown) err = %v, want ErrNotFound", err)
		}
	})
}

func TestSQLStoreAccountReads(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	sessions := auth.NewSessions(gen.New(d.Pool()), time.Now)
	hash := "$argon2id$h"
	alice := seedUser(t, d, "Alice@example.test", &hash)
	bob := seedUser(t, d, "bob@example.test", &hash)
	carol := seedUser(t, d, "carol@example.test", nil)
	admin := seedUser(t, d, "admin@example.test", &hash)
	if _, err := d.Pool().Exec(ctx, `UPDATE users SET display_name = 'Robert Builder', instance_admin = true, must_change_password = true, last_sign_in_at = now() WHERE id = $1`, bob); err != nil {
		t.Fatal(err)
	}
	seedFactors(t, d, alice)

	orgA, orgB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, slug, name) VALUES ($1, 'zeta', 'Zeta'), ($2, 'acme', 'Acme')`, []any{orgA, orgB}},
		{`INSERT INTO org_memberships (org_id, user_id, role) VALUES ($1, $3, 'owner'), ($2, $3, 'viewer'), ($1, $4, 'admin')`, []any{orgA, orgB, alice, admin}},
	} {
		if _, err := d.Pool().Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("ListAccounts", func(t *testing.T) {
		all, err := store.ListAccounts(ctx, "", "", 10)
		if err != nil {
			t.Fatal(err)
		}
		var emails []string
		for _, r := range all {
			emails = append(emails, r.Email)
		}
		if got := strings.Join(emails, ","); got != "admin@example.test,Alice@example.test,bob@example.test,carol@example.test" {
			t.Fatalf("order %s, want case-insensitive by e-mail", got)
		}
		a := all[1]
		if a.ID != alice || !a.HasPassword || !a.HasSecondFactor || a.OrgCount != 2 || a.InstanceAdmin || a.LastSignInAt != nil {
			t.Fatalf("alice = %+v", a)
		}
		b := all[2]
		if !b.InstanceAdmin || !b.MustChangePassword || b.HasSecondFactor || b.OrgCount != 0 || b.LastSignInAt == nil || b.DisplayName != "Robert Builder" {
			t.Fatalf("bob = %+v", b)
		}
		if all[3].HasPassword {
			t.Fatalf("carol = %+v, want no password", all[3])
		}

		page, err := store.ListAccounts(ctx, "", "alice@EXAMPLE.test", 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 2 || page[0].ID != bob || page[1].ID != carol {
			t.Fatalf("page after alice = %+v, want bob and carol", page)
		}

		for query, want := range map[string]uuid.UUID{"ROBERT": bob, "alice@": alice, "car": carol, "100%": uuid.Nil, "_": uuid.Nil} {
			found, err := store.ListAccounts(ctx, query, "", 10)
			if err != nil {
				t.Fatal(err)
			}
			if want == uuid.Nil {
				if len(found) != 0 {
					t.Errorf("query %q matched %+v, want nothing (no wildcards)", query, found)
				}
				continue
			}
			if len(found) != 1 || found[0].ID != want {
				t.Errorf("query %q matched %+v, want only %s", query, found, want)
			}
		}
	})

	t.Run("GetAccount", func(t *testing.T) {
		if _, _, err := sessions.Create(ctx, alice, true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := sessions.Create(ctx, alice, false); err != nil { // pending: not counted
			t.Fatal(err)
		}
		revoked, _, err := sessions.Create(ctx, alice, true)
		if err != nil {
			t.Fatal(err)
		}
		expired, _, err := sessions.Create(ctx, alice, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool().Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1`, revoked); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool().Exec(ctx, `UPDATE sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, expired); err != nil {
			t.Fatal(err)
		}

		got, err := store.GetAccount(ctx, alice)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != alice || got.Email != "Alice@example.test" || got.OrgCount != 2 || got.CreatedAt.IsZero() ||
			got.Factors != (accounts.Factors{TOTP: true, Passkeys: 1, RecoveryCodesLeft: 2}) || got.ActiveSessions != 1 ||
			!got.HasSecondFactor || got.TempPasswordExpiresAt != nil || got.DisabledAt != nil {
			t.Fatalf("GetAccount(alice) = %+v", got)
		}
		if _, err := store.GetAccount(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, accounts.ErrNotFound) {
			t.Fatalf("GetAccount(unknown) err = %v, want ErrNotFound", err)
		}
	})

	t.Run("an unconfirmed TOTP secret is no second factor", func(t *testing.T) {
		if _, err := d.Pool().Exec(ctx, `INSERT INTO totp_secrets (user_id, secret_sealed) VALUES ($1, 'sealed')`, bob); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetAccount(ctx, bob)
		if err != nil {
			t.Fatal(err)
		}
		if got.HasSecondFactor || got.Factors.TOTP {
			t.Fatalf("GetAccount(bob) = %+v, want no second factor", got)
		}
	})

	t.Run("ListMemberships", func(t *testing.T) {
		got, err := store.ListMemberships(ctx, alice, admin)
		if err != nil {
			t.Fatal(err)
		}
		want := []accounts.Membership{
			{OrgID: orgB, Slug: "acme", Name: "Acme", Role: "viewer", ActorIsMember: false},
			{OrgID: orgA, Slug: "zeta", Name: "Zeta", Role: "owner", ActorIsMember: true},
		}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("ListMemberships = %+v, want %+v", got, want)
		}
	})
}
