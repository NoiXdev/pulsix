package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// failOnQuery wraps a real gen.DBTX (a live pool) and forces one specific
// query to fail, while every other query is delegated unchanged. sqlc
// embeds "-- name: <Query> :<type>" as the first line of each generated
// query constant, so matching on that name is a stable, query-specific
// hook — it lets these tests drive a genuine DB write failure inside a
// single service call (e.g. the third of ConfirmEnrol's several
// sequential writes) without a hand-rolled full mock of pgx's Rows/Row
// types for every query the service happens to use.
type failOnQuery struct {
	gen.DBTX
	name string
	err  error
}

func (f failOnQuery) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "-- name: "+f.name+" ") {
		return pgconn.CommandTag{}, f.err
	}
	return f.DBTX.Exec(ctx, sql, args...)
}

// totpFor builds a TOTP service over db using a fixed key, so several
// service instances (e.g. one real, one wrapped in failOnQuery) can share
// state through the same underlying database.
func totpFor(db gen.DBTX) *auth.TOTP {
	var key [32]byte
	return auth.NewTOTP(gen.New(db), crypto.NewBox(key), "Pulsix Test", time.Now)
}

func TestTOTPBeginEnrolPropagatesUpsertError(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "t@example.test", DisplayName: "T"}); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("upsert failed")
	tp := totpFor(failOnQuery{DBTX: d.Pool(), name: "UpsertTOTPSecret", err: sentinel})
	if _, _, err := tp.BeginEnrol(ctx, uid, "t@example.test"); !errors.Is(err, sentinel) {
		t.Fatalf("BeginEnrol error = %v, want %v", err, sentinel)
	}
}

// TestTOTPConfirmEnrolPropagatesWriteErrors covers ConfirmEnrol's three
// sequential writes after the code itself validates (ConfirmTOTP,
// ReplaceRecoveryCodes, and the InsertRecoveryCode loop): a failure in any
// one of them must surface as an error, not be silently swallowed.
func TestTOTPConfirmEnrolPropagatesWriteErrors(t *testing.T) {
	for _, queryName := range []string{"ConfirmTOTP", "ReplaceRecoveryCodes", "InsertRecoveryCode"} {
		t.Run(queryName, func(t *testing.T) {
			d := testdb.Open(t)
			ctx := context.Background()
			q := gen.New(d.Pool())
			uid := uuid.Must(uuid.NewV7())
			if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "t@example.test", DisplayName: "T"}); err != nil {
				t.Fatal(err)
			}
			secret, _, err := totpFor(d.Pool()).BeginEnrol(ctx, uid, "t@example.test")
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			code, _ := totp.GenerateCode(secret, time.Now())

			sentinel := errors.New(queryName + " failed")
			tp := totpFor(failOnQuery{DBTX: d.Pool(), name: queryName, err: sentinel})
			if _, err := tp.ConfirmEnrol(ctx, uid, code); !errors.Is(err, sentinel) {
				t.Fatalf("ConfirmEnrol error = %v, want %v", err, sentinel)
			}
		})
	}
}

// TestTOTPDisablePropagatesWriteErrors covers Disable's two sequential
// writes: a failure in either must surface, not be dropped in favour of
// the other succeeding.
func TestTOTPDisablePropagatesWriteErrors(t *testing.T) {
	for _, queryName := range []string{"ReplaceRecoveryCodes", "DeleteTOTP"} {
		t.Run(queryName, func(t *testing.T) {
			d := testdb.Open(t)
			ctx := context.Background()
			q := gen.New(d.Pool())
			uid := uuid.Must(uuid.NewV7())
			if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "t@example.test", DisplayName: "T"}); err != nil {
				t.Fatal(err)
			}
			real := totpFor(d.Pool())
			secret, _, err := real.BeginEnrol(ctx, uid, "t@example.test")
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			code, _ := totp.GenerateCode(secret, time.Now())
			if _, err := real.ConfirmEnrol(ctx, uid, code); err != nil {
				t.Fatalf("confirm: %v", err)
			}

			sentinel := errors.New(queryName + " failed")
			tp := totpFor(failOnQuery{DBTX: d.Pool(), name: queryName, err: sentinel})
			if err := tp.Disable(ctx, uid); !errors.Is(err, sentinel) {
				t.Fatalf("Disable error = %v, want %v", err, sentinel)
			}
		})
	}
}

// TestTOTPVerifyPropagatesSetTOTPLastStepError covers verifyAndConsume's
// final write, the conditional UPDATE that claims the matched step.
func TestTOTPVerifyPropagatesSetTOTPLastStepError(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "t@example.test", DisplayName: "T"}); err != nil {
		t.Fatal(err)
	}
	real := totpFor(d.Pool())
	secret, _, err := real.BeginEnrol(ctx, uid, "t@example.test")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if _, err := real.ConfirmEnrol(ctx, uid, code); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	sentinel := errors.New("set last step failed")
	tp := totpFor(failOnQuery{DBTX: d.Pool(), name: "SetTOTPLastStep", err: sentinel})
	nextCode, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	if ok, err := tp.Verify(ctx, uid, nextCode); ok || !errors.Is(err, sentinel) {
		t.Fatalf("Verify = (%v, %v), want (false, %v)", ok, err, sentinel)
	}
}

// TestTOTPVerifyPropagatesOpenError asserts that a secret which fails to
// decrypt (corrupted at rest, or sealed under a different master key) is
// reported as an error rather than silently treated as "no such secret".
func TestTOTPVerifyPropagatesOpenError(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "t@example.test", DisplayName: "T"}); err != nil {
		t.Fatal(err)
	}
	// Too short to be a valid AES-GCM nonce+ciphertext: Box.Open rejects it.
	if err := q.UpsertTOTPSecret(ctx, gen.UpsertTOTPSecretParams{UserID: uid, SecretSealed: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := q.ConfirmTOTP(ctx, uid); err != nil {
		t.Fatal(err)
	}

	tp := totpFor(d.Pool())
	if ok, err := tp.Verify(ctx, uid, "000000"); ok || err == nil {
		t.Fatalf("Verify on an undecryptable secret = (%v, %v), want (false, a decrypt error)", ok, err)
	}
}
