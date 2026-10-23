package notify

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

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

// Claim implements DeliveryQueue. It passes only the lease's LENGTH: when
// the lease starts and whether another manager's has expired are both
// decided by the database's clock, the one clock every manager shares
// (see ClaimNotificationDeliveries in notifications.sql, backlog 2.80).
func (st *DeliverySQLStore) Claim(ctx context.Context, lease time.Duration, limit int) ([]Claimed, error) {
	rows, err := st.q.ClaimNotificationDeliveries(ctx, gen.ClaimNotificationDeliveriesParams{
		Lease: pgtype.Interval{Microseconds: lease.Microseconds(), Valid: true},
		Lim:   int32(limit), //nolint:gosec // limit is dispatchBatch
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
				PendingRecipients: r.PendingRecipients,
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

// Retry implements DeliveryQueue. in is how long to wait, not when to
// run: the database adds it to its own clock (backlog 2.80).
func (st *DeliverySQLStore) Retry(ctx context.Context, c Claimed, in time.Duration, errText string, pending []string) error {
	if _, err := st.q.RetryNotificationDelivery(ctx, gen.RetryNotificationDeliveryParams{
		RetryIn:           pgtype.Interval{Microseconds: in.Microseconds(), Valid: true},
		PendingRecipients: pending,
		LastError:         errText, OrgID: c.Delivery.OrgID, ID: c.Delivery.ID, Attempts: int32(c.Delivery.Attempts), //nolint:gosec // attempts ≤ MaxAttempts
	}); err != nil {
		return fmt.Errorf("retry notification delivery: %w", err)
	}
	return st.trim(ctx, c)
}

func (st *DeliverySQLStore) Fail(ctx context.Context, c Claimed, errText string) error {
	if _, err := st.q.FailNotificationDelivery(ctx, gen.FailNotificationDeliveryParams{
		LastError: errText, OrgID: c.Delivery.OrgID, ID: c.Delivery.ID, Attempts: int32(c.Delivery.Attempts), //nolint:gosec // attempts ≤ MaxAttempts
	}); err != nil {
		return fmt.Errorf("fail notification delivery: %w", err)
	}
	return st.trim(ctx, c)
}

// FailStuck implements DeliveryQueue: see the interface's doc comment and
// FailStuckNotificationDeliveries in notifications.sql. It runs on the raw
// pool without pulsix.org_id, like the claim it complements — a stuck row
// belongs to no particular organization's request.
func (st *DeliverySQLStore) FailStuck(ctx context.Context) (int64, error) {
	n, err := st.q.FailStuckNotificationDeliveries(ctx,
		"gave up: the delivery was claimed repeatedly without ever recording an outcome")
	if err != nil {
		return 0, fmt.Errorf("fail stuck notification deliveries: %w", err)
	}
	return n, nil
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
