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
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// fakeTx is the slice of pgx.Tx Apply's capture touches: Exec, for the
// savepoint statements. It records them, and fails the one named in fail.
// Anything else panics through the nil embedded interface, which is the
// point — the capture must not issue statements of its own.
type fakeTx struct {
	pgx.Tx
	stmts []string
	fail  map[string]error
}

func (t *fakeTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	t.stmts = append(t.stmts, sql)
	return pgconn.CommandTag{}, t.fail[sql]
}

const (
	setSP      = "SAVEPOINT alert_detail"
	releaseSP  = "RELEASE SAVEPOINT alert_detail"
	rollbackSP = "ROLLBACK TO SAVEPOINT alert_detail"
)

// detailCall is one Detail call, with whether it ran inside the savepoint
// (the last statement on the transaction before it was SAVEPOINT).
type detailCall struct {
	sub         alerts.Subject
	inSavepoint bool
}

// fakeDetails answers every subject with the same detail, or err, and
// counts how many lookups Apply started.
type fakeDetails struct {
	outer   *fakeTx
	detail  string
	err     error
	calls   []detailCall
	lookups int
}

func (f *fakeDetails) Lookup() alerts.DetailLookup {
	f.lookups++
	return f
}

func (f *fakeDetails) Detail(_ context.Context, tx pgx.Tx, orgID uuid.UUID, sub alerts.Subject) (string, error) {
	in := tx == f.outer && len(f.outer.stmts) > 0 && f.outer.stmts[len(f.outer.stmts)-1] == setSP
	f.calls = append(f.calls, detailCall{sub: sub, inSavepoint: in})
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

func stmtsEqual(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("statements = %q, want %q", got, want)
	}
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
	if len(src.calls) != 1 || src.calls[0].sub != sub || !src.calls[0].inSavepoint {
		t.Fatalf("Detail calls = %+v, want one for %+v inside the savepoint", src.calls, sub)
	}
	stmtsEqual(t, tx.stmts, setSP, releaseSP)
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
	if len(src.calls) != 0 || len(tx.stmts) != 0 || src.lookups != 0 {
		t.Errorf("Detail called %d times, statements %q, %d lookups, want none", len(src.calls), tx.stmts, src.lookups)
	}
}

// A failing lookup rolls back to its savepoint and releases it, logs, and
// leaves the detail empty; Apply still executes the action and succeeds.
// On an opening there is nothing to keep.
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
	if acts := store.executed[0]; len(acts) != 1 || acts[0].Detail != "" || acts[0].KeepDetail {
		t.Fatalf("executed = %+v, want one opening without detail", acts)
	}
	stmtsEqual(t, tx.stmts, setSP, rollbackSP, releaseSP)
	if !strings.Contains(log.String(), "relation does not exist") {
		t.Errorf("log = %q, want the lookup's error", log.String())
	}
}

// A lookup that swallowed an SQL error returns success on an aborted
// transaction, so the RELEASE fails: Apply still rolls back to the
// savepoint (which clears the aborted state) and releases it, and the
// detail is dropped as a failure.
func TestApplyRollsBackWhenTheReleaseFails(t *testing.T) {
	store := newFakeStore()
	tx := &fakeTx{fail: map[string]error{releaseSP: errors.New("current transaction is aborted")}}
	src := &fakeDetails{outer: tx, detail: "timeout"}
	var log bytes.Buffer
	svc := detailService(store, src, &log)

	if err := svc.Apply(ctx, tx, input(stateAt(tupleSubject("web", "", "http.up"), rules.StateCrit, t0))); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if acts := store.executed[0]; len(acts) != 1 || acts[0].Detail != "" {
		t.Fatalf("executed = %+v, want one action without detail", acts)
	}
	stmtsEqual(t, tx.stmts, setSP, releaseSP, rollbackSP, releaseSP)
	if !strings.Contains(log.String(), "current transaction is aborted") {
		t.Errorf("log = %q, want the release's error", log.String())
	}
}

// On a severity change a failed lookup is not "no detail": the action
// keeps the alert's previous detail (KeepDetail), while a lookup that
// found nothing replaces it with none.
func TestApplyKeepsTheDetailWhenASeverityLookupFails(t *testing.T) {
	sub := tupleSubject("web", "home", "http.assertion")
	for _, tc := range []struct {
		name string
		err  error
		keep bool
	}{
		{"failed", errors.New("boom"), true},
		{"found nothing", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.open[hostID] = []alerts.Alert{openAlert(sub, rules.StateWarn, t0)}
			tx := &fakeTx{}
			svc := detailService(store, &fakeDetails{outer: tx, err: tc.err}, &bytes.Buffer{})
			if err := svc.Apply(ctx, tx, input(stateAt(sub, rules.StateCrit, t0))); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			a := store.executed[0][0]
			if a.Kind != alerts.ActionSeverity || a.Detail != "" || a.KeepDetail != tc.keep {
				t.Errorf("action = %+v, want a severity change with KeepDetail %v", a, tc.keep)
			}
		})
	}
}

// A savepoint that cannot even be set is the same: logged, no detail, the
// action still executes, and the lookup is never asked.
func TestApplyToleratesASavepointThatCannotBeSet(t *testing.T) {
	store := newFakeStore()
	tx := &fakeTx{fail: map[string]error{setSP: errors.New("conn busy")}}
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

// All subjects of one Apply share a host, so Apply starts one lookup for
// them all — the adapter memoises what it reads about the host on it.
func TestApplyStartsOneLookupPerEvaluation(t *testing.T) {
	store := newFakeStore()
	tx := &fakeTx{}
	src := &fakeDetails{outer: tx, detail: "x"}
	svc := detailService(store, src, &bytes.Buffer{})
	in := input(
		stateAt(tupleSubject("shop", "a", "http.assertion"), rules.StateCrit, t0),
		stateAt(tupleSubject("shop", "b", "http.assertion"), rules.StateWarn, t0),
		stateAt(tupleSubject("shop", "", "http.up"), rules.StateCrit, t0),
	)
	if err := svc.Apply(ctx, tx, in); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(src.calls) != 3 || src.lookups != 1 {
		t.Errorf("Detail calls = %d, lookups = %d; want 3 calls on one lookup", len(src.calls), src.lookups)
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
// Apply sets no savepoint at all — the nil tx below would panic if it did.
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
