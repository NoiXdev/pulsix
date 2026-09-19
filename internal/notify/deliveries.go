package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// testSendTimeout bounds a synchronous "Send test".
const testSendTimeout = 35 * time.Second

// DeliveryStatus is where a message is: pending (queued or being sent),
// sent, or failed for good.
type DeliveryStatus string

const (
	DeliveryPending DeliveryStatus = "pending"
	DeliverySent    DeliveryStatus = "sent"
	DeliveryFailed  DeliveryStatus = "failed"
)

// Delivery is one message to one channel, and its log entry.
type Delivery struct {
	ID, OrgID, ChannelID uuid.UUID
	EventKey             string
	EventKind            EventKind
	Payload              json.RawMessage
	Status               DeliveryStatus
	Attempts             int
	NextAttemptAt        time.Time
	LastError            string
	CreatedAt            time.Time
	SentAt               *time.Time
}

// DeliveryLog records and lists deliveries (org-scoped).
type DeliveryLog interface {
	InsertDelivery(ctx context.Context, d Delivery) error
	ListDeliveries(ctx context.Context, orgID, channelID uuid.UUID, limit int) ([]Delivery, error)
}

// SendTest sends the sample event (as kind test) through the channel right
// now, outside the queue, so the operator sees the result at once; the
// attempt is recorded in the channel's delivery log either way. A failed
// send is not an error of this call: the returned delivery says failed and
// why.
func (s *Service) SendTest(ctx context.Context, org OrgRef, channelID uuid.UUID) (Delivery, error) {
	ch, err := s.d.Channels.GetChannel(ctx, org.ID, channelID)
	if err != nil {
		return Delivery{}, err
	}
	now := s.d.Now()
	ev := sampleEvent()
	ev.Kind, ev.At, ev.Org = EventTest, now, org
	ev.URL = strings.TrimRight(s.d.PublicURL, "/") + "/o/" + org.Slug + "/settings/notifications"
	payload, err := json.Marshal(ev)
	if err != nil {
		return Delivery{}, fmt.Errorf("encode test event: %w", err)
	}
	id := s.newID()
	sendCtx, cancel := context.WithTimeout(ctx, testSendTimeout)
	sendErr := s.d.Sender.Send(sendCtx, ch, id, ev)
	cancel()
	d := Delivery{
		ID: id, OrgID: org.ID, ChannelID: ch.ID, EventKey: "test:" + id.String(), EventKind: EventTest,
		Payload: payload, Status: DeliverySent, Attempts: 1, NextAttemptAt: now, CreatedAt: now,
	}
	if sendErr != nil {
		d.Status, d.LastError = DeliveryFailed, errorText(sendErr)
	} else {
		d.SentAt = &now
	}
	if err := s.d.Deliveries.InsertDelivery(ctx, d); err != nil {
		return Delivery{}, err
	}
	return d, nil
}

// Deliveries lists the newest deliveries of one channel.
func (s *Service) Deliveries(ctx context.Context, orgID, channelID uuid.UUID, limit int) ([]Delivery, error) {
	if _, err := s.d.Channels.GetChannel(ctx, orgID, channelID); err != nil {
		return nil, err
	}
	return s.d.Deliveries.ListDeliveries(ctx, orgID, channelID, limit)
}
