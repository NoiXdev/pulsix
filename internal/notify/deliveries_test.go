package notify_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/notify"
)

type fakeLog struct {
	mu    sync.Mutex
	rows  []notify.Delivery
	trims []trimCall
}

func (f *fakeLog) InsertDelivery(_ context.Context, d notify.Delivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, d)
	return nil
}

// trims records each TrimDeliveries call, so a test can assert that the
// "Send test" path applies the retention the dispatcher otherwise would.
func (f *fakeLog) TrimDeliveries(_ context.Context, _, channelID uuid.UUID, keep int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trims = append(f.trims, trimCall{channelID: channelID, keep: keep})
	return nil
}

// GetDelivery and ResendDelivery mirror the SQL store closely enough to
// exercise the service's own branching: the resend matches only a
// FAILED row, so the service's "it exists but is not failed" path is
// reachable here.
func (f *fakeLog) GetDelivery(_ context.Context, _, channelID, id uuid.UUID) (notify.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.rows {
		if d.ChannelID == channelID && d.ID == id {
			return d, nil
		}
	}
	return notify.Delivery{}, notify.ErrNotFound
}

func (f *fakeLog) ResendDelivery(_ context.Context, _, channelID, id uuid.UUID) (notify.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, d := range f.rows {
		if d.ChannelID != channelID || d.ID != id || d.Status != notify.DeliveryFailed {
			continue
		}
		d.Status, d.Attempts, d.LastError, d.SentAt = notify.DeliveryPending, 0, "", nil
		f.rows[i] = d
		return d, nil
	}
	return notify.Delivery{}, notify.ErrNotFound
}

type trimCall struct {
	channelID uuid.UUID
	keep      int
}

func (f *fakeLog) ListDeliveries(_ context.Context, _, channelID uuid.UUID, limit int) ([]notify.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []notify.Delivery
	for i := len(f.rows) - 1; i >= 0 && len(out) < limit; i-- {
		if f.rows[i].ChannelID == channelID {
			out = append(out, f.rows[i])
		}
	}
	return out, nil
}

func TestSendTestRecordsTheOutcome(t *testing.T) {
	w := newWorld(t)
	log := &fakeLog{}
	sender := &countingSender{}
	svc := notify.NewService(notify.Deps{Channels: w.channels, Deliveries: log, Box: fakeBox{}, Mail: w.mail, Sender: sender, PublicURL: "https://pulsix.example.test/"})
	c, err := svc.CreateChannel(ctx, w.org, w.actor, webhookInput("ops hook"))
	if err != nil {
		t.Fatal(err)
	}
	org := notify.OrgRef{ID: w.org, Slug: "acme", Name: "Acme"}
	d, err := svc.SendTest(ctx, org, c.ID)
	if err != nil || d.Status != notify.DeliverySent || d.SentAt == nil || !strings.HasPrefix(d.EventKey, "test:") {
		t.Fatalf("SendTest = %+v, %v", d, err)
	}
	if !strings.Contains(string(d.Payload), `"url":"https://pulsix.example.test/o/acme/settings/notifications"`) || !strings.Contains(string(d.Payload), `"kind":"test"`) {
		t.Fatalf("payload = %s", d.Payload)
	}
	sender.err = errors.New("the endpoint answered 500")
	d, err = svc.SendTest(ctx, org, c.ID)
	if err != nil || d.Status != notify.DeliveryFailed || d.LastError != "the endpoint answered 500" {
		t.Fatalf("failed test = %+v, %v", d, err)
	}
	list, err := svc.Deliveries(ctx, w.org, c.ID, 10)
	if err != nil || len(list) != 2 || list[0].Status != notify.DeliveryFailed {
		t.Fatalf("log = %+v, %v", list, err)
	}
	if _, err := svc.SendTest(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("unknown channel = %v", err)
	}
	if _, err := svc.Deliveries(ctx, w.org, uuid.Must(uuid.NewV7()), 10); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("unknown channel log = %v", err)
	}
}

// TestSendTestTrimsTheChannelsDeliveryLog is backlog 2.80. The retention
// that keeps a channel's log bounded ran only on the dispatcher's own
// Complete and Fail, so a channel an operator only ever exercised through
// "Send test" — one that never had a real queued delivery — grew without
// bound.
func TestSendTestTrimsTheChannelsDeliveryLog(t *testing.T) {
	w := newWorld(t)
	log := &fakeLog{}
	svc := notify.NewService(notify.Deps{Channels: w.channels, Deliveries: log, Box: fakeBox{}, Mail: w.mail, Sender: &countingSender{}, PublicURL: "https://pulsix.example.test/"})
	ch, err := svc.CreateChannel(ctx, w.org, w.actor, webhookInput("ops hook"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendTest(ctx, notify.OrgRef{ID: w.org, Slug: "acme", Name: "Acme"}, ch.ID); err != nil {
		t.Fatal(err)
	}
	if len(log.trims) != 1 {
		t.Fatalf("trim calls = %d, want 1: a test send must apply the same retention the dispatcher does", len(log.trims))
	}
	if log.trims[0].channelID != ch.ID {
		t.Fatalf("trimmed channel = %s, want %s", log.trims[0].channelID, ch.ID)
	}
	if log.trims[0].keep <= 0 {
		t.Fatalf("keep = %d, want the dispatcher's retention", log.trims[0].keep)
	}
}

// TestResendAndPayloadView is backlog 2.75. The delivery log showed
// status, attempts and the last error. An operator looking at a failed
// row could neither see WHAT had been sent nor put it back on the
// queue once the receiver was fixed — the message was simply lost, and
// the only recovery was to make the underlying event happen again.
func TestResendAndPayloadView(t *testing.T) {
	w := newWorld(t)
	log := &fakeLog{}
	sender := &countingSender{}
	svc := notify.NewService(notify.Deps{Channels: w.channels, Deliveries: log, Box: fakeBox{}, Mail: w.mail, Sender: sender, PublicURL: "https://pulsix.example.test/"})
	ch, err := svc.CreateChannel(ctx, w.org, w.actor, webhookInput("ops hook"))
	if err != nil {
		t.Fatal(err)
	}
	org := notify.OrgRef{ID: w.org, Slug: "acme", Name: "Acme"}
	sender.err = errors.New("the endpoint answered 500")
	failed, err := svc.SendTest(ctx, org, ch.ID)
	if err != nil || failed.Status != notify.DeliveryFailed {
		t.Fatalf("SendTest = %+v, %v; want a failed delivery to work with", failed, err)
	}

	// The payload view: what was actually sent, not a summary of it.
	got, err := svc.Delivery(ctx, w.org, ch.ID, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Payload, failed.Payload) || len(got.Payload) == 0 {
		t.Fatalf("payload = %s, want the stored event", got.Payload)
	}
	if _, err := svc.Delivery(ctx, w.org, ch.ID, uuid.Must(uuid.NewV7())); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("an unknown delivery = %v", err)
	}

	// The resend: back to pending with a fresh attempt budget, and the
	// stale failure cleared so the row does not read as both.
	again, err := svc.ResendDelivery(ctx, w.org, ch.ID, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != notify.DeliveryPending || again.Attempts != 0 || again.LastError != "" || again.SentAt != nil {
		t.Fatalf("resent = %+v, want pending with no attempts, no error and no sent time", again)
	}
	if !bytes.Equal(again.Payload, failed.Payload) {
		t.Fatalf("the resend changed the payload: %s", again.Payload)
	}

	// A delivery that is not failed is not resendable: a pending one is
	// already on its way, and a sent one would be a second copy of a
	// message the receiver has.
	if _, err := svc.ResendDelivery(ctx, w.org, ch.ID, again.ID); !errors.Is(err, notify.ErrInvalidInput) {
		t.Fatalf("resending a pending delivery = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.ResendDelivery(ctx, w.org, ch.ID, uuid.Must(uuid.NewV7())); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("resending an unknown delivery = %v", err)
	}
}
