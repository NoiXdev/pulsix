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
	mu   sync.Mutex
	rows []notify.Delivery
}

func (f *fakeLog) InsertDelivery(_ context.Context, d notify.Delivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, d)
	return nil
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
