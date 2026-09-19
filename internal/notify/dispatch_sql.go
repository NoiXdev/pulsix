package notify

import (
	"context"
	"fmt"
	"time"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// DeliverySQLStore is the dispatcher's store: the raw pool with no
// pulsix.org_id bound, so the bypass policy applies and the claim sees
// every organization; every other statement names its org_id.
type DeliverySQLStore struct{ q *gen.Queries }

// NewDeliverySQLStore builds the store on the raw pool.
func NewDeliverySQLStore(d *db.DB) *DeliverySQLStore {
	return &DeliverySQLStore{q: gen.New(d.Pool())}
}

func (st *DeliverySQLStore) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Claimed, error) {
	rows, err := st.q.ClaimNotificationDeliveries(ctx, gen.ClaimNotificationDeliveriesParams{
		LeaseUntil: now.Add(lease), Now: now, Lim: int32(limit), //nolint:gosec // limit is dispatchBatch
	})
	if err != nil {
		return nil, fmt.Errorf("claim notification deliveries: %w", err)
	}
	out := make([]Claimed, len(rows))
	for i, r := range rows {
		out[i] = Claimed{
			Delivery: Delivery{
				ID: r.ID, OrgID: r.OrgID, ChannelID: r.ChannelID, EventKey: r.EventKey, EventKind: EventKind(r.EventKind),
				Payload: r.Payload, Status: DeliveryPending, Attempts: int(r.Attempts), CreatedAt: r.CreatedAt,
			},
			Channel: Channel{ID: r.ChannelID, OrgID: r.OrgID, Kind: Kind(r.ChannelKind), Name: r.ChannelName, Enabled: r.ChannelEnabled, Config: r.ChannelConfig},
		}
	}
	return out, nil
}

func (st *DeliverySQLStore) Complete(ctx context.Context, c Claimed, at time.Time) error {
	if _, err := st.q.CompleteNotificationDelivery(ctx, gen.CompleteNotificationDeliveryParams{
		At: db.NullTime(&at), OrgID: c.Delivery.OrgID, ID: c.Delivery.ID, Attempts: int32(c.Delivery.Attempts), //nolint:gosec // attempts ≤ MaxAttempts
	}); err != nil {
		return fmt.Errorf("complete notification delivery: %w", err)
	}
	return st.trim(ctx, c)
}

func (st *DeliverySQLStore) Retry(ctx context.Context, c Claimed, next time.Time, errText string) error {
	if _, err := st.q.RetryNotificationDelivery(ctx, gen.RetryNotificationDeliveryParams{
		NextAttemptAt: next, LastError: errText, OrgID: c.Delivery.OrgID, ID: c.Delivery.ID, Attempts: int32(c.Delivery.Attempts), //nolint:gosec // attempts ≤ MaxAttempts
	}); err != nil {
		return fmt.Errorf("reschedule notification delivery: %w", err)
	}
	return nil
}

func (st *DeliverySQLStore) Fail(ctx context.Context, c Claimed, errText string) error {
	if _, err := st.q.FailNotificationDelivery(ctx, gen.FailNotificationDeliveryParams{
		LastError: errText, OrgID: c.Delivery.OrgID, ID: c.Delivery.ID, Attempts: int32(c.Delivery.Attempts), //nolint:gosec // attempts ≤ MaxAttempts
	}); err != nil {
		return fmt.Errorf("fail notification delivery: %w", err)
	}
	return st.trim(ctx, c)
}

// trim keeps a channel's newest keepDeliveries finished rows.
func (st *DeliverySQLStore) trim(ctx context.Context, c Claimed) error {
	if err := st.q.TrimNotificationDeliveries(ctx, gen.TrimNotificationDeliveriesParams{
		OrgID: c.Delivery.OrgID, ChannelID: c.Delivery.ChannelID, Keep: keepDeliveries,
	}); err != nil {
		return fmt.Errorf("trim notification deliveries: %w", err)
	}
	return nil
}
