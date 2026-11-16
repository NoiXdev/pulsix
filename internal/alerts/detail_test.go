// Apply's detail capture (backlog 3.13b, spec "4. Alert detail"): an
// opening or a severity change asks the DetailSource why, inside a
// savepoint of the evaluator's transaction; every other action asks
// nothing. These run against fakeStore and fakeTx — the savepoint's real
// behaviour on Postgres is detail_sql_test.go's.
package alerts_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// fakeTx is the slice of pgx.Tx Apply's capture touches: Begin (the
// savepoint), Commit and Rollback. Anything else panics through the nil
// embedded interface, which is the point — the capture must not issue
// statements of its own.
type fakeTx struct {
	pgx.Tx
	parent              *fakeTx
	begun               int
	committed, rolledBk int
	beginErr            error
}

func (t *fakeTx) Begin(context.Context) (pgx.Tx, error) {
	if t.beginErr != nil {
		return nil, t.beginErr
	}
	t.begun++
	return &fakeTx{parent: t}, nil
}

func (t *fakeTx) Commit(context.Context) error {
	t.parent.committed++
	return nil
}

func (t *fakeTx) Rollback(context.Context) error {
	t.parent.rolledBk++
	return nil
}

// detailCall is one Detail call, with whether it ran on a savepoint of the
// outer transaction rather than the transaction itself.
type detailCall struct {
	sub         alerts.Subject
	onSavepoint bool
}

// fakeDetails answers every subject with the same detail, or err.
type fakeDetails struct {
	outer  *fakeTx
	detail string
	err    error
	calls  []detailCall
}

func (f *fakeDetails) Detail(_ context.Context, tx pgx.Tx, orgID uuid.UUID, sub alerts.Subject) (string, error) {
	sp, _ := tx.(*fakeTx)
	f.calls = append(f.calls, detailCall{sub: sub, onSavepoint: sp != nil && sp.parent == f.outer && sp != f.outer})
	if orgID != input().OrgID {
		return "", errors.New("wrong organization")
	}
	return f.detail, f.err
}

func detailService(store *fakeStore, src alerts.DetailSource, log *bytes.Buffer) *alerts.Service {
	svc := alerts.NewService(store, clockAt(t0))
	svc.SetDetailSource(src, slog.New(slog.NewTextHandler(log, nil)))
	return svc
}

func TestApplyCapturesTheDetailOfAnOpening(t *testing.T) {
	store := newFakeStore()
	tx := &fakeTx{}
	src := &fakeDetails{outer: tx, detail: "timeout"}
	svc := detailService(store, src, &bytes.Buffer{})

	sub := tupleSubject("web", "", "http.up")
	if err := svc.Apply(ctx, tx, input(stateAt(sub, rules.StateCrit, t0))); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	acts := store.executed[0]
	if len(acts) != 1 || acts[0].Kind != alerts.ActionOpen || acts[0].Detail != "timeout" {
		t.Fatalf("executed = %+v, want one ActionOpen with detail timeout", acts)
	}
	if len(src.calls) != 1 || src.calls[0].sub != sub || !src.calls[0].onSavepoint {
		t.Fatalf("Detail calls = %+v, want one for %+v on a savepoint", src.calls, sub)
	}
	if tx.begun != 1 || tx.committed != 1 || tx.rolledBk != 0 {
		t.Errorf("savepoint begun/committed/rolled back = %d/%d/%d, want 1/1/0", tx.begun, tx.committed, tx.rolledBk)
	}
}

// A severity action carries only the alert's id (Reconcile's own
// contract), so the subject comes from the open alerts Apply loaded.
func TestApplyTakesASeverityChangesSubjectFromTheOpenAlert(t *testing.T) {
	store := newFakeStore()
	sub := tupleSubject("web", "home", "http.assertion")
	open := openAlert(sub, rules.StateWarn, t0)
	store.open[hostID] = []alerts.Alert{open}
	tx := &fakeTx{}
	src := &fakeDetails{outer: tx, detail: `status == 200 — saw 503`}
	svc := detailService(store, src, &bytes.Buffer{})

	if err := svc.Apply(ctx, tx, input(stateAt(sub, rules.StateCrit, t0))); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	acts := store.executed[0]
	if len(acts) != 1 || acts[0].Kind != alerts.ActionSeverity || acts[0].AlertID != open.ID {
		t.Fatalf("executed = %+v, want one ActionSeverity on %s", acts, open.ID)
	}
	if acts[0].Detail != `status == 200 — saw 503` {
		t.Errorf("severity detail = %q", acts[0].Detail)
	}
	if len(src.calls) != 1 || src.calls[0].sub != sub {
		t.Errorf("Detail calls = %+v, want one for the open alert's subject %+v", src.calls, sub)
	}
}

// Close and flap actions carry no detail, and a host-scope opening (the
// unknown roll-up) has no tuple to explain: none of them asks the source.
func TestApplyAsksNothingForClosesFlapsAndTheHostAlert(t *testing.T) {
	closing := tupleSubject("web", "", "http.up")
	flapping := tupleSubject("dns", "", "dns.up")

	store := newFakeStore()
	store.open[hostID] = []alerts.Alert{
		openAlert(closing, rules.StateCrit, t0),
		openAlert(flapping, rules.StateCrit, t0),
	}
	tx := &fakeTx{}
	src := &fakeDetails{outer: tx, detail: "timeout"}
	svc := detailService(store, src, &bytes.Buffer{})

	flap := stateAt(flapping, rules.StateCrit, t0)
	flap.Flapping = true
	in := withHost(input(stateAt(closing, rules.StateOK, t0), flap),
		rules.HostState{HostID: hostID, State: rules.StateUnknown, Since: t0})
	if err := svc.Apply(ctx, tx, in); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	kinds := map[alerts.ActionKind]bool{}
	for _, a := range store.executed[0] {
		kinds[a.Kind] = true
		if a.Detail != "" {
			t.Errorf("%s action carries detail %q, want none", a.Kind, a.Detail)
		}
	}
	if !kinds[alerts.ActionClose] || !kinds[alerts.ActionFlapped] || !kinds[alerts.ActionOpen] {
		t.Fatalf("executed = %+v, want a close, a flap and the host-scope open", store.executed[0])
	}
	if len(src.calls) != 0 || tx.begun != 0 {
		t.Errorf("Detail called %d times, %d savepoints, want none", len(src.calls), tx.begun)
	}
}

// A failing lookup rolls back its savepoint, logs, and leaves the detail
// empty; Apply still executes the action and succeeds.
func TestApplyToleratesAFailingLookup(t *testing.T) {
	store := newFakeStore()
	tx := &fakeTx{}
	src := &fakeDetails{outer: tx, err: errors.New("relation does not exist")}
	var log bytes.Buffer
	svc := detailService(store, src, &log)

	sub := tupleSubject("web", "", "http.up")
	if err := svc.Apply(ctx, tx, input(stateAt(sub, rules.StateCrit, t0))); err != nil {
		t.Fatalf("Apply: %v — a failing lookup must never fail the evaluation", err)
	}
	if acts := store.executed[0]; len(acts) != 1 || acts[0].Detail != "" {
		t.Fatalf("executed = %+v, want one action without detail", acts)
	}
	if tx.begun != 1 || tx.rolledBk != 1 || tx.committed != 0 {
		t.Errorf("savepoint begun/committed/rolled back = %d/%d/%d, want 1/0/1", tx.begun, tx.committed, tx.rolledBk)
	}
	if !strings.Contains(log.String(), "relation does not exist") {
		t.Errorf("log = %q, want the lookup's error", log.String())
	}
}

// A savepoint that cannot even be taken is the same: logged, no detail,
// the action still executes.
func TestApplyToleratesASavepointThatCannotBeTaken(t *testing.T) {
	store := newFakeStore()
	tx := &fakeTx{beginErr: errors.New("conn busy")}
	src := &fakeDetails{outer: tx, detail: "timeout"}
	var log bytes.Buffer
	svc := detailService(store, src, &log)

	if err := svc.Apply(ctx, tx, input(stateAt(tupleSubject("web", "", "http.up"), rules.StateCrit, t0))); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if acts := store.executed[0]; len(acts) != 1 || acts[0].Detail != "" {
		t.Fatalf("executed = %+v, want one action without detail", acts)
	}
	if len(src.calls) != 0 || !strings.Contains(log.String(), "conn busy") {
		t.Errorf("calls = %d, log = %q", len(src.calls), log.String())
	}
}

// The detail is cut to 512 bytes on a rune boundary, whatever the source
// returns.
func TestApplyCutsTheDetail(t *testing.T) {
	store := newFakeStore()
	tx := &fakeTx{}
	src := &fakeDetails{outer: tx, detail: "x" + strings.Repeat("é", 400)}
	svc := detailService(store, src, &bytes.Buffer{})

	if err := svc.Apply(ctx, tx, input(stateAt(tupleSubject("web", "home", "http.assertion"), rules.StateCrit, t0))); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := store.executed[0][0].Detail
	if len(got) != 511 || !utf8.ValidString(got) || !strings.HasPrefix(src.detail, got) {
		t.Errorf("detail = %d bytes (valid UTF-8 %v), want the 511-byte prefix that ends on a rune boundary", len(got), utf8.ValidString(got))
	}
}

// Without a source (every Service a test builds with NewService alone)
// Apply takes no savepoint at all — the nil tx below would panic if it did.
func TestApplyWithoutADetailSourceTakesNoSavepoint(t *testing.T) {
	store := newFakeStore()
	svc := alerts.NewService(store, clockAt(t0))
	if err := svc.Apply(ctx, nil, input(stateAt(tupleSubject("web", "", "http.up"), rules.StateCrit, t0))); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if d := store.executed[0][0].Detail; d != "" {
		t.Errorf("detail = %q, want none", d)
	}
}
