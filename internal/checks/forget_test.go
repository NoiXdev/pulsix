// The service half of forgetting items (backlog 3.12b), against fakes:
// what ForgetItem and ForgetExpired hand the store and the alert closer,
// and the expiry rule the re-check inside each forget applies. The store's
// own behaviour — the deletes, the lock, the races — is in
// store_sql_forget_test.go against the real database.
package checks_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/checks"
)

// fakeStore's forget methods exist so *fakeStore keeps satisfying
// checks.Store; the forget tests below use forgetStore instead.
func (f *fakeStore) ForgetItem(context.Context, checks.ItemRef, checks.ForgetSteps) (bool, error) {
	return false, f.err
}

func (f *fakeStore) ExpiredItems(context.Context, time.Time, time.Duration, int) ([]checks.ItemRef, error) {
	return nil, f.err
}

// forgetStore is a fake store for the forget tests: it records what the
// service asked for and plays the store's part of ForgetSteps — Proceed
// with the newest readings it was given for the item, then Within with a
// nil transaction — exactly in the order SQLStore does.
type forgetStore struct {
	*fakeStore
	expired []checks.ItemRef
	newest  map[checks.ItemRef]checks.ItemNewest
	exists  map[checks.ItemRef]bool
	fail    map[checks.ItemRef]error

	expiredCall struct {
		cutoff time.Time
		period time.Duration
		limit  int
	}
	forgot []checks.ItemRef
}

func newForgetStore() *forgetStore {
	return &forgetStore{
		fakeStore: newFakeStore(),
		newest:    map[checks.ItemRef]checks.ItemNewest{},
		exists:    map[checks.ItemRef]bool{},
		fail:      map[checks.ItemRef]error{},
	}
}

func (f *forgetStore) ExpiredItems(_ context.Context, cutoff time.Time, period time.Duration, limit int) ([]checks.ItemRef, error) {
	f.expiredCall.cutoff, f.expiredCall.period, f.expiredCall.limit = cutoff, period, limit
	return f.expired, nil
}

func (f *forgetStore) ForgetItem(_ context.Context, ref checks.ItemRef, steps checks.ForgetSteps) (bool, error) {
	if err := f.fail[ref]; err != nil {
		return false, err
	}
	if steps.Proceed != nil && !steps.Proceed(f.newest[ref]) {
		return false, nil
	}
	if !f.exists[ref] {
		return false, nil
	}
	if err := steps.Within(nil); err != nil {
		return false, err
	}
	f.forgot = append(f.forgot, ref)
	return true, nil
}

// closeCall is one CloseForgotten call the fake closer saw.
type closeCall struct {
	ref checks.ItemRef
	now time.Time
}

type fakeCloser struct {
	calls []closeCall
	err   error
}

func (c *fakeCloser) CloseForgotten(_ context.Context, _ pgx.Tx, orgID, hostID uuid.UUID, checkKey, item string, now time.Time) (int, error) {
	c.calls = append(c.calls, closeCall{ref: checks.ItemRef{OrgID: orgID, HostID: hostID, CheckKey: checkKey, Item: item}, now: now})
	return 1, c.err
}

func TestForgetItemClosesTheItemsAlertsInsideTheForget(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	ref := checks.ItemRef{OrgID: uuid.New(), HostID: uuid.New(), CheckKey: "disk", Item: "/mnt/old"}
	st := newForgetStore()
	st.exists[ref] = true
	closer := &fakeCloser{}
	svc := checks.NewService(st, fixedClock(now), checks.WithAlertCloser(closer))

	if err := svc.ForgetItem(ctx, ref.OrgID, ref.HostID, ref.CheckKey, ref.Item); err != nil {
		t.Fatalf("forget item: %v", err)
	}
	if len(st.forgot) != 1 || st.forgot[0] != ref {
		t.Fatalf("store forgot %+v, want exactly %+v", st.forgot, ref)
	}
	if len(closer.calls) != 1 || closer.calls[0] != (closeCall{ref: ref, now: now}) {
		t.Fatalf("closer calls = %+v, want one for %+v at %v", closer.calls, ref, now)
	}
}

func TestForgetItemWholeHostItem(t *testing.T) {
	ref := checks.ItemRef{OrgID: uuid.New(), HostID: uuid.New(), CheckKey: "cpu", Item: ""}
	st := newForgetStore()
	st.exists[ref] = true
	svc := checks.NewService(st, checks.WithAlertCloser(&fakeCloser{}))

	if err := svc.ForgetItem(ctx, ref.OrgID, ref.HostID, ref.CheckKey, ""); err != nil {
		t.Fatalf("forget the whole-host item: %v", err)
	}
	if len(st.forgot) != 1 || st.forgot[0] != ref {
		t.Fatalf("store forgot %+v, want %+v", st.forgot, ref)
	}
}

func TestForgetItemRefusals(t *testing.T) {
	org, host := uuid.New(), uuid.New()
	storeErr := errors.New("connection reset")
	closeErr := errors.New("alerts unavailable")
	tests := []struct {
		name     string
		closer   checks.AlertCloser
		checkKey string
		setup    func(st *forgetStore, ref checks.ItemRef)
		want     error
	}{
		{name: "no current row is not found", closer: &fakeCloser{}, checkKey: "disk", want: checks.ErrNotFound},
		{name: "empty check_key is invalid", closer: &fakeCloser{}, checkKey: "", want: checks.ErrInvalidInput},
		{name: "a store failure propagates", closer: &fakeCloser{}, checkKey: "disk",
			setup: func(st *forgetStore, ref checks.ItemRef) { st.fail[ref] = storeErr }, want: storeErr},
		{name: "an alert close failure propagates", closer: &fakeCloser{err: closeErr}, checkKey: "disk",
			setup: func(st *forgetStore, ref checks.ItemRef) { st.exists[ref] = true }, want: closeErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := checks.ItemRef{OrgID: org, HostID: host, CheckKey: tc.checkKey, Item: "/"}
			st := newForgetStore()
			if tc.setup != nil {
				tc.setup(st, ref)
			}
			svc := checks.NewService(st, checks.WithAlertCloser(tc.closer))
			err := svc.ForgetItem(ctx, org, host, tc.checkKey, "/")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestForgetWithoutAlertCloserRefuses: a Service wired without the alert
// port must not forget an item and leave its alerts open for ever.
func TestForgetWithoutAlertCloserRefuses(t *testing.T) {
	st := newForgetStore()
	ref := checks.ItemRef{OrgID: uuid.New(), HostID: uuid.New(), CheckKey: "disk", Item: "/"}
	st.exists[ref] = true
	st.expired = []checks.ItemRef{ref}
	svc := checks.NewService(st)

	if err := svc.ForgetItem(ctx, ref.OrgID, ref.HostID, ref.CheckKey, ref.Item); err == nil {
		t.Fatal("ForgetItem without an alert closer succeeded")
	}
	if _, err := svc.ForgetExpired(ctx, time.Hour, 10); err == nil {
		t.Fatal("ForgetExpired without an alert closer succeeded")
	}
	if len(st.forgot) != 0 {
		t.Fatalf("store forgot %+v without an alert closer", st.forgot)
	}
}

// TestForgetExpiredCountsPerOrganization: the selection is asked for
// now - period, the period and the limit; each selected item is forgotten
// with the alert close at the same now; the counts are per organization,
// and an item the re-check finds no longer due is not counted.
func TestForgetExpiredCountsPerOrganization(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	period := 168 * time.Hour
	orgA, orgB := uuid.New(), uuid.New()
	old := checks.ItemNewest{Rows: 2, Item: now.Add(-period - time.Hour), Host: now}
	a1 := checks.ItemRef{OrgID: orgA, HostID: uuid.New(), CheckKey: "docker", Item: "build-1"}
	a2 := checks.ItemRef{OrgID: orgA, HostID: uuid.New(), CheckKey: "docker", Item: "build-2"}
	b1 := checks.ItemRef{OrgID: orgB, HostID: uuid.New(), CheckKey: "disk", Item: ""}
	back := checks.ItemRef{OrgID: orgB, HostID: uuid.New(), CheckKey: "disk", Item: "/back"}

	st := newForgetStore()
	st.expired = []checks.ItemRef{a1, a2, b1, back}
	for _, r := range st.expired {
		st.exists[r] = true
		st.newest[r] = old
	}
	// back reported again between the selection and its forget.
	st.newest[back] = checks.ItemNewest{Rows: 2, Item: now, Host: now}
	closer := &fakeCloser{}
	svc := checks.NewService(st, fixedClock(now), checks.WithAlertCloser(closer))

	counts, err := svc.ForgetExpired(ctx, period, 50)
	if err != nil {
		t.Fatalf("forget expired: %v", err)
	}
	if want := map[uuid.UUID]int{orgA: 2, orgB: 1}; len(counts) != len(want) || counts[orgA] != 2 || counts[orgB] != 1 {
		t.Fatalf("counts = %v, want %v", counts, want)
	}
	if c := st.expiredCall; !c.cutoff.Equal(now.Add(-period)) || c.period != period || c.limit != 50 {
		t.Fatalf("selection asked for %+v, want cutoff %v, period %v, limit 50", c, now.Add(-period), period)
	}
	if len(closer.calls) != 3 {
		t.Fatalf("closer called %d times, want 3 (not for the item that came back)", len(closer.calls))
	}
	for _, c := range closer.calls {
		if !c.now.Equal(now) || c.ref == back {
			t.Fatalf("closer call %+v, want now %v and never the item that came back", c, now)
		}
	}
}

// TestForgetExpiredKeepsGoingPastAFailure: one broken item must not starve
// every item behind it; the error names the failure, the counts what did
// succeed.
func TestForgetExpiredKeepsGoingPastAFailure(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	org := uuid.New()
	broken := checks.ItemRef{OrgID: org, HostID: uuid.New(), CheckKey: "docker", Item: "broken"}
	fine := checks.ItemRef{OrgID: org, HostID: uuid.New(), CheckKey: "docker", Item: "fine"}
	boom := errors.New("boom")
	st := newForgetStore()
	st.expired = []checks.ItemRef{broken, fine}
	st.fail[broken] = boom
	st.exists[fine] = true
	st.newest[fine] = checks.ItemNewest{Rows: 1, Item: now.Add(-200 * time.Hour), Host: now}
	svc := checks.NewService(st, fixedClock(now), checks.WithAlertCloser(&fakeCloser{}))

	counts, err := svc.ForgetExpired(ctx, 168*time.Hour, 10)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the failure", err)
	}
	if counts[org] != 1 {
		t.Fatalf("counts = %v, want the item behind the failure forgotten", counts)
	}
}

func TestForgetExpiredRefusesNonPositiveArguments(t *testing.T) {
	svc := checks.NewService(newForgetStore(), checks.WithAlertCloser(&fakeCloser{}))
	for _, tc := range []struct {
		period time.Duration
		limit  int
	}{{0, 10}, {-time.Hour, 10}, {time.Hour, 0}, {time.Hour, -1}} {
		if _, err := svc.ForgetExpired(ctx, tc.period, tc.limit); !errors.Is(err, checks.ErrInvalidInput) {
			t.Errorf("ForgetExpired(%v, %d) = %v, want ErrInvalidInput", tc.period, tc.limit, err)
		}
	}
}

// TestForgetExpiredRecheckBoundaries is the expiry rule the re-check
// inside each forget applies, at its boundaries — the same ones
// TestExpiredItemsBoundaries pins on the store's selection.
func TestForgetExpiredRecheckBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	period := 168 * time.Hour
	cutoff := now.Add(-period)
	steady := now.Add(-60 * 24 * time.Hour) // an agent connected for long before any of this
	tests := []struct {
		name   string
		newest checks.ItemNewest
		want   bool
	}{
		{"exactly at the period is not yet due", checks.ItemNewest{Rows: 1, Item: cutoff, Host: now}, false},
		{"a second past the period is due", checks.ItemNewest{Rows: 1, Item: cutoff.Add(-time.Second), Host: now}, true},
		// A host newest in the future satisfies the second half either way,
		// so only the first half decides.
		{"first half alone, exactly at the period", checks.ItemNewest{Rows: 1, Item: cutoff, Host: now.Add(time.Hour)}, false},
		{"first half alone, a second past it", checks.ItemNewest{Rows: 1, Item: cutoff.Add(-time.Second), Host: now.Add(time.Hour)}, true},
		{"host newest exactly item + period is not due",
			checks.ItemNewest{Rows: 1, Item: cutoff.Add(-time.Hour), Host: cutoff.Add(-time.Hour).Add(period)}, false},
		{"host newest a second later is due",
			checks.ItemNewest{Rows: 1, Item: cutoff.Add(-time.Hour), Host: cutoff.Add(-time.Hour).Add(period + time.Second)}, true},
		{"offline host: its newest is the item's own", checks.ItemNewest{Rows: 1, Item: now.Add(-30 * 24 * time.Hour), Host: now.Add(-30 * 24 * time.Hour)}, false},
		{"no rows left", checks.ItemNewest{Rows: 0, Item: time.Unix(0, 0), Host: now}, false},
		// A host with an agent: its other checks' readings (the server's
		// probes) do not count, its agent's heartbeat and the item's own
		// check key do.
		{"offline agent, fresh probe elsewhere",
			checks.ItemNewest{Rows: 1, Item: cutoff.Add(-time.Hour), Key: cutoff.Add(-time.Hour), Host: now, HasAgent: true, AgentSeen: cutoff.Add(-time.Hour)}, false},
		{"agent never seen, fresh probe elsewhere",
			checks.ItemNewest{Rows: 1, Item: cutoff.Add(-time.Hour), Key: cutoff.Add(-time.Hour), Host: now, HasAgent: true}, false},
		{"agent seen exactly item + period is not due",
			checks.ItemNewest{Rows: 1, Item: cutoff.Add(-time.Hour), Key: cutoff.Add(-time.Hour), Host: now, HasAgent: true, AgentSeen: cutoff.Add(-time.Hour).Add(period), AgentConnected: steady}, false},
		{"agent seen a second later is due",
			checks.ItemNewest{Rows: 1, Item: cutoff.Add(-time.Hour), Key: cutoff.Add(-time.Hour), Host: now, HasAgent: true, AgentSeen: cutoff.Add(-time.Hour).Add(period + time.Second), AgentConnected: steady}, true},
		{"offline agent, a sibling item of the same check key still reports",
			checks.ItemNewest{Rows: 1, Item: cutoff.Add(-time.Hour), Key: now, Host: now, HasAgent: true, AgentSeen: cutoff.Add(-time.Hour)}, true},
		{"agentless: the same check key exactly item + period is not due, the host decides",
			checks.ItemNewest{Rows: 1, Item: cutoff.Add(-time.Hour), Key: cutoff.Add(-time.Hour).Add(period), Host: cutoff.Add(-time.Hour).Add(period)}, false},
		{"still due only when the item itself is past the period",
			checks.ItemNewest{Rows: 1, Item: cutoff, Key: now, Host: now, HasAgent: true, AgentSeen: now, AgentConnected: steady}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := checks.ItemRef{OrgID: uuid.New(), HostID: uuid.New(), CheckKey: "docker", Item: "c"}
			st := newForgetStore()
			st.expired = []checks.ItemRef{ref}
			st.exists[ref] = true
			st.newest[ref] = tc.newest
			svc := checks.NewService(st, fixedClock(now), checks.WithAlertCloser(&fakeCloser{}))

			counts, err := svc.ForgetExpired(ctx, period, 10)
			if err != nil {
				t.Fatal(err)
			}
			if got := counts[ref.OrgID] == 1; got != tc.want {
				t.Fatalf("forgotten = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestForgetExpiredAgentCountsAfterADayConnected (backlog 3.12c): an
// agent back from an outage heartbeats at once, before a check with a long
// interval has run again, so its heartbeat says nothing about that check's
// items until it has been connected for checks.MaxInterval plus a
// 30-minute margin — every check has reported at least once by then, the
// first collect of a 24 h check landing one full interval after its
// ticker starts, a little after the connection. Before that the agent
// contributes nothing; the item's own check key still does. An agent
// whose connection start is unknown (zero or the epoch: never seen since
// the upgrade, or re-enrolled) contributes nothing either.
func TestForgetExpiredAgentCountsAfterADayConnected(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	period := 48 * time.Hour
	gone := now.Add(-period - 72*time.Hour) // the item's newest reading, long before the outage ended
	countsAfter := checks.MaxInterval + 30*time.Minute
	agent := func(connected time.Time) checks.ItemNewest {
		return checks.ItemNewest{Rows: 1, Item: gone, Key: gone, Host: now, HasAgent: true, AgentSeen: now, AgentConnected: connected}
	}
	tests := []struct {
		name   string
		newest checks.ItemNewest
		want   bool
	}{
		{"reconnected a minute ago", agent(now.Add(-time.Minute)), false},
		{"connected exactly MaxInterval: the first 24 h collect may not have landed", agent(now.Add(-checks.MaxInterval)), false},
		{"connected a second short of MaxInterval + 30 min", agent(now.Add(-countsAfter + time.Second)), false},
		{"connected exactly MaxInterval + 30 min", agent(now.Add(-countsAfter)), true},
		{"connected for a month", agent(now.Add(-30 * 24 * time.Hour)), true},
		{"connection start unknown (zero)", agent(time.Time{}), false},
		{"connection start unknown (epoch)", agent(time.Unix(0, 0).UTC()), false},
		{"reconnected, but a sibling item of the same check key still reports",
			checks.ItemNewest{Rows: 1, Item: gone, Key: now, Host: now, HasAgent: true, AgentSeen: now, AgentConnected: now.Add(-time.Minute)}, true},
		{"no agent: the host's newest reading decides as before",
			checks.ItemNewest{Rows: 1, Item: gone, Key: gone, Host: now}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := checks.ItemRef{OrgID: uuid.New(), HostID: uuid.New(), CheckKey: "backup", Item: "nightly"}
			st := newForgetStore()
			st.expired = []checks.ItemRef{ref}
			st.exists[ref] = true
			st.newest[ref] = tc.newest
			svc := checks.NewService(st, fixedClock(now), checks.WithAlertCloser(&fakeCloser{}))

			counts, err := svc.ForgetExpired(ctx, period, 10)
			if err != nil {
				t.Fatal(err)
			}
			if got := counts[ref.OrgID] == 1; got != tc.want {
				t.Fatalf("forgotten = %v, want %v", got, tc.want)
			}
		})
	}
}
