// Reconcile is table-driven, one test per lifecycle decision in the
// design doc (docs/superpowers/specs/2026-10-06-v0.4a-alerts-design.md,
// "Lifecycle"), because the implementation is only correct relative to
// those decisions and each test is named after the one it pins.
package alerts_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

var (
	// t0 is when every test's tuple state began. input's own At is an
	// hour later, so any test that accidentally used the tick time where
	// it should have used state.Since fails loudly instead of by luck.
	t0     = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	hostID = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	orgID  = uuid.MustParse("22222222-2222-4222-8222-222222222222")
)

// tupleSubject builds a tuple-scope Subject on the fixed test host, the
// shape every open alert and every TupleState below is keyed on.
func tupleSubject(checkKey, item, metric string) alerts.Subject {
	return alerts.Subject{HostID: hostID, Scope: alerts.ScopeTuple, CheckKey: checkKey, Item: item, Metric: metric}
}

func hostSubject() alerts.Subject {
	return alerts.Subject{HostID: hostID, Scope: alerts.ScopeHost}
}

// stateAt is one tuple's post-evaluation state, as Reconcile receives it:
// sub's own tuple, at state, since the given time.
func stateAt(sub alerts.Subject, state rules.State, since time.Time) rules.TupleState {
	return rules.TupleState{
		Tuple:       rules.Tuple{CheckKey: sub.CheckKey, Item: sub.Item, Metric: sub.Metric},
		State:       state,
		Since:       since,
		EvaluatedAt: since,
	}
}

// input wraps states into an AlertInput on the fixed test host, with a
// healthy host roll-up unless a test overrides it with withHost. At is
// deliberately an hour after t0, so "the open carries state.Since, not
// in.At" cannot pass by the two timestamps happening to agree.
func input(states ...rules.TupleState) rules.AlertInput {
	return rules.AlertInput{
		OrgID:  orgID,
		HostID: hostID,
		At:     t0.Add(time.Hour),
		States: states,
		Host:   rules.HostState{HostID: hostID, State: rules.StateOK, Since: t0, EvaluatedAt: t0.Add(time.Hour)},
	}
}

func withHost(in rules.AlertInput, host rules.HostState) rules.AlertInput {
	in.Host = host
	return in
}

// openAlert is a plain open alert on sub, at severity, opened at openedAt,
// with Peak following Severity — the shape every test below starts from
// before overriding the one or two fields its own case is about.
func openAlert(sub alerts.Subject, severity rules.State, openedAt time.Time) alerts.Alert {
	return alerts.Alert{
		ID:       uuid.Must(uuid.NewV7()),
		OrgID:    orgID,
		Subject:  sub,
		OpenedAt: openedAt,
		Severity: severity,
		Peak:     severity,
	}
}

func findKind(acts []alerts.Action, kind alerts.ActionKind) (alerts.Action, bool) {
	for _, a := range acts {
		if a.Kind == kind {
			return a, true
		}
	}
	return alerts.Action{}, false
}

func hasSeverity(acts []alerts.Action, want rules.State) bool {
	a, ok := findKind(acts, alerts.ActionSeverity)
	return ok && a.Severity == want
}

func hasClose(acts []alerts.Action) bool {
	_, ok := findKind(acts, alerts.ActionClose)
	return ok
}

func mustBeOneOpen(t *testing.T, acts []alerts.Action, wantAt time.Time, wantSeverity rules.State) {
	t.Helper()
	if len(acts) != 1 {
		t.Fatalf("got %d actions, want exactly 1 open: %+v", len(acts), acts)
	}
	a := acts[0]
	if a.Kind != alerts.ActionOpen {
		t.Fatalf("action kind = %q, want open", a.Kind)
	}
	if !a.At.Equal(wantAt) {
		t.Fatalf("open At = %v, want %v", a.At, wantAt)
	}
	if a.Severity != wantSeverity {
		t.Fatalf("open Severity = %q, want %q", a.Severity, wantSeverity)
	}
}

func TestReconcileOpensOneAlertPerEpisode(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_percent")
	// warn opens; the alert then survives crit and the fall back to warn.
	acts := alerts.Reconcile(input(stateAt(sub, rules.StateWarn, t0)), nil)
	mustBeOneOpen(t, acts, t0, rules.StateWarn)

	a := openAlert(sub, rules.StateWarn, t0)
	got := alerts.Reconcile(input(stateAt(sub, rules.StateCrit, t0)), []alerts.Alert{a})
	if !hasSeverity(got, rules.StateCrit) {
		t.Fatalf("rising to crit produced %+v, want a severity action", got)
	}

	a.Severity, a.Peak = rules.StateCrit, rules.StateCrit
	got = alerts.Reconcile(input(stateAt(sub, rules.StateWarn, t0)), []alerts.Alert{a})
	if hasClose(got) {
		t.Fatalf("falling back to warn closed the alert: %+v", got)
	}
	if !hasSeverity(got, rules.StateWarn) {
		t.Fatalf("falling back to warn produced %+v, want a severity action carrying warn", got)
	}
}

func TestReconcilePeakOnlyRises(t *testing.T) {
	sub := tupleSubject("cpu", "", "used_percent")
	a := openAlert(sub, rules.StateCrit, t0)
	a.Peak = rules.StateCrit // the alert's episode already touched crit

	got := alerts.Reconcile(input(stateAt(sub, rules.StateWarn, t0)), []alerts.Alert{a})
	act, ok := findKind(got, alerts.ActionSeverity)
	if !ok {
		t.Fatalf("falling to warn produced no severity action: %+v", got)
	}
	if act.Severity != rules.StateWarn {
		t.Fatalf("severity action carried %q, want warn — Reconcile must not pre-lower the peak, the executor's GREATEST does that", act.Severity)
	}
}

func TestReconcileVoidsAckOnWorsening(t *testing.T) {
	sub := tupleSubject("mem", "", "used_percent")

	worsening := openAlert(sub, rules.StateWarn, t0)
	worsening.AckSeverity = rules.StateWarn
	got := alerts.Reconcile(input(stateAt(sub, rules.StateCrit, t0)), []alerts.Alert{worsening})
	act, ok := findKind(got, alerts.ActionSeverity)
	if !ok || !act.VoidsAck {
		t.Fatalf("worsening past an ack at warn did not void it: %+v", got)
	}

	improving := openAlert(sub, rules.StateCrit, t0)
	improving.AckSeverity = rules.StateCrit
	got = alerts.Reconcile(input(stateAt(sub, rules.StateWarn, t0)), []alerts.Alert{improving})
	act, ok = findKind(got, alerts.ActionSeverity)
	if !ok {
		t.Fatalf("improving to warn produced no severity action: %+v", got)
	}
	if act.VoidsAck {
		t.Fatalf("an improvement voided the acknowledgement: %+v", act)
	}
}

func TestReconcileUnknownOpensOnlyAHostAlert(t *testing.T) {
	subs := []alerts.Subject{
		tupleSubject("disk", "/", "used_percent"),
		tupleSubject("disk", "/var", "used_percent"),
		tupleSubject("cpu", "", "used_percent"),
		tupleSubject("mem", "", "used_percent"),
	}
	states := make([]rules.TupleState, len(subs))
	for i, s := range subs {
		states[i] = stateAt(s, rules.StateUnknown, t0)
	}
	in := withHost(input(states...), rules.HostState{HostID: hostID, State: rules.StateUnknown, Since: t0, EvaluatedAt: t0.Add(time.Hour)})

	got := alerts.Reconcile(in, nil)
	var opens []alerts.Action
	for _, a := range got {
		if a.Kind == alerts.ActionOpen {
			opens = append(opens, a)
		}
	}
	if len(opens) != 1 {
		t.Fatalf("got %d open actions, want exactly 1: %+v", len(opens), got)
	}
	if opens[0].Subject.Scope != alerts.ScopeHost {
		t.Fatalf("the one open action has scope %q, want host", opens[0].Subject.Scope)
	}
	if opens[0].Severity != rules.StateUnknown {
		t.Fatalf("the host open carries severity %q, want unknown", opens[0].Severity)
	}
	if !opens[0].At.Equal(t0) {
		t.Fatalf("the host open's At = %v, want Host.Since = %v (not in.At = %v)", opens[0].At, t0, t0.Add(time.Hour))
	}
}

func TestReconcileFlappingHoldsTheAlertOpen(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_percent")
	a := openAlert(sub, rules.StateWarn, t0)

	flapping := stateAt(sub, rules.StateOK, t0)
	flapping.Flapping = true
	got := alerts.Reconcile(input(flapping), []alerts.Alert{a})
	if hasClose(got) {
		t.Fatalf("a flapping tuple at ok closed its alert: %+v", got)
	}
	flapped, ok := findKind(got, alerts.ActionFlapped)
	if !ok {
		t.Fatalf("a flapping tuple at ok on an alert not yet marked flapping produced %+v, want a flapped action latching was_flapping", got)
	}
	if flapped.AlertID != a.ID {
		t.Fatalf("flapped action carries AlertID %v, want %v", flapped.AlertID, a.ID)
	}

	settled := stateAt(sub, rules.StateOK, t0)
	settled.Flapping = false
	got = alerts.Reconcile(input(settled), []alerts.Alert{a})
	act, ok := findKind(got, alerts.ActionClose)
	if !ok {
		t.Fatalf("a settled tuple at ok did not close its alert: %+v", got)
	}
	if act.Reason != alerts.Resolved {
		t.Fatalf("close reason = %q, want resolved", act.Reason)
	}
}

// TestReconcileIgnoresAHealthyTupleWithNoOpenAlert is the ordinary case
// every tick mostly consists of: a tuple reads ok and never had an alert
// open on it, so Reconcile has nothing to decide.
func TestReconcileIgnoresAHealthyTupleWithNoOpenAlert(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_percent")
	got := alerts.Reconcile(input(stateAt(sub, rules.StateOK, t0)), nil)
	if len(got) != 0 {
		t.Fatalf("a healthy tuple with no open alert produced %+v, want no actions", got)
	}
}

// TestReconcileStaysQuietOnAnAlreadyFlaggedFlappingTuple is the steady
// state TestReconcileFlappingHoldsTheAlertOpen's first half leads into: a
// tuple still flapping at ok, on an alert already marked WasFlapping,
// produces nothing at all — neither a close nor a repeated flapped action
// — every tick it stays that way.
func TestReconcileStaysQuietOnAnAlreadyFlaggedFlappingTuple(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_percent")
	a := openAlert(sub, rules.StateWarn, t0)
	a.WasFlapping = true

	flapping := stateAt(sub, rules.StateOK, t0)
	flapping.Flapping = true
	got := alerts.Reconcile(input(flapping), []alerts.Alert{a})
	if len(got) != 0 {
		t.Fatalf("an already-flagged flapping tuple produced %+v, want no actions", got)
	}
}

func TestReconcileOpensAtStateSince(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_percent")
	since := t0
	in := input(stateAt(sub, rules.StateWarn, since))
	if in.At.Equal(since) {
		t.Fatalf("test setup bug: in.At must differ from state.Since for this test to mean anything")
	}

	got := alerts.Reconcile(in, nil)
	act, ok := findKind(got, alerts.ActionOpen)
	if !ok {
		t.Fatalf("no open action: %+v", got)
	}
	if !act.At.Equal(since) {
		t.Fatalf("ActionOpen.At = %v, want state.Since = %v (got in.At = %v instead)", act.At, since, in.At)
	}
}

// TestReconcileKeepsSeverityWhenTupleGoesUnknown is Review Focus 1: an
// open alert at crit whose tuple now reads unknown must emit no severity
// action and no close. unknown is not ok, so it cannot close the alert;
// and decision 4 says unknown is answered at the host, so it must not
// become the alert's severity either.
func TestReconcileKeepsSeverityWhenTupleGoesUnknown(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_percent")
	a := openAlert(sub, rules.StateCrit, t0)
	a.Peak = rules.StateCrit

	got := alerts.Reconcile(input(stateAt(sub, rules.StateUnknown, t0)), []alerts.Alert{a})
	if len(got) != 0 {
		t.Fatalf("a tuple going unknown produced %+v, want no actions on its open alert", got)
	}
}

// TestReconcileClearsSnoozeUntilChangeOnSeverityChange is Review Focus 2:
// snooze_until_change is an unbounded mute, so any severity change — in
// either direction — must clear it, or the alert stays muted forever.
func TestReconcileClearsSnoozeUntilChangeOnSeverityChange(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_percent")

	rising := openAlert(sub, rules.StateWarn, t0)
	rising.SnoozeUntilChange = true
	got := alerts.Reconcile(input(stateAt(sub, rules.StateCrit, t0)), []alerts.Alert{rising})
	act, ok := findKind(got, alerts.ActionSeverity)
	if !ok || !act.ClearsSnoozeUntilChange {
		t.Fatalf("a rising severity did not clear snooze_until_change: %+v", got)
	}

	falling := openAlert(sub, rules.StateCrit, t0)
	falling.SnoozeUntilChange = true
	got = alerts.Reconcile(input(stateAt(sub, rules.StateWarn, t0)), []alerts.Alert{falling})
	act, ok = findKind(got, alerts.ActionSeverity)
	if !ok || !act.ClearsSnoozeUntilChange {
		t.Fatalf("a falling severity did not clear snooze_until_change: %+v", got)
	}
}

// TestReconcileLeavesSnoozeUntilChangeWhenSeverityHolds pairs with the
// review-focus test above: when the severity does not change, Reconcile
// must not emit a severity action at all, so the executor never touches
// snooze_until_change on an unrelated tick.
func TestReconcileLeavesSnoozeUntilChangeWhenSeverityHolds(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_percent")
	a := openAlert(sub, rules.StateWarn, t0)
	a.SnoozeUntilChange = true

	got := alerts.Reconcile(input(stateAt(sub, rules.StateWarn, t0)), []alerts.Alert{a})
	if _, ok := findKind(got, alerts.ActionSeverity); ok {
		t.Fatalf("an unchanged severity produced a severity action: %+v", got)
	}
}

func TestReconcileLatchesWasFlappingOnce(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_percent")
	a := openAlert(sub, rules.StateWarn, t0)

	flapping := stateAt(sub, rules.StateWarn, t0)
	flapping.Flapping = true
	got := alerts.Reconcile(input(flapping), []alerts.Alert{a})
	flapped, ok := findKind(got, alerts.ActionFlapped)
	if !ok {
		t.Fatalf("a flapping tuple on an alert not yet marked flapping produced %+v, want a flapped action", got)
	}
	if flapped.AlertID != a.ID {
		t.Fatalf("flapped action carries AlertID %v, want %v", flapped.AlertID, a.ID)
	}

	a.WasFlapping = true
	got = alerts.Reconcile(input(flapping), []alerts.Alert{a})
	if _, ok := findKind(got, alerts.ActionFlapped); ok {
		t.Fatalf("an already-flapping alert produced another flapped action: %+v", got)
	}
}

func TestReconcileClosesHostAlertWhenHostRecovers(t *testing.T) {
	h := alerts.Alert{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Subject: hostSubject(), Severity: rules.StateUnknown, Peak: rules.StateUnknown}

	got := alerts.Reconcile(input(), []alerts.Alert{h})
	act, ok := findKind(got, alerts.ActionClose)
	if !ok {
		t.Fatalf("a recovered host did not close its host-scope alert: %+v", got)
	}
	if act.AlertID != h.ID {
		t.Fatalf("close action carries AlertID %v, want %v", act.AlertID, h.ID)
	}
	if act.Reason != alerts.Resolved {
		t.Fatalf("close reason = %q, want resolved", act.Reason)
	}
}

func TestReconcileDoesNotReopenAnAlreadyOpenHostAlert(t *testing.T) {
	h := alerts.Alert{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Subject: hostSubject(), Severity: rules.StateUnknown, Peak: rules.StateUnknown}
	in := withHost(input(), rules.HostState{HostID: hostID, State: rules.StateUnknown, Since: t0, EvaluatedAt: t0.Add(time.Hour)})

	got := alerts.Reconcile(in, []alerts.Alert{h})
	if len(got) != 0 {
		t.Fatalf("an already-open host alert produced %+v, want no actions", got)
	}
}

// TestReconcileOrderFollowsStatesOrder pins the determinism requirement:
// the returned []Action must be a function of in.States' own order, never
// of Go's randomized map iteration order over the alerts Reconcile indexes
// internally from open. Odd-indexed metrics already have an open alert (at
// warn, with the tuple now reading crit), so Reconcile must advance them
// — ActionSeverity, carrying AlertID — while the even-indexed ones have no
// alert and must open instead — ActionOpen, carrying Subject. A nil open
// slice (the previous version of this test) leaves bySubject empty, so an
// implementation that produced output by ranging over that empty map
// would emit nothing and still pass; populating it here means a
// map-iteration bug scrambles this alternating pattern instead. Run across
// several input sizes since such a bug would not necessarily show on every
// length.
func TestReconcileOrderFollowsStatesOrder(t *testing.T) {
	metrics := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7"}
	subs := make([]alerts.Subject, len(metrics))
	states := make([]rules.TupleState, len(metrics))
	for i, m := range metrics {
		subs[i] = tupleSubject("disk", "/", m)
		states[i] = stateAt(subs[i], rules.StateCrit, t0)
	}

	open := make([]alerts.Alert, 0, len(metrics)/2)
	alertIDs := make(map[int]uuid.UUID, len(metrics)/2)
	for i := range metrics {
		if i%2 == 1 {
			a := openAlert(subs[i], rules.StateWarn, t0)
			open = append(open, a)
			alertIDs[i] = a.ID
		}
	}

	for n := 1; n <= len(metrics); n++ {
		got := alerts.Reconcile(input(states[:n]...), open)
		if len(got) != n {
			t.Fatalf("n=%d: got %d actions, want %d: %+v", n, len(got), n, got)
		}
		for i, act := range got {
			if i%2 == 0 {
				if act.Kind != alerts.ActionOpen || act.Subject.Metric != metrics[i] {
					t.Fatalf("n=%d: action %d = %+v, want an open for metric %q (order must follow in.States)", n, i, act, metrics[i])
				}
				continue
			}
			if act.Kind != alerts.ActionSeverity || act.AlertID != alertIDs[i] {
				t.Fatalf("n=%d: action %d = %+v, want a severity action for alert %v (order must follow in.States)", n, i, act, alertIDs[i])
			}
		}
	}
}

// TestReconcileClosesAnUnmeasuredTupleStale is design decision 5's second
// path, decided here rather than by the sweep (v0.4a final review,
// Critical): a tuple whose check the manager no longer resolves is left
// out of the evaluation with its check_states row standing, so it appears
// in AlertInput.Unmeasured and in no TupleState at all. It is not healthy
// — nothing judged it — so `stale`, not `resolved`, and dated by the
// tick's own At, since nothing else says when it went.
func TestReconcileClosesAnUnmeasuredTupleStale(t *testing.T) {
	sub := tupleSubject("disk", "/", "used_pct")
	a := openAlert(sub, rules.StateCrit, t0)

	in := input()
	in.Unmeasured = []rules.Tuple{{CheckKey: sub.CheckKey, Item: sub.Item, Metric: sub.Metric}}

	acts := alerts.Reconcile(in, []alerts.Alert{a})
	if len(acts) != 1 {
		t.Fatalf("got %d actions, want exactly 1 close: %+v", len(acts), acts)
	}
	got := acts[0]
	if got.Kind != alerts.ActionClose || got.AlertID != a.ID {
		t.Fatalf("action = %+v, want a close of %s", got, a.ID)
	}
	if got.Reason != alerts.Stale {
		t.Fatalf("close reason = %q, want stale", got.Reason)
	}
	if !got.At.Equal(in.At) {
		t.Fatalf("close At = %v, want the tick's own %v", got.At, in.At)
	}
}

// TestReconcileIgnoresUnmeasuredTuplesWithNoOpenAlert: the report is made
// every tick a tuple stays unmeasured (rules.Result.Unmeasured's own doc
// comment), so the second and every later tick must decide nothing at all
// rather than closing an already-closed alert.
func TestReconcileIgnoresUnmeasuredTuplesWithNoOpenAlert(t *testing.T) {
	in := input()
	in.Unmeasured = []rules.Tuple{{CheckKey: "disk", Item: "/", Metric: "used_pct"}}

	if acts := alerts.Reconcile(in, nil); len(acts) != 0 {
		t.Fatalf("got %+v, want no actions for an unmeasured tuple nothing is open on", acts)
	}
}

// TestReconcileClosesTheHostAlertStaleWhenNothingIsJudged is decision 5
// applied to the host roll-up. rules.Evaluate returns the ZERO HostState
// when it judged no tuple at all — "write no row, and delete the one that
// is there" — which happens exactly when every check has been taken off
// the host. That is a vanished subject, not a recovery, so the host alert
// closes stale; a roll-up that actually reads ok still closes resolved.
func TestReconcileClosesTheHostAlertStaleWhenNothingIsJudged(t *testing.T) {
	a := openAlert(hostSubject(), rules.StateUnknown, t0)

	gone := alerts.Reconcile(withHost(input(), rules.HostState{HostID: hostID}), []alerts.Alert{a})
	if len(gone) != 1 || gone[0].Kind != alerts.ActionClose || gone[0].Reason != alerts.Stale {
		t.Fatalf("zero roll-up produced %+v, want one close with reason stale", gone)
	}

	healthy := alerts.Reconcile(input(), []alerts.Alert{a})
	if len(healthy) != 1 || healthy[0].Kind != alerts.ActionClose || healthy[0].Reason != alerts.Resolved {
		t.Fatalf("an ok roll-up produced %+v, want one close with reason resolved", healthy)
	}
}
