package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestTOTPEnrolVerifyReplay(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "t@example.test", DisplayName: "T"}); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	tp := auth.NewTOTP(q, crypto.NewBox(key), "Pulsix Test", time.Now)

	secret, url, err := tp.BeginEnrol(ctx, uid, "t@example.test")
	if err != nil || url == "" {
		t.Fatalf("begin: %v", err)
	}
	if on, _ := tp.Enabled(ctx, uid); on {
		t.Fatal("enabled before confirm")
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	recovery, err := tp.ConfirmEnrol(ctx, uid, code)
	if err != nil || len(recovery) != 8 {
		t.Fatalf("confirm: %v, %d codes", err, len(recovery))
	}
	if on, _ := tp.Enabled(ctx, uid); !on {
		t.Fatal("not enabled after confirm")
	}
	// the confirmation code is consumed; a fresh code for the same step must be rejected (replay)
	if ok, _ := tp.Verify(ctx, uid, code); ok {
		t.Fatal("replayed code accepted")
	}
	if ok, _ := tp.Verify(ctx, uid, "000000"); ok {
		t.Fatal("wrong code accepted")
	}
	if ok, _ := tp.VerifyRecovery(ctx, uid, recovery[0]); !ok {
		t.Fatal("recovery code rejected")
	}
	if ok, _ := tp.VerifyRecovery(ctx, uid, recovery[0]); ok {
		t.Fatal("recovery code reused")
	}
}
