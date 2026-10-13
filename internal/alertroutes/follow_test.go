// The follower's own tests: coalescing, idempotency, the catch-up pass and
// the one transaction the ledger write and the enqueue share. The fixture
// they all run on — followEnv and its helpers — lives in
// follow_fixture_test.go, and the delivery-window and rotation gates have
// their own file in follow_window_test.go.
package alertroutes_test

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/rules"
)

func TestTickCoalescesPerChannel(t *testing.T) {
	// Decision 5: one message per channel per tick, listing what
	// happened — not one message per event.
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	for range 3 {
		env.openCritAlert()
	}

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Fatalf("Enqueue called %d times, want 1 — one message per channel", len(env.enq.calls))
	}
	if env.enq.calls[0].count != 3 {
		t.Errorf("batch carried %d alerts, want 3", env.enq.calls[0].count)
	}
}

// TestTickCapsTheItemListButNotTheCount is the storm case decision 5
// names outright — "fifty hosts filling their disks" — and the cap sits
// exactly there, so the first batch that trips it is the worst possible
// one to get wrong. The message carries at most MaxBatchItems items, but
// Count stays the number of alerts the batch actually decided: that
// difference is the only thing telling a renderer the list was cut
// (notify.alertTruncationNote turns it into "… and N more").
func TestTickCapsTheItemListButNotTheCount(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	const over = alertroutes.MaxBatchItems + 7
	for range over {
		env.openCritAlert()
	}

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Fatalf("Enqueue called %d times, want 1", len(env.enq.calls))
	}
	if got := env.enq.calls[0].items; got != alertroutes.MaxBatchItems {
		t.Errorf("message listed %d items, want the cap of %d", got, alertroutes.MaxBatchItems)
	}
	if got := env.enq.calls[0].count; got != over {
		t.Errorf("batch count = %d, want %d — the number before the cut, not after it", got, over)
	}
}

func TestTickIsIdempotent(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	env.openCritAlert()

	for range 2 {
		if err := env.follower.Tick(context.Background(), env.org); err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	if len(env.enq.calls) != 1 {
		t.Errorf("Enqueue called %d times across two ticks, want 1", len(env.enq.calls))
	}
}

func TestSuppressedIsCaughtUpWhenTheWindowEnds(t *testing.T) {
	// Decision 4. The alert opened inside a maintenance window and is
	// still open after it — the case that must not be swallowed.
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	window := env.maintenanceWindow(env.host, env.clock.Now().Add(-time.Hour), env.clock.Now().Add(time.Hour))
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick during maintenance: %v", err)
	}
	if len(env.enq.calls) != 0 {
		t.Fatalf("a suppressed alert was delivered: %d calls", len(env.enq.calls))
	}

	env.endWindow(window)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick after maintenance: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Errorf("Enqueue called %d times after the window ended, want 1", len(env.enq.calls))
	}
}

func TestUnmatchedEventIsRecordedNotLeftPending(t *testing.T) {
	// Review Focus 3. No route wants this kind, so nothing is sent — but
	// the event must be decided, or the anti-join re-examines it forever.
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventClosed}, []uuid.UUID{env.channel})
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 0 {
		t.Errorf("Enqueue called %d times, want 0", len(env.enq.calls))
	}
	if got := env.ledgerState(); got != alertroutes.StateUnrouted {
		t.Errorf("ledger state = %q, want %q", got, alertroutes.StateUnrouted)
	}
}

func TestTickFailureLeavesEverythingUndecided(t *testing.T) {
	// Review Focus 5 and the transaction rule from the spec's "The
	// follower": recording `sent` without the delivery existing would
	// drop an alert silently and forever, because the anti-join never
	// looks at a decided event again.
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	env.openCritAlert()
	env.enq.fail = errors.New("channel vanished mid-tick")

	if err := env.follower.Tick(context.Background(), env.org); err == nil {
		t.Fatal("Tick() = nil, want the enqueue failure reported")
	}
	if got := env.ledgerState(); got != "" {
		t.Errorf("ledger state = %q after a failed tick, want no row at all", got)
	}

	env.enq.fail = nil
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick after recovery: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Errorf("Enqueue called %d times after recovery, want 1 — the event was not lost", len(env.enq.calls))
	}
}

func TestConcurrentTicksSendOnce(t *testing.T) {
	// Review Focus 4. Two managers, one organization, one message.
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	env.openCritAlert()

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = env.follower.Tick(context.Background(), env.org)
		}()
	}
	wg.Wait()
	if len(env.enq.calls) != 1 {
		t.Errorf("Enqueue called %d times across four concurrent ticks, want 1", len(env.enq.calls))
	}
}

// TestBatchKeyIsDerivedFromItsEventsNotTheClock is the review's own
// scenario, with the real outbox behind it rather than the recording
// fake: two managers' ticks land inside the same wall-clock second — the
// injected clock is frozen, so "the same second" is exact rather than
// hoped for — with new events committed in between. A key built from the
// tick's second would be identical for both, and
// InsertNotificationDelivery's ON CONFLICT (org_id, event_key,
// channel_id) DO NOTHING would then swallow the second batch WITHOUT AN
// ERROR while its events commit as 'sent' in the same transaction: the
// anti-join never looks at them again and nobody is ever told. The
// advisory lock cannot help — the two ticks are sequential, not
// concurrent.
func TestBatchKeyIsDerivedFromItsEventsNotTheClock(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	outbox := notify.NewOutbox(env.d, "https://pulsix.example", time.Now, rand.Reader, slog.Default())
	follower := alertroutes.NewFollower(env.d, env.store, noGroups{}, env.rotations, outbox,
		"https://pulsix.example", env.clock.Now, slog.Default())
	ctx := context.Background()

	env.openCritAlert()
	if err := follower.Tick(ctx, env.org); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	// A second manager's tick, the same second, over events that only
	// committed after the first tick had already read its working set.
	env.openCritAlert()
	if err := follower.Tick(ctx, env.org); err != nil {
		t.Fatalf("second Tick: %v", err)
	}

	if got := env.deliveries(); got != 2 {
		t.Errorf("notification_deliveries = %d, want 2 — the second tick's batch was swallowed by the first tick's key", got)
	}
}

// TestRunTicksEveryOrganizationUntilCancelled covers the loop itself: Run
// finds the organizations on its own — nothing hands it a tenant list —
// ticks each of them, and returns when its context is cancelled, which is
// precisely what internal/app's startLoop relies on to shut the follower
// down before it closes the pool.
func TestRunTicksEveryOrganizationUntilCancelled(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened}, []uuid.UUID{env.channel})
	env.openCritAlert()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.follower.Run(ctx, 10*time.Millisecond)
	}()

	deadline := time.Now().Add(30 * time.Second)
	for env.enq.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Run never delivered the pending alert")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
