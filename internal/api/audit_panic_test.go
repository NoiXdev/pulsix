package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
)

// spyAuditStore records every Insert call it receives. It never needs a
// working List/ListForActor: TestPanicInHandlerRecordsNoAuditEvent only
// drives the mutating route, never reads a feed back.
type spyAuditStore struct {
	inserted []audit.Event
}

func (s *spyAuditStore) Insert(_ context.Context, e audit.Event) error {
	s.inserted = append(s.inserted, e)
	return nil
}

func (s *spyAuditStore) List(context.Context, uuid.UUID, audit.Filter, string, int) ([]audit.Event, string, error) {
	return nil, "", nil
}

func (s *spyAuditStore) ListForActor(context.Context, uuid.UUID, audit.Filter, string, int) ([]audit.Event, string, error) {
	return nil, "", nil
}

// TestPanicInHandlerRecordsNoAuditEvent drives a real panic through
// auditMiddleware, wired the same way registerRoutes/New wire it in
// production (chi's middleware.Recoverer outermost, auditMiddleware
// innermost around the operation). auditMiddleware's own doc comment
// currently asserts "a panicking handler never reaches the code after
// next() that calls Record" purely by reasoning about Go's unwind
// semantics; this test proves it against a real HTTP round trip instead:
// the panicking handler's response is chi's generic 500 (Recoverer caught
// it above auditMiddleware, which has no recover of its own), and no event
// was ever inserted — the request fails closed with no audit trail of an
// action that, from the caller's point of view, never completed either.
func TestPanicInHandlerRecordsNoAuditEvent(t *testing.T) {
	store := &spyAuditStore{}
	deps := Deps{Audit: audit.NewRecorder(store, time.Now)}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	hapi := humachi.New(r, huma.DefaultConfig("Pulsix API (panic test)", "0.0.0"))
	hapi.UseMiddleware(auditMiddleware(deps))
	huma.Post(hapi, "/panic", func(ctx context.Context, _ *struct{}) (*okOutput, error) {
		// Scope is set before panicking, exactly like a real handler would
		// after requireOrg succeeds, so a passing test can't be explained
		// by carrier.haveScope being false rather than by the panic
		// actually skipping the post-next() Record call.
		setAuditScope(ctx, nil, uuid.Must(uuid.NewV7()))
		panic("simulated handler panic")
	}, withAudit("panicOp", "thing"))

	ts := httptest.NewServer(r)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/panic", "application/json", http.NoBody)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (chi's Recoverer)", resp.StatusCode, http.StatusInternalServerError)
	}
	if len(store.inserted) != 0 {
		t.Fatalf("inserted = %+v, want no audit event recorded for a panicking handler", store.inserted)
	}
}
