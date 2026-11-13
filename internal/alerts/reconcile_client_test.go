// Client hosts (backlog 3.57, docs/superpowers/specs/2026-11-13-client-hosts-design.md,
// "Evaluation" items 1 and 3 and the kind switch): the host-scope subject
// carries either the unknown alert or — for a client that has not been
// seen for the organization's threshold — the absent warning (severity
// warn), never both. A frozen client opens no unknown alert and closes an
// open one as stale; an online client's unknown roll-up waits out the
// online window before its alert opens. A server is exactly as before.
package alerts_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

const (
	window   = 90 * time.Second
	absentIn = 14 * 24 * time.Hour
)

var (
	// lastSeen is the agent's last contact: fifteen days before the tick,
	// so a 14-day threshold has been reached a day ago.
	lastSeen = t0.Add(time.Hour).Add(-15 * 24 * time.Hour)
	// unknownHostID and warnHostID are the open host alerts the cases
	// below start from; fixed so the expected actions can name them.
	unknownHostID = uuid.MustParse("33333333-3333-4333-8333-333333333333")
	warnHostID    = uuid.MustParse("44444444-4444-4444-8444-444444444444")
)

func openUnknownHost() alerts.Alert {
	a := openAlert(hostSubject(), rules.StateUnknown, t0)
	a.ID = unknownHostID
	return a
}

func openAbsentWarning() alerts.Alert {
	a := openAlert(hostSubject(), rules.StateWarn, lastSeen.Add(absentIn))
	a.ID = warnHostID
	return a
}

// frozen is a frozen client's input: nothing judged, the previous roll-up
// carried as it is, and the absent verdict.
func frozen(host rules.HostState, absent bool, absentAfter time.Duration) rules.AlertInput {
	return rules.AlertInput{
		OrgID: orgID, HostID: hostID, At: t0.Add(time.Hour),
		Host:   host,
		Frozen: true, Absent: absent, AgentLastSeen: lastSeen, AbsentAfter: absentAfter,
		ClientWithAgent: true, UnknownGrace: window,
	}
}

// frozenSeen is frozen with the agent's last contact at seen.
func frozenSeen(host rules.HostState, seen time.Time, absent bool) rules.AlertInput {
	in := frozen(host, absent, absentIn)
	in.AgentLastSeen = seen
	return in
}

// onlineClient is an online client's judged input with roll-up host, in
// an online period that began well before the roll-up did.
func onlineClient(host rules.HostState) rules.AlertInput {
	in := withHost(input(), host)
	in.AbsentAfter = absentIn
	in.ClientWithAgent = true
	in.UnknownGrace = window
	in.AgentOnlineSince = t0.Add(-24 * time.Hour)
	return in
}

// reconnected is onlineClient whose online period began `ago` before the
// tick.
func reconnected(host rules.HostState, ago time.Duration) rules.AlertInput {
	in := onlineClient(host)
	in.AgentOnlineSince = in.At.Add(-ago)
	return in
}

// zeroWindow is onlineClient built with a zero online window: still a
// client with an agent, but with no grace.
func zeroWindow(host rules.HostState) rules.AlertInput {
	in := onlineClient(host)
	in.UnknownGrace = 0
	return in
}

// unknownSince is an unknown roll-up that began `ago` before the tick.
func unknownSince(ago time.Duration) rules.HostState {
	at := t0.Add(time.Hour)
	return rules.HostState{HostID: hostID, State: rules.StateUnknown, Unknown: 1, Since: at.Add(-ago), EvaluatedAt: at}
}

var (
	okHost      = rules.HostState{HostID: hostID, State: rules.StateOK, Since: t0, EvaluatedAt: t0.Add(time.Hour)}
	unknownHost = unknownSince(time.Hour)
)

func closeAction(id uuid.UUID, reason alerts.CloseReason) alerts.Action {
	return alerts.Action{Kind: alerts.ActionClose, AlertID: id, At: t0.Add(time.Hour), Reason: reason}
}

func openHost(at time.Time, severity rules.State) alerts.Action {
	return alerts.Action{Kind: alerts.ActionOpen, Subject: hostSubject(), At: at, Severity: severity}
}

func TestReconcileClientHostAlert(t *testing.T) {
	tupleCrit := openAlert(tupleSubject("disk", "/", "used_percent"), rules.StateCrit, t0)

	cases := []struct {
		name string
		in   rules.AlertInput
		open []alerts.Alert
		want []alerts.Action
	}{
		// Frozen: spec item 1 — no unknown alert, no stale close of a
		// tuple alert, nothing judged.
		{
			name: "a frozen client whose carried roll-up is unknown opens no unknown alert",
			in:   frozen(unknownHost, false, absentIn),
		},
		{
			name: "a frozen client leaves its open tuple alerts exactly as they are",
			in:   frozen(okHost, false, absentIn),
			open: []alerts.Alert{tupleCrit},
		},
		{
			// The kind switch (server to client while the agent is off):
			// the roll-up went unknown after the agent's last contact, so
			// the absence caused it — the subject no longer applies, it
			// did not recover.
			name: "a frozen client closes an unknown host alert its absence caused as stale",
			in:   frozen(unknownHost, false, absentIn),
			open: []alerts.Alert{openUnknownHost()},
			want: []alerts.Action{closeAction(unknownHostID, alerts.Stale)},
		},
		{
			// A shutdown that slipped past the grace: unknown from the
			// agent's last contact on — the boundary counts as caused.
			name: "a frozen client closes an unknown host alert that began at its last contact as stale",
			in:   frozenSeen(unknownSince(time.Hour), t0, false),
			open: []alerts.Alert{openUnknownHost()},
			want: []alerts.Action{closeAction(unknownHostID, alerts.Stale)},
		},
		{
			// A real problem while the client was on: the unknown predates
			// the agent's last contact, so it stays open through the
			// freeze — no close, no notification, its acknowledgement and
			// any escalation exactly as they were.
			name: "a frozen client keeps an unknown host alert that predates its last contact",
			in:   frozenSeen(unknownSince(time.Hour), t0.Add(30*time.Minute), false),
			open: []alerts.Alert{openUnknownHost()},
		},
		{
			// A certificate renewal reads the agent as last seen at its
			// registration, i.e. just now: still not what caused the
			// unknown, so the alert stays.
			name: "a client frozen by a certificate renewal keeps its older unknown host alert",
			in:   frozenSeen(unknownSince(time.Hour), t0.Add(time.Hour), false),
			open: []alerts.Alert{openUnknownHost()},
		},
		{
			// Once the warning is due it takes the one host-scope subject;
			// the older unknown alert gives way to it.
			name: "the absent warning replaces an unknown host alert that predates the last contact",
			in:   frozenSeen(unknownSince(16*24*time.Hour), lastSeen, true),
			open: []alerts.Alert{openUnknownHost()},
			want: []alerts.Action{
				closeAction(unknownHostID, alerts.Stale),
				openHost(t0.Add(time.Hour), rules.StateWarn),
			},
		},
		{
			name: "a frozen client closes an open unknown host alert stale whatever its carried roll-up",
			in:   frozen(okHost, false, absentIn),
			open: []alerts.Alert{openUnknownHost(), tupleCrit},
			want: []alerts.Action{closeAction(unknownHostID, alerts.Stale)},
		},
		{
			name: "a frozen client not yet absent opens nothing",
			in:   frozen(okHost, false, absentIn),
		},
		{
			name: "with the warning off a frozen client never opens it",
			in:   frozen(okHost, false, 0),
		},
		// Spec item 3: the absent warning.
		{
			// Dated at the tick that found it: the "since" fact is the
			// agent's last contact, which the warning shows on its own.
			name: "an absent client opens the host alert at severity warn, dated at the tick",
			in:   frozen(okHost, true, absentIn),
			want: []alerts.Action{openHost(t0.Add(time.Hour), rules.StateWarn)},
		},
		{
			name: "an absent client with no roll-up at all still gets the warning",
			in:   frozen(rules.HostState{}, true, absentIn),
			want: []alerts.Action{openHost(t0.Add(time.Hour), rules.StateWarn)},
		},
		{
			name: "an open absent warning is not opened again on the next tick",
			in:   frozen(okHost, true, absentIn),
			open: []alerts.Alert{openAbsentWarning()},
		},
		{
			// Same subject: the unknown alert is closed first, then the
			// warning opens, in that order (the one-open-per-subject index).
			name: "an absent client with an open unknown host alert closes it stale before the warning opens",
			in:   frozen(unknownHost, true, absentIn),
			open: []alerts.Alert{openUnknownHost()},
			want: []alerts.Action{
				closeAction(unknownHostID, alerts.Stale),
				openHost(t0.Add(time.Hour), rules.StateWarn),
			},
		},
		{
			// The threshold was raised or the warning switched off while
			// the client is still away: the warning no longer applies, but
			// the client did not come back either.
			name: "a frozen client no longer absent closes the warning as stale",
			in:   frozen(okHost, false, 0),
			open: []alerts.Alert{openAbsentWarning()},
			want: []alerts.Action{closeAction(warnHostID, alerts.Stale)},
		},
		// Online again.
		{
			name: "an online client closes its absent warning as resolved",
			in:   onlineClient(okHost),
			open: []alerts.Alert{openAbsentWarning()},
			want: []alerts.Action{closeAction(warnHostID, alerts.Resolved)},
		},
		{
			name: "an online client with no roll-up closes its absent warning as resolved",
			in:   onlineClient(rules.HostState{}),
			open: []alerts.Alert{openAbsentWarning()},
			want: []alerts.Action{closeAction(warnHostID, alerts.Resolved)},
		},
		{
			name: "an online client still in its unknown grace closes the warning and opens nothing",
			in:   onlineClient(unknownSince(30 * time.Second)),
			open: []alerts.Alert{openAbsentWarning()},
			want: []alerts.Action{closeAction(warnHostID, alerts.Resolved)},
		},
		{
			name: "an online client past its unknown grace closes the warning before the unknown alert opens",
			in:   onlineClient(unknownSince(2 * time.Minute)),
			open: []alerts.Alert{openAbsentWarning()},
			want: []alerts.Action{
				closeAction(warnHostID, alerts.Resolved),
				openHost(t0.Add(time.Hour).Add(-2*time.Minute), rules.StateUnknown),
			},
		},
		// The shutdown race: an online client's unknown roll-up waits out
		// the online window.
		{
			name: "an online client's unknown roll-up inside the grace opens nothing",
			in:   onlineClient(unknownSince(window - time.Second)),
		},
		{
			name: "an online client's unknown roll-up opens the alert once the grace has passed, dated at the roll-up's start",
			in:   onlineClient(unknownSince(window)),
			want: []alerts.Action{openHost(t0.Add(time.Hour).Add(-window), rules.StateUnknown)},
		},
		{
			name: "an online client's open unknown host alert is kept while the roll-up stays unknown",
			in:   onlineClient(unknownSince(30 * time.Second)),
			open: []alerts.Alert{openUnknownHost()},
		},
		{
			name: "an online client's unknown host alert closes as resolved when the roll-up recovers",
			in:   onlineClient(okHost),
			open: []alerts.Alert{openUnknownHost()},
			want: []alerts.Action{closeAction(unknownHostID, alerts.Resolved)},
		},
		// A client back from an outage whose roll-up was carried unknown
		// through the freeze: the grace runs from the online period's
		// start, not from the roll-up's old Since.
		{
			name: "a reconnected client's carried unknown roll-up waits its grace from the online start",
			in:   reconnected(unknownHost, window-time.Second),
		},
		{
			name: "a reconnected client's carried unknown roll-up opens once the grace from the online start has passed",
			in:   reconnected(unknownHost, window),
			want: []alerts.Action{openHost(t0, rules.StateUnknown)},
		},
		// A store built with a zero window: no grace, but still a client
		// with an agent, so its return still resolves the warning.
		{
			name: "with a zero window an online client's warning still closes as resolved",
			in:   zeroWindow(okHost),
			open: []alerts.Alert{openAbsentWarning()},
			want: []alerts.Action{closeAction(warnHostID, alerts.Resolved)},
		},
		{
			name: "with a zero window an online client's unknown roll-up opens the alert at once",
			in:   zeroWindow(unknownSince(0)),
			want: []alerts.Action{openHost(t0.Add(time.Hour), rules.StateUnknown)},
		},
		// Client to server while the agent is off: no grace, no
		// presence; the server goes unknown as any server would.
		{
			name: "a host that is no longer a client closes the warning stale before its unknown alert opens",
			in:   withHost(input(), unknownSince(time.Second)),
			open: []alerts.Alert{openAbsentWarning()},
			want: []alerts.Action{
				closeAction(warnHostID, alerts.Stale),
				openHost(t0.Add(time.Hour).Add(-time.Second), rules.StateUnknown),
			},
		},
		{
			name: "a host that is no longer a client and reads ok closes the warning stale",
			in:   input(),
			open: []alerts.Alert{openAbsentWarning()},
			want: []alerts.Action{closeAction(warnHostID, alerts.Stale)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := alerts.Reconcile(tc.in, tc.open)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Reconcile:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// TestReconcileServerHostAlertIsUnchanged pins the server path: with no
// presence (no grace, never frozen, never absent) the host alert opens the
// moment the roll-up reads unknown — even a roll-up that began at this
// very tick — and closes resolved on recovery, stale on a zero roll-up.
func TestReconcileServerHostAlertIsUnchanged(t *testing.T) {
	cases := []struct {
		name string
		in   rules.AlertInput
		open []alerts.Alert
		want []alerts.Action
	}{
		{
			name: "opens at once",
			in:   withHost(input(), unknownSince(0)),
			want: []alerts.Action{openHost(t0.Add(time.Hour), rules.StateUnknown)},
		},
		{
			name: "stays open",
			in:   withHost(input(), unknownHost),
			open: []alerts.Alert{openUnknownHost()},
		},
		{
			name: "closes resolved",
			in:   input(),
			open: []alerts.Alert{openUnknownHost()},
			want: []alerts.Action{closeAction(unknownHostID, alerts.Resolved)},
		},
		{
			name: "closes stale on a zero roll-up",
			in:   withHost(input(), rules.HostState{}),
			open: []alerts.Alert{openUnknownHost()},
			want: []alerts.Action{closeAction(unknownHostID, alerts.Stale)},
		},
		{
			name: "nothing to do",
			in:   input(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := alerts.Reconcile(tc.in, tc.open)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Reconcile:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}
