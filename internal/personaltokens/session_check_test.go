package personaltokens_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/personaltokens"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// sessionCheck is a personaltokens.SessionCheck that records its calls and
// answers err.
type sessionCheck struct {
	err   error
	calls []uuid.UUID
	inTx  []int
	st    *fakeStore
}

func (c *sessionCheck) RequireComplete(_ context.Context, _, sessionID uuid.UUID) error {
	c.calls = append(c.calls, sessionID)
	c.inTx = append(c.inTx, c.st.inTx)
	return c.err
}

// A create from a session re-checks that session inside its transaction,
// after the owner lock (CountTokens), and stores nothing when it is refused:
// a session superseded by a factor enrolled meanwhile mints no token.
func TestCreateRechecksTheCreatingSession(t *testing.T) {
	refused := errors.New("session no longer complete")
	for _, tc := range []struct {
		name string
		err  error
	}{{"complete", nil}, {"refused", refused}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			check := &sessionCheck{err: tc.err, st: w.st}
			svc := personaltokens.NewService(w.st, w.members, func() time.Time { return fixedNow }, personaltokens.WithSessionCheck(check))
			sid := uuid.Must(uuid.NewV7())
			in := api(g(w.a, tenancy.RoleViewer))
			in.SessionID = sid
			_, _, err := svc.Create(context.Background(), w.user, in)
			if !errors.Is(err, tc.err) {
				t.Fatalf("Create: %v, want %v", err, tc.err)
			}
			if len(check.calls) != 1 || check.calls[0] != sid || check.inTx[0] == 0 {
				t.Fatalf("checks = %v (in a transaction: %v), want one for %s inside it", check.calls, check.inTx, sid)
			}
			if want := map[bool]int{true: 0, false: 1}[tc.err != nil]; len(w.st.tokens) != want {
				t.Fatalf("tokens stored = %d, want %d", len(w.st.tokens), want)
			}
		})
	}
}

// A create that names a session but has no check to run is refused rather
// than skipping the check.
func TestCreateWithASessionNeedsACheck(t *testing.T) {
	w := newWorld()
	in := api(g(w.a, tenancy.RoleViewer))
	in.SessionID = uuid.Must(uuid.NewV7())
	if _, _, err := w.svc.Create(context.Background(), w.user, in); err == nil {
		t.Fatal("Create with a session and no check succeeded")
	}
	if len(w.st.tokens) != 0 {
		t.Fatalf("tokens stored = %d, want none", len(w.st.tokens))
	}
}
