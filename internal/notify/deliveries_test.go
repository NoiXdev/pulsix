package notify_test

import (
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
