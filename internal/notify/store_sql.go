package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore is the org-scoped notification store.
type SQLStore struct {
	d *db.DB
	q *gen.Queries
}

// NewSQLStore builds the store on *db.DB so every statement runs in the
// request's org scope.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{d: d, q: gen.New(d)} }

func (st *SQLStore) CreateChannel(ctx context.Context, c Channel) (Channel, error) {
	row, err := st.q.CreateNotificationChannel(ctx, gen.CreateNotificationChannelParams{
		ID: c.ID, OrgID: c.OrgID, Kind: string(c.Kind), Name: c.Name, Config: c.Config, BodyJson: c.BodyJSON, Enabled: c.Enabled,
		CreatedBy: db.NullUUID(c.CreatedBy),
	})
	if db.IsUniqueViolation(err) {
		return Channel{}, ErrNameTaken
	}
	if err != nil {
		return Channel{}, fmt.Errorf("create notification channel: %w", err)
	}
	return toChannel(row), nil
}

func (st *SQLStore) GetChannel(ctx context.Context, orgID, id uuid.UUID) (Channel, error) {
	row, err := st.q.GetNotificationChannel(ctx, gen.GetNotificationChannelParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	if err != nil {
		return Channel{}, fmt.Errorf("get notification channel: %w", err)
	}
	return toChannel(row), nil
}

func (st *SQLStore) ListChannels(ctx context.Context, orgID uuid.UUID) ([]Channel, error) {
	rows, err := st.q.ListNotificationChannels(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list notification channels: %w", err)
	}
	out := make([]Channel, len(rows))
	for i, r := range rows {
		out[i] = toChannel(r)
	}
	return out, nil
}

func (st *SQLStore) UpdateChannel(ctx context.Context, c Channel, revision int64) (Channel, error) {
	row, err := st.q.UpdateNotificationChannel(ctx, gen.UpdateNotificationChannelParams{
		Name: c.Name, Config: c.Config, BodyJson: c.BodyJSON, Enabled: c.Enabled, OrgID: c.OrgID, ID: c.ID, Revision: revision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := st.GetChannel(ctx, c.OrgID, c.ID); gerr != nil {
			return Channel{}, gerr
		}
		return Channel{}, ErrRevisionMismatch
	}
	if db.IsUniqueViolation(err) {
		return Channel{}, ErrNameTaken
	}
	if err != nil {
		return Channel{}, fmt.Errorf("update notification channel: %w", err)
	}
	return toChannel(row), nil
}

func (st *SQLStore) DeleteChannel(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := st.q.DeleteNotificationChannel(ctx, gen.DeleteNotificationChannelParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete notification channel: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TrimDeliveries implements DeliveryLog: the same retention the
// dispatcher applies on every outcome, reachable for the "Send test"
// path, which writes its row without going through the dispatcher
// (backlog 2.80).
func (st *SQLStore) TrimDeliveries(ctx context.Context, orgID, channelID uuid.UUID, keep int) error {
	if err := st.q.TrimNotificationDeliveries(ctx, gen.TrimNotificationDeliveriesParams{
		OrgID: orgID, ChannelID: channelID, Keep: int32(keep), //nolint:gosec // keep is keepDeliveries
	}); err != nil {
		return fmt.Errorf("trim notification deliveries: %w", err)
	}
	return nil
}

func (st *SQLStore) InsertDelivery(ctx context.Context, d Delivery) error {
	if _, err := st.q.InsertNotificationDelivery(ctx, gen.InsertNotificationDeliveryParams{
		ID: d.ID, OrgID: d.OrgID, ChannelID: d.ChannelID, EventKey: d.EventKey, EventKind: string(d.EventKind),
		Payload: d.Payload, Status: string(d.Status), Attempts: int32(d.Attempts), //nolint:gosec // attempts ≤ MaxAttempts
		NextAttemptAt: d.NextAttemptAt, LastError: d.LastError, CreatedAt: d.CreatedAt, SentAt: db.NullTime(d.SentAt),
	}); err != nil {
		return fmt.Errorf("record notification delivery: %w", err)
	}
	return nil
}

func (st *SQLStore) ListDeliveries(ctx context.Context, orgID, channelID uuid.UUID, limit int) ([]Delivery, error) {
	rows, err := st.q.ListNotificationDeliveries(ctx, gen.ListNotificationDeliveriesParams{OrgID: orgID, ChannelID: channelID, Lim: int32(limit)}) //nolint:gosec // limit is clamped by the API (≤ 100)
	if err != nil {
		return nil, fmt.Errorf("list notification deliveries: %w", err)
	}
	out := make([]Delivery, len(rows))
	for i, r := range rows {
		out[i] = Delivery{
			ID: r.ID, OrgID: r.OrgID, ChannelID: r.ChannelID, EventKey: r.EventKey, EventKind: EventKind(r.EventKind),
			Payload: r.Payload, Status: DeliveryStatus(r.Status), Attempts: int(r.Attempts), NextAttemptAt: r.NextAttemptAt,
			LastError: r.LastError, CreatedAt: r.CreatedAt, SentAt: db.TimePtr(r.SentAt),
		}
	}
	return out, nil
}

func (st *SQLStore) ScheduleRules(ctx context.Context, orgID, scheduleID uuid.UUID) ([]ChannelRule, error) {
	rows, err := st.q.ListScheduleNotifications(ctx, gen.ListScheduleNotificationsParams{OrgID: orgID, ScheduleID: scheduleID})
	if err != nil {
		return nil, fmt.Errorf("list schedule notifications: %w", err)
	}
	out := make([]ChannelRule, len(rows))
	for i, r := range rows {
		out[i] = ChannelRule{ChannelID: r.ChannelID, ChannelName: r.ChannelName, ChannelKind: Kind(r.ChannelKind)}
		for _, e := range r.Events {
			out[i].Events = append(out[i].Events, Category(e))
		}
	}
	return out, nil
}

// OrgScheduleCategories reads every (schedule, events) row of the
// organization's schedule_notifications in one query (backlog 2.73),
// grouping the raw per-channel event arrays by schedule id; Service's own
// OrgScheduleCategories unions them into the distinct categories each
// schedule's rules cover.
func (st *SQLStore) OrgScheduleCategories(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID][][]string, error) {
	rows, err := st.q.ListOrgScheduleNotifyEvents(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list org schedule notify events: %w", err)
	}
	out := map[uuid.UUID][][]string{}
	for _, r := range rows {
		out[r.ScheduleID] = append(out[r.ScheduleID], r.Events)
	}
	return out, nil
}

// ReplaceScheduleRules swaps a schedule's rules in one transaction. A
// foreign key violation means a channel that is not the organization's
// (the schedule's existence is the caller's check).
func (st *SQLStore) ReplaceScheduleRules(ctx context.Context, orgID, scheduleID uuid.UUID, rules []ChannelRule) error {
	tx, err := st.d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin schedule notifications: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)
	if err := q.DeleteScheduleNotifications(ctx, gen.DeleteScheduleNotificationsParams{OrgID: orgID, ScheduleID: scheduleID}); err != nil {
		return fmt.Errorf("clear schedule notifications: %w", err)
	}
	for _, r := range rules {
		events := make([]string, len(r.Events))
		for i, e := range r.Events {
			events[i] = string(e)
		}
		err := q.InsertScheduleNotification(ctx, gen.InsertScheduleNotificationParams{OrgID: orgID, ScheduleID: scheduleID, ChannelID: r.ChannelID, Events: events})
		if db.IsForeignKeyViolation(err) {
			// schedule_notifications has two composite foreign keys, and
			// only one of them is the caller's mistake. A violation of the
			// schedule reference means the schedule was deleted between the
			// handler's read and this write — a vanished schedule, not an
			// unknown channel. A violation that names no constraint falls
			// back to the channel, the case an operator can actually cause.
			if strings.Contains(db.ConstraintName(err), "schedule_id") {
				return fmt.Errorf("%w: the schedule was deleted while its notifications were being saved", ErrNotFound)
			}
			return fmt.Errorf("%w: unknown channel %s", ErrInvalidInput, r.ChannelID)
		}
		if err != nil {
			return fmt.Errorf("store schedule notification: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit schedule notifications: %w", err)
	}
	return nil
}

func (st *SQLStore) GetDigest(ctx context.Context, orgID uuid.UUID) (DigestSettings, bool, error) {
	row, err := st.q.GetNotificationDigest(ctx, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DigestSettings{}, false, nil
	}
	if err != nil {
		return DigestSettings{}, false, fmt.Errorf("get notification digest: %w", err)
	}
	chans, err := st.q.ListNotificationDigestChannels(ctx, orgID)
	if err != nil {
		return DigestSettings{}, false, fmt.Errorf("list digest channels: %w", err)
	}
	d := DigestSettings{Enabled: row.Enabled, Weekday: int(row.Weekday), Time: row.AtTime, Timezone: row.Timezone, NextRunAt: db.TimePtr(row.NextRunAt)}
	for _, c := range chans {
		d.Channels = append(d.Channels, ChannelRef{ID: c.ChannelID, Name: c.ChannelName, Kind: Kind(c.ChannelKind)})
	}
	return d, true, nil
}

// PutDigest upserts the settings and replaces the channel list in one
// transaction; an unknown channel is ErrInvalidInput.
func (st *SQLStore) PutDigest(ctx context.Context, orgID uuid.UUID, s DigestSettings, channels []uuid.UUID) error {
	tx, err := st.d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin notification digest: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)
	if _, err := q.UpsertNotificationDigest(ctx, gen.UpsertNotificationDigestParams{
		OrgID: orgID, Enabled: s.Enabled, Weekday: int16(s.Weekday), AtTime: s.Time, Timezone: s.Timezone, //nolint:gosec // 0-6
		NextRunAt: db.NullTime(s.NextRunAt),
	}); err != nil {
		return fmt.Errorf("store notification digest: %w", err)
	}
	if err := q.DeleteNotificationDigestChannels(ctx, orgID); err != nil {
		return fmt.Errorf("clear digest channels: %w", err)
	}
	for _, ch := range channels {
		err := q.InsertNotificationDigestChannel(ctx, gen.InsertNotificationDigestChannelParams{OrgID: orgID, ChannelID: ch})
		if db.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: unknown channel %s", ErrInvalidInput, ch)
		}
		if err != nil {
			return fmt.Errorf("store digest channel: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit notification digest: %w", err)
	}
	return nil
}

func toChannel(r gen.NotificationChannel) Channel {
	return Channel{
		ID: r.ID, OrgID: r.OrgID, Kind: Kind(r.Kind), Name: r.Name, Enabled: r.Enabled, Config: r.Config, BodyJSON: r.BodyJson,
		CreatedBy: db.UUIDPtr(r.CreatedBy), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Revision: r.Revision,
	}
}
