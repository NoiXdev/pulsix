package notify_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/notify"
)

// countingSender counts sends per delivery id.
type countingSender struct {
	mu    sync.Mutex
	sends map[uuid.UUID]int
	err   error
}

func (c *countingSender) Send(_ context.Context, _ notify.Channel, id uuid.UUID, _ notify.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sends == nil {
		c.sends = map[uuid.UUID]int{}
	}
	c.sends[id]++
	return c.err
}

func seedChannel(t *testing.T, d *db.DB, org uuid.UUID) notify.Channel {
	t.Helper()
	cfg, _ := json.Marshal(notify.EmailConfig{Recipients: []string{"ops@example.test"}})
	c, err := notify.NewSQLStore(d).CreateChannel(db.WithOrgScope(ctx, org), notify.Channel{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: notify.KindEmail, Name: "ops " + uuid.NewString()[:6], Enabled: true, Config: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func queueDelivery(t *testing.T, d *db.DB, c notify.Channel, key string, at time.Time) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if err := notify.NewSQLStore(d).InsertDelivery(db.WithOrgScope(ctx, c.OrgID), notify.Delivery{
		ID: id, OrgID: c.OrgID, ChannelID: c.ID, EventKey: key, EventKind: notify.EventTest,
		Payload: json.RawMessage(`{"kind":"test"}`), Status: notify.DeliveryPending, NextAttemptAt: at, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func deliveryRow(t *testing.T, d *db.DB, id uuid.UUID) (status string, attempts int, lastError string, next time.Time) {
	t.Helper()
	if err := d.Pool().QueryRow(ctx, `SELECT status, attempts, last_error, next_attempt_at FROM notification_deliveries WHERE id = $1`, id).
		Scan(&status, &attempts, &lastError, &next); err != nil {
		t.Fatal(err)
	}
	return
}

// TestTwoDispatchersSendEachDeliveryOnce: 30 due deliveries across two
// organizations, two dispatchers ticking concurrently over two stores —
// every delivery is sent exactly once and recorded sent.
func TestTwoDispatchersSendEachDeliveryOnce(t *testing.T) {
	d := testdb.Open(t)
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	var ids []uuid.UUID
	for _, slug := range []string{"dispatch-a", "dispatch-b"} {
		ch := seedChannel(t, d, seedOrg(t, d, slug))
		for i := 0; i < 15; i++ {
			ids = append(ids, queueDelivery(t, d, ch, "test:"+uuid.NewString(), now.Add(-time.Minute)))
		}
	}
	sender := &countingSender{}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		disp := notify.NewDispatcher(notify.NewDeliverySQLStore(d), sender, time.Second, func() time.Time { return now }, nil, nil)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := disp.Tick(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if sender.sends[id] != 1 {
			t.Fatalf("delivery %s sent %d times", id, sender.sends[id])
		}
		if status, attempts, _, _ := deliveryRow(t, d, id); status != "sent" || attempts != 1 {
			t.Fatalf("delivery %s = %s after %d attempts", id, status, attempts)
		}
	}
}

func TestDeliveryBackoffAndFinalFailure(t *testing.T) {
	d := testdb.Open(t)
	ch := seedChannel(t, d, seedOrg(t, d, "dispatch-retry"))
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	id := queueDelivery(t, d, ch, "test:retry", now)
	sender := &countingSender{err: context.DeadlineExceeded}
	clock := now
	disp := notify.NewDispatcher(notify.NewDeliverySQLStore(d), sender, time.Second, func() time.Time { return clock }, nil, nil)
	delays := []time.Duration{30 * time.Second, 2 * time.Minute, 8 * time.Minute, 32 * time.Minute, 128 * time.Minute}
	for attempt := 1; attempt <= notify.MaxAttempts; attempt++ {
		if _, err := disp.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		status, attempts, lastErr, next := deliveryRow(t, d, id)
		if attempts != attempt || lastErr != context.DeadlineExceeded.Error() {
			t.Fatalf("attempt %d: attempts %d, last error %q", attempt, attempts, lastErr)
		}
		if attempt < notify.MaxAttempts {
			if status != "pending" || !next.Equal(clock.Add(delays[attempt-1])) {
				t.Fatalf("attempt %d: %s, next %s", attempt, status, next)
			}
			// Not due yet: a tick right now claims nothing.
			if n, _ := disp.Tick(ctx); n != 0 {
				t.Fatalf("attempt %d: a delivery was claimed before its backoff ended", attempt)
			}
			clock = next
		} else if status != "failed" {
			t.Fatalf("after %d attempts: %s, want failed", attempt, status)
		}
	}
}

// TestExpiredLeaseIsReclaimedAndALateCompletionIsFenced: a manager that
// claimed a row and vanished leaves it pending until the lease ends; the
// next claim takes it (attempts 2), and the first manager's late
// completion changes nothing.
func TestExpiredLeaseIsReclaimedAndALateCompletionIsFenced(t *testing.T) {
	d := testdb.Open(t)
	ch := seedChannel(t, d, seedOrg(t, d, "dispatch-lease"))
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	id := queueDelivery(t, d, ch, "test:lease", now)
	st := notify.NewDeliverySQLStore(d)
	first, err := st.Claim(ctx, now, 2*time.Minute, 8)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim = %v, %v", first, err)
	}
	if again, _ := st.Claim(ctx, now.Add(time.Minute), 2*time.Minute, 8); len(again) != 0 {
		t.Fatal("a leased row was claimed twice")
	}
	second, err := st.Claim(ctx, now.Add(3*time.Minute), 2*time.Minute, 8)
	if err != nil || len(second) != 1 || second[0].Delivery.Attempts != 2 {
		t.Fatalf("claim after the lease = %+v, %v", second, err)
	}
	if err := st.Complete(ctx, first[0], now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if status, _, _, _ := deliveryRow(t, d, id); status != "pending" {
		t.Fatalf("a fenced completion changed the row to %s", status)
	}
	if err := st.Complete(ctx, second[0], now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if status, _, _, _ := deliveryRow(t, d, id); status != "sent" {
		t.Fatalf("status = %s, want sent", status)
	}
}

func TestDeliveryLogKeepsTheNewest500(t *testing.T) {
	d := testdb.Open(t)
	ch := seedChannel(t, d, seedOrg(t, d, "dispatch-trim"))
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	if _, err := d.Pool().Exec(ctx, `INSERT INTO notification_deliveries (id, org_id, channel_id, event_key, event_kind, payload, status, attempts, next_attempt_at, created_at)
		SELECT gen_random_uuid(), $1, $2, 'old:' || g, 'test', '{}', 'sent', 1, $3::timestamptz, $3::timestamptz - g * interval '1 second' FROM generate_series(1, 505) AS g`,
		ch.OrgID, ch.ID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	queueDelivery(t, d, ch, "test:new", now)
	if _, err := notify.NewDispatcher(notify.NewDeliverySQLStore(d), &countingSender{}, time.Second, func() time.Time { return now }, nil, nil).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE channel_id = $1`, ch.ID).Scan(&n); err != nil || n != 500 {
		t.Fatalf("rows = %d, %v; want 500", n, err)
	}
	list, err := notify.NewSQLStore(d).ListDeliveries(db.WithOrgScope(ctx, ch.OrgID), ch.OrgID, ch.ID, 1)
	if err != nil || len(list) != 1 || list[0].EventKey != "test:new" || list[0].Status != notify.DeliverySent {
		t.Fatalf("newest = %+v, %v", list, err)
	}
}

func TestInsertDeliveryIsIdempotentPerEventAndChannel(t *testing.T) {
	d := testdb.Open(t)
	ch := seedChannel(t, d, seedOrg(t, d, "dispatch-idem"))
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	queueDelivery(t, d, ch, "run:same", now)
	queueDelivery(t, d, ch, "run:same", now)
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE event_key = 'run:same'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d, %v; the event key must dedupe", n, err)
	}
}

// TestClaimHasAnAttemptsBackstop is the final review's Important #6 /
// minor 26. ClaimNotificationDeliveries had no attempts filter at all, and
// the Go-side MaxAttempts cap is consulted only when the *send* returned
// an error. A row whose outcome write never landed — a manager that died
// after the receiver accepted the message, or whose outcome write panicked
// — therefore became due again when its lease expired and was re-claimed
// with no cap whatever: the same message re-sent to the receiver every two
// minutes, forever. The SQL-side backstop bounds that loop even when no
// Go-side decision is ever reached.
//
// It is deliberately far above MaxAttempts: the Go side still owns the
// real cap and its error text, and this only stops an unbounded loop.
func TestClaimHasAnAttemptsBackstop(t *testing.T) {
	d := testdb.Open(t)
	ch := seedChannel(t, d, seedOrg(t, d, "dispatch-backstop"))
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	below := queueDelivery(t, d, ch, "test:below-the-backstop", now.Add(-time.Minute))
	at := queueDelivery(t, d, ch, "test:at-the-backstop", now.Add(-time.Minute))
	// Both leases have expired, so without a cap both are claimable.
	if _, err := d.Pool().Exec(ctx, `UPDATE notification_deliveries SET attempts = 19, locked_until = $2 WHERE id = $1`, below, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE notification_deliveries SET attempts = 20, locked_until = $2 WHERE id = $1`, at, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := notify.NewDeliverySQLStore(d).Claim(ctx, now, 2*time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Delivery.ID != below {
		ids := make([]uuid.UUID, len(got))
		for i, c := range got {
			ids[i] = c.Delivery.ID
		}
		t.Fatalf("claimed %v, want only the row below the backstop (%s)", ids, below)
	}
}
