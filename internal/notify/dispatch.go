package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Queue tuning (spec §5): at most MaxAttempts claims per message, then
// failed; a claim leases a row for leaseFor, far longer than one send can
// take (the webhook timeout is 10 s, an e-mail conversation 30 s), so a
// row is only claimed again when its manager died mid-send.
const (
	MaxAttempts             = 6
	DefaultDispatchInterval = 2 * time.Second
	dispatchBatch           = 8
	leaseFor                = 2 * time.Minute
	keepDeliveries          = 500
)

// retryDelay is the exponential backoff after failed attempt n (1-5):
// 30 s, 2 min, 8 min, 32 min, 2 h 8 min.
func retryDelay(attempt int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempt; i++ {
		d *= 4
	}
	return d
}

// Claimed is one leased delivery with its channel.
type Claimed struct {
	Delivery Delivery
	Channel  Channel
}

// DeliveryQueue is the bypass-scoped queue store the dispatcher drives.
type DeliveryQueue interface {
	Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Claimed, error)
	Complete(ctx context.Context, c Claimed, at time.Time) error
	Retry(ctx context.Context, c Claimed, next time.Time, errText string) error
	Fail(ctx context.Context, c Claimed, errText string) error
}

// Sender delivers one event through one channel (ChannelSender).
type Sender interface {
	Send(ctx context.Context, ch Channel, deliveryID uuid.UUID, ev Event) error
}

// Dispatcher sends queued deliveries. Several managers may run one each:
// the claim is FOR UPDATE SKIP LOCKED plus a lease. Delivery is
// at-least-once — a manager that dies after the receiver accepted a
// message but before recording it sent sends it again once the lease ends
// — and never more than once per (event, channel) enqueue, because the
// enqueue itself is idempotent (the unique event key).
type Dispatcher struct {
	queue    DeliveryQueue
	sender   Sender
	interval time.Duration
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
	log      *slog.Logger
}

// NewDispatcher wires the dispatcher; zero interval, nil now, sleep and
// log take their defaults.
func NewDispatcher(queue DeliveryQueue, sender Sender, interval time.Duration, now func() time.Time, sleep func(ctx context.Context, d time.Duration) error, log *slog.Logger) *Dispatcher {
	if interval <= 0 {
		interval = DefaultDispatchInterval
	}
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{queue: queue, sender: sender, interval: interval, now: now, sleep: sleep, log: log}
}

// Run dispatches until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		d.tickSafely(ctx)
		if err := d.sleep(ctx, d.interval); err != nil {
			return
		}
	}
}

// tickSafely runs one Tick, recovering any panic that escapes it — the
// same net DigestTicker.Run and schedules.Ticker.Run have had all along,
// and the one Dispatcher.Run was missing (final review, Important #6):
// a panic in the claim, or anywhere else in Tick's own body, otherwise
// took the dispatcher goroutine and with it the whole manager process
// down. The tick is skipped; the next one retries.
func (d *Dispatcher) tickSafely(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			d.log.ErrorContext(ctx, "notification dispatch tick panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if _, err := d.Tick(ctx); err != nil && ctx.Err() == nil {
		d.log.ErrorContext(ctx, "notification dispatch failed", "err", err)
	}
}

// Tick sends every due delivery, one claimed batch at a time (the batch in
// parallel), until a batch comes back smaller than dispatchBatch.
func (d *Dispatcher) Tick(ctx context.Context) (int, error) {
	total := 0
	for {
		claimed, err := d.queue.Claim(ctx, d.now(), leaseFor, dispatchBatch)
		if err != nil {
			return total, err
		}
		var wg sync.WaitGroup
		for _, c := range claimed {
			wg.Add(1)
			go func() {
				defer wg.Done()
				d.deliver(ctx, c)
			}()
		}
		wg.Wait()
		total += len(claimed)
		if len(claimed) < dispatchBatch || ctx.Err() != nil {
			return total, nil
		}
	}
}

// deliver sends one claimed delivery and records the outcome. A panic in
// a sender is recovered into a retry, like a transient error.
//
// The recover covers the *whole* function, not just the send (final
// review, Important #6): deliver runs in a goroutine Tick spawns per
// claimed delivery, so a panic in the outcome write below — Complete,
// Retry or Fail — could never be caught by any parent and killed the
// manager. Combined with the queue's missing SQL-side attempts cap that
// was an unbounded loop: crash, restart, re-claim the same row, re-send
// to the receiver, crash. There is nothing useful to do about such a
// panic but log it and leave the row to its lease.
func (d *Dispatcher) deliver(ctx context.Context, c Claimed) {
	defer func() {
		if r := recover(); r != nil {
			d.log.ErrorContext(ctx, "notification delivery panicked outside the sender", "delivery", c.Delivery.ID, "org", c.Delivery.OrgID, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	sendErr := d.send(ctx, c)
	// The outcome is recorded even when ctx ended meanwhile (shutdown):
	// the send already happened or not; the lease would otherwise make
	// another manager repeat a completed send.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var err error
	switch {
	case sendErr == nil:
		err = d.queue.Complete(wctx, c, d.now())
	case IsPermanent(sendErr) || c.Delivery.Attempts >= MaxAttempts:
		err = d.queue.Fail(wctx, c, errorText(sendErr))
	default:
		err = d.queue.Retry(wctx, c, d.now().Add(retryDelay(c.Delivery.Attempts)), errorText(sendErr))
	}
	if err != nil {
		d.log.ErrorContext(wctx, "record notification delivery", "delivery", c.Delivery.ID, "org", c.Delivery.OrgID, "err", err)
	}
}

func (d *Dispatcher) send(ctx context.Context, c Claimed) (err error) {
	defer func() {
		if r := recover(); r != nil {
			d.log.ErrorContext(ctx, "notification sender panicked", "delivery", c.Delivery.ID, "panic", r, "stack", string(debug.Stack()))
			err = errors.New("the sender failed unexpectedly (see the manager log)")
		}
	}()
	if !c.Channel.Enabled {
		return Permanent(errors.New("the channel is disabled"))
	}
	var ev Event
	if err := json.Unmarshal(c.Delivery.Payload, &ev); err != nil {
		return Permanent(errors.New("the stored message does not decode"))
	}
	return d.sender.Send(ctx, c.Channel, c.Delivery.ID, ev)
}
