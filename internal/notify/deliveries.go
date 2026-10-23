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
	// PendingRecipients narrows an e-mail retry to the addresses the
	// previous attempt did not reach. Empty means "the channel's own
	// list" — what a first attempt and every pre-2.74 row mean. Only a
	// retry after a partial failure sets it, and a webhook never does:
	// one destination leaves nothing to narrow.
	PendingRecipients []string
}

// DeliveryLog records and lists deliveries (org-scoped).
type DeliveryLog interface {
	InsertDelivery(ctx context.Context, d Delivery) error
	ListDeliveries(ctx context.Context, orgID, channelID uuid.UUID, limit int) ([]Delivery, error)
	// TrimDeliveries keeps a channel's newest keep finished rows. The
	// dispatcher trims on every outcome it records, but a "Send test"
	// writes its row straight to the log without going through the
	// dispatcher at all — so a channel an operator only ever tests grew
	// its log without bound (backlog 2.80).
	TrimDeliveries(ctx context.Context, orgID, channelID uuid.UUID, keep int) error
	// GetDelivery reads one delivery, payload included (backlog 2.75).
	// The list deliberately leaves the payload out — it returns up to a
	// hundred rows and a digest's event is not small — so seeing what
	// was sent is its own read.
	GetDelivery(ctx context.Context, orgID, channelID, id uuid.UUID) (Delivery, error)
	// ResendDelivery puts a FAILED delivery back on the queue with a
	// fresh attempt budget, and reports ErrNotFound when the row is
	// gone OR is not failed — the status predicate lives in the
	// statement so two operators cannot both reset a row the dispatcher
	// has meanwhile claimed. The caller tells the two apart.
	ResendDelivery(ctx context.Context, orgID, channelID, id uuid.UUID) (Delivery, error)
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
	// A test send is its own one-off delivery: no queue row behind it and
	// so no narrowing — the channel's full recipient list, which is what
	// an operator testing a channel wants to confirm reaches everyone.
	sendErr := s.d.Sender.Send(sendCtx, Claimed{Channel: ch, Delivery: Delivery{ID: id, OrgID: org.ID, ChannelID: ch.ID}}, ev)
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
	// The row is already recorded, so a trim failure must not turn a
	// completed test into an error for the operator: log it and move on.
	if err := s.d.Deliveries.TrimDeliveries(insertCtx, org.ID, ch.ID, keepDeliveries); err != nil {
		s.d.Log.ErrorContext(insertCtx, "trim the channel's delivery log after a test send", "channel", ch.ID, "org", org.ID, "err", err)
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

// Delivery reads one delivery of a channel, payload included: what was
// actually sent, which the log's list does not carry (backlog 2.75).
func (s *Service) Delivery(ctx context.Context, orgID, channelID, id uuid.UUID) (Delivery, error) {
	if s.d.Deliveries == nil {
		return Delivery{}, fmt.Errorf("notify: Delivery needs Deps.Deliveries, which is nil (%w)", errMissingDep)
	}
	if _, err := s.d.Channels.GetChannel(ctx, orgID, channelID); err != nil {
		return Delivery{}, err
	}
	return s.d.Deliveries.GetDelivery(ctx, orgID, channelID, id)
}

// ResendDelivery puts a failed delivery back on the queue, so a message
// lost to a receiver that was down can be sent once it is back —
// without having to make the underlying event happen again (backlog
// 2.75).
//
// Only a FAILED delivery: a pending one is already on its way, and a
// sent one would put a second copy of a message in front of a receiver
// that already has it. An e-mail delivery keeps whatever
// pending_recipients it carried, so a resend after a partial send owes
// the same addresses rather than the whole list again (backlog 2.74).
func (s *Service) ResendDelivery(ctx context.Context, orgID, channelID, id uuid.UUID) (Delivery, error) {
	if s.d.Deliveries == nil {
		return Delivery{}, fmt.Errorf("notify: ResendDelivery needs Deps.Deliveries, which is nil (%w)", errMissingDep)
	}
	if _, err := s.d.Channels.GetChannel(ctx, orgID, channelID); err != nil {
		return Delivery{}, err
	}
	d, err := s.d.Deliveries.ResendDelivery(ctx, orgID, channelID, id)
	if !errors.Is(err, ErrNotFound) {
		return d, err
	}
	// The statement matched nothing. Which of the two reasons it was
	// decides what the operator is told, and only a second read can
	// say: a row that exists but is not failed is a refusal to explain,
	// not a missing thing.
	existing, getErr := s.d.Deliveries.GetDelivery(ctx, orgID, channelID, id)
	if getErr != nil {
		return Delivery{}, getErr
	}
	return Delivery{}, fmt.Errorf("%w: only a failed delivery can be sent again; this one is %s", ErrInvalidInput, existing.Status)
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
