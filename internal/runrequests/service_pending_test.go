package runrequests_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

func (w *wiring) createFor(token *uuid.UUID) (runrequests.Request, error) {
	return w.svc.Create(context.Background(), w.org, runrequests.CreateInput{
		Kind: runrequests.KindRun, TemplateID: uuid.Must(uuid.NewV7()), Reason: "set up web",
		RequestedByUser: uuid.Must(uuid.NewV7()), RequestedByToken: token,
	})
}

// TestCreateCapsPendingRequestsPerToken: a token holds at most
// MaxPendingPerToken pending, unexpired requests; a decision or an expiry
// frees a slot, other tokens have their own, and a request without a token
// is not capped.
func TestCreateCapsPendingRequestsPerToken(t *testing.T) {
	w := newWiring()
	ctx := context.Background()
	tok, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	var first runrequests.Request
	for i := range runrequests.MaxPendingPerToken {
		r, err := w.createFor(&tok)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if i == 0 {
			first = r
		}
	}
	if runrequests.MaxPendingPerToken != 20 {
		t.Fatalf("MaxPendingPerToken = %d, want 20", runrequests.MaxPendingPerToken)
	}
	if _, err := w.createFor(&tok); !errors.Is(err, runrequests.ErrTooManyPending) {
		t.Fatalf("request 21 = %v, want ErrTooManyPending", err)
	}
	if _, err := w.createFor(&other); err != nil {
		t.Fatalf("another token = %v", err)
	}
	for range runrequests.MaxPendingPerToken + 1 {
		if _, err := w.createFor(nil); err != nil {
			t.Fatalf("without a token = %v", err)
		}
	}
	if _, err := w.svc.Reject(ctx, w.org, first.ID, uuid.Must(uuid.NewV7())); err != nil {
		t.Fatal(err)
	}
	if _, err := w.createFor(&tok); err != nil {
		t.Fatalf("after a rejection = %v", err)
	}
	if _, err := w.createFor(&tok); !errors.Is(err, runrequests.ErrTooManyPending) {
		t.Fatalf("full again = %v, want ErrTooManyPending", err)
	}
	w.now = w.now.Add(runrequests.TTL)
	if _, err := w.createFor(&tok); err != nil {
		t.Fatalf("after the others expired = %v", err)
	}
}

// TestPendingCount counts the organization's pending, unexpired requests.
func TestPendingCount(t *testing.T) {
	w := newWiring()
	ctx := context.Background()
	old := w.create(t)
	w.now = w.now.Add(30 * time.Minute)
	w.create(t)
	decided := w.create(t)
	if _, err := w.svc.Reject(ctx, w.org, decided.ID, uuid.Must(uuid.NewV7())); err != nil {
		t.Fatal(err)
	}
	if n, err := w.svc.PendingCount(ctx, w.org); err != nil || n != 2 {
		t.Fatalf("count = %d %v, want 2", n, err)
	}
	w.now = old.ExpiresAt
	if n, err := w.svc.PendingCount(ctx, w.org); err != nil || n != 1 {
		t.Fatalf("count after one expired = %d %v, want 1", n, err)
	}
	if n, err := w.svc.PendingCount(ctx, uuid.Must(uuid.NewV7())); err != nil || n != 0 {
		t.Fatalf("another organization = %d %v, want 0", n, err)
	}
}

// TestRequestsCarryTheirTokensState: every read of a request names the
// token that made it and whether that token can still ask (backlog 2.120):
// revoked, expired (on the service's clock) or deleted reads as a hint for
// the approver, never as a block.
func TestRequestsCarryTheirTokensState(t *testing.T) {
	w := newWiring()
	ctx := context.Background()
	soon, later := w.now.Add(time.Minute), w.now.Add(48*time.Hour)
	ids := map[runrequests.TokenState]uuid.UUID{}
	for _, st := range []runrequests.TokenState{runrequests.TokenActive, runrequests.TokenRevoked, runrequests.TokenExpired, runrequests.TokenDeleted} {
		ids[st] = uuid.Must(uuid.NewV7())
	}
	w.store.tokens[ids[runrequests.TokenActive]] = runrequests.TokenRecord{Name: "claude-active", ExpiresAt: &later}
	w.store.tokens[ids[runrequests.TokenRevoked]] = runrequests.TokenRecord{Name: "claude-revoked", Revoked: true}
	w.store.tokens[ids[runrequests.TokenExpired]] = runrequests.TokenRecord{Name: "claude-expired", ExpiresAt: &soon}
	byToken := map[runrequests.TokenState]runrequests.Request{}
	for st, tok := range ids {
		r, err := w.createFor(&tok)
		if err != nil {
			t.Fatal(err)
		}
		byToken[st] = r
	}
	untokened, err := w.createFor(nil)
	if err != nil {
		t.Fatal(err)
	}
	w.now = soon // the expiring token expires on the service's clock
	want := map[runrequests.TokenState]string{
		runrequests.TokenActive: "claude-active", runrequests.TokenRevoked: "claude-revoked",
		runrequests.TokenExpired: "claude-expired", runrequests.TokenDeleted: "",
	}
	for st, r := range byToken {
		got, _, _, err := w.svc.Get(ctx, w.org, r.ID)
		if err != nil || got.TokenState != st || got.TokenName != want[st] {
			t.Fatalf("Get %s = %q %q %v", st, got.TokenName, got.TokenState, err)
		}
	}
	list, err := w.svc.List(ctx, w.org, nil)
	if err != nil || len(list) != 5 {
		t.Fatalf("List = %d %v", len(list), err)
	}
	for _, r := range list {
		if r.ID == untokened.ID {
			if r.TokenState != "" || r.TokenName != "" {
				t.Fatalf("a request without a token = %q %q", r.TokenName, r.TokenState)
			}
			continue
		}
		var st runrequests.TokenState
		for s, tok := range ids {
			if *r.RequestedByToken == tok {
				st = s
			}
		}
		if r.TokenState != st || r.TokenName != want[st] {
			t.Fatalf("List %s = %q %q", st, r.TokenName, r.TokenState)
		}
	}
	rejected, err := w.svc.Reject(ctx, w.org, byToken[runrequests.TokenRevoked].ID, uuid.Must(uuid.NewV7()))
	if err != nil || rejected.TokenState != runrequests.TokenRevoked || rejected.TokenName != "claude-revoked" {
		t.Fatalf("Reject = %q %q %v", rejected.TokenName, rejected.TokenState, err)
	}
	r := byToken[runrequests.TokenExpired]
	_, _, fp, _ := w.svc.Get(ctx, w.org, r.ID)
	approved, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil)
	if err != nil || approved.Request.TokenState != runrequests.TokenExpired {
		t.Fatalf("Approve = %q %v", approved.Request.TokenState, err)
	}
}

// TestAFailedTokenReadNeverFailsAWrite: the token state is a hint, so once
// Create or Reject has written, a failing token read leaves the request
// without its token fields instead of failing an outcome that already
// happened. A plain read still fails.
func TestAFailedTokenReadNeverFailsAWrite(t *testing.T) {
	w := newWiring()
	ctx := context.Background()
	tok := uuid.Must(uuid.NewV7())
	w.store.tokens[tok] = runrequests.TokenRecord{Name: "claude"}
	w.store.tokensErr = errors.New("token table unreachable")
	r, err := w.createFor(&tok)
	if err != nil || r.TokenName != "" || r.TokenState != "" {
		t.Fatalf("Create = %q %q %v, want the request without its token fields", r.TokenName, r.TokenState, err)
	}
	if _, ok := w.store.rows[r.ID]; !ok {
		t.Fatal("the request was not stored")
	}
	rejected, err := w.svc.Reject(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()))
	if err != nil || rejected.Status != runrequests.StatusRejected || rejected.TokenState != "" {
		t.Fatalf("Reject = %+v %v, want rejected without its token fields", rejected, err)
	}
	if _, _, _, err := w.svc.Get(ctx, w.org, r.ID); !errors.Is(err, w.store.tokensErr) {
		t.Fatalf("Get = %v, want the token read's error", err)
	}
	if _, err := w.svc.List(ctx, w.org, nil); !errors.Is(err, w.store.tokensErr) {
		t.Fatalf("List = %v, want the token read's error", err)
	}
}
