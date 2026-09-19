package notify

import (
	"context"
	"encoding/json"
	"errors"
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
	if err := s.requireSendDeps(); err != nil {
		return Delivery{}, err
	}
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
	// The attempt is recorded even when ctx ended meanwhile (a caller that
	// disconnected mid-send): the send already happened or not, and the
	// insert must not lose that row just because nobody is left to read
	// the response — the same detached, bounded write dispatch.go's
	// deliver uses for its own outcome writes.
	insertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.d.Deliveries.InsertDelivery(insertCtx, d); err != nil {
		return Delivery{}, err
	}
	return d, nil
}

// Deliveries lists the newest deliveries of one channel.
func (s *Service) Deliveries(ctx context.Context, orgID, channelID uuid.UUID, limit int) ([]Delivery, error) {
	if s.d.Deliveries == nil {
		return nil, fmt.Errorf("notify: Deliveries needs Deps.Deliveries, which is nil (%w)", errMissingDep)
	}
	if _, err := s.d.Channels.GetChannel(ctx, orgID, channelID); err != nil {
		return nil, err
	}
	return s.d.Deliveries.ListDeliveries(ctx, orgID, channelID, limit)
}

// errMissingDep marks a Service built without a Deps field one of its
// methods actually needs — a wiring bug (production always sets every
// field; see internal/app), never a caller's input. requireSendDeps and
// Deliveries above check for it explicitly so the failure names the
// missing field instead of a nil-pointer panic on first use; NewService
// itself does not refuse a partial Deps, because tests that only exercise
// channel management (CreateChannel, UpdateChannel, ...) have no need for
// Sender or Deliveries.
var errMissingDep = errors.New("service built without a required dependency")

// requireSendDeps checks SendTest's two extra dependencies beyond channel
// management: Sender (to actually deliver) and Deliveries (to log the
// attempt either way).
func (s *Service) requireSendDeps() error {
	var missing []string
	if s.d.Sender == nil {
		missing = append(missing, "Sender")
	}
	if s.d.Deliveries == nil {
		missing = append(missing, "Deliveries")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("notify: SendTest needs Deps.%s, which is nil (%w)", strings.Join(missing, " and Deps."), errMissingDep)
}
