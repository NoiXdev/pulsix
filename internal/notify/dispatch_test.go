package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/notify"
)

type outcome struct {
	kind    string // complete, retry, fail
	next    time.Time
	errText string
}

// fakeQueue hands out its pending deliveries once and records outcomes.
type fakeQueue struct {
	mu       sync.Mutex
	pending  []notify.Claimed
	outcomes map[uuid.UUID]outcome
	claimErr error
}

func (q *fakeQueue) Claim(_ context.Context, _ time.Time, _ time.Duration, limit int) ([]notify.Claimed, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claimErr != nil {
		return nil, q.claimErr
	}
	n := min(limit, len(q.pending))
	out := q.pending[:n]
	q.pending = q.pending[n:]
	return out, nil
}

func (q *fakeQueue) record(c notify.Claimed, o outcome) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.outcomes == nil {
		q.outcomes = map[uuid.UUID]outcome{}
	}
	q.outcomes[c.Delivery.ID] = o
	return nil
}

func (q *fakeQueue) Complete(_ context.Context, c notify.Claimed, _ time.Time) error {
	return q.record(c, outcome{kind: "complete"})
}

func (q *fakeQueue) Retry(_ context.Context, c notify.Claimed, next time.Time, errText string) error {
	return q.record(c, outcome{kind: "retry", next: next, errText: errText})
}

func (q *fakeQueue) Fail(_ context.Context, c notify.Claimed, errText string) error {
	return q.record(c, outcome{kind: "fail", errText: errText})
}

// scriptedSender answers per delivery id.
type scriptedSender struct {
	mu      sync.Mutex
	answers map[uuid.UUID]error
	panics  map[uuid.UUID]bool
	calls   int
}

func (s *scriptedSender) Send(_ context.Context, _ notify.Channel, id uuid.UUID, _ notify.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.panics[id] {
		panic("boom")
	}
	return s.answers[id]
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func claimed(attempts int, enabled bool, payload string) notify.Claimed {
	return notify.Claimed{
		Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7()), OrgID: uuid.Must(uuid.NewV7()), Attempts: attempts, Payload: json.RawMessage(payload)},
		Channel:  notify.Channel{Enabled: enabled, Kind: notify.KindWebhook},
	}
}

func TestDispatcherOutcomes(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	ok := claimed(1, true, `{"kind":"test"}`)
	transient := claimed(2, true, `{"kind":"test"}`)
	lastTry := claimed(notify.MaxAttempts, true, `{"kind":"test"}`)
	permanent := claimed(1, true, `{"kind":"test"}`)
	disabled := claimed(1, false, `{"kind":"test"}`)
	broken := claimed(1, true, `{`)
	panicky := claimed(1, true, `{"kind":"test"}`)
	q := &fakeQueue{pending: []notify.Claimed{ok, transient, lastTry, permanent, disabled, broken, panicky}}
	s := &scriptedSender{
		answers: map[uuid.UUID]error{
			transient.Delivery.ID: errors.New("the endpoint answered 503"),
			lastTry.Delivery.ID:   errors.New("the endpoint answered 503"),
			permanent.Delivery.ID: notify.Permanent(errors.New("the endpoint answered 400")),
		},
		panics: map[uuid.UUID]bool{panicky.Delivery.ID: true},
	}
	n, err := notify.NewDispatcher(q, s, time.Second, func() time.Time { return now }, nil, quiet()).Tick(ctx)
	if err != nil || n != 7 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	want := map[uuid.UUID]string{
		ok.Delivery.ID: "complete", transient.Delivery.ID: "retry", lastTry.Delivery.ID: "fail",
		permanent.Delivery.ID: "fail", disabled.Delivery.ID: "fail", broken.Delivery.ID: "fail", panicky.Delivery.ID: "retry",
	}
	for id, kind := range want {
		if got := q.outcomes[id]; got.kind != kind {
			t.Errorf("delivery %s: %+v, want %s", id, got, kind)
		}
	}
	// Attempt 2 failed: the next try is 2 minutes later (30 s × 4).
	if got := q.outcomes[transient.Delivery.ID]; !got.next.Equal(now.Add(2*time.Minute)) || got.errText != "the endpoint answered 503" {
		t.Fatalf("retry = %+v", got)
	}
	if got := q.outcomes[disabled.Delivery.ID]; got.errText != "the channel is disabled" {
		t.Fatalf("disabled = %+v", got)
	}
	if s.calls != 5 {
		t.Fatalf("sender calls = %d; a disabled channel and an undecodable message are never sent", s.calls)
	}
}

func TestDispatcherClaimsUntilABatchIsShort(t *testing.T) {
	var pending []notify.Claimed
	for i := 0; i < 20; i++ {
		pending = append(pending, claimed(1, true, `{"kind":"test"}`))
	}
	q := &fakeQueue{pending: pending}
	n, err := notify.NewDispatcher(q, &scriptedSender{}, time.Second, nil, nil, nil).Tick(ctx)
	if err != nil || n != 20 || len(q.outcomes) != 20 {
		t.Fatalf("Tick = %d, %v; outcomes %d", n, err, len(q.outcomes))
	}
	q.claimErr = errors.New("db down")
	if _, err := notify.NewDispatcher(q, &scriptedSender{}, time.Second, nil, nil, nil).Tick(ctx); err == nil {
		t.Fatal("a claim failure must surface")
	}
}

func TestDispatcherRunStopsWithTheContext(t *testing.T) {
	cctx, cancel := context.WithCancel(ctx)
	ticks := 0
	sleep := func(context.Context, time.Duration) error {
		ticks++
		if ticks == 3 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	notify.NewDispatcher(&fakeQueue{}, &scriptedSender{}, time.Second, nil, sleep, nil).Run(cctx)
	if ticks != 3 {
		t.Fatalf("ticks = %d", ticks)
	}
}
