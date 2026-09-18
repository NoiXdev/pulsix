package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore persists triggers. Resolve runs on whatever scope ctx carries —
// the hook endpoints carry none (bypass policy) and look up by id alone.
type SQLStore struct {
	d *db.DB
	q *gen.Queries
}

func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{d: d, q: gen.New(d)} }

func (st *SQLStore) Create(ctx context.Context, t Trigger, sealed []byte) (Trigger, error) {
	mapping, rl, err := encode(t)
	if err != nil {
		return Trigger{}, err
	}
	var out Trigger
	err = st.d.WithWriteTx(ctx, t.OrgID, func(ctx context.Context) error {
		row, err := st.q.CreateTrigger(ctx, gen.CreateTriggerParams{
			ID: t.ID, OrgID: t.OrgID, Kind: string(t.Kind), TemplateID: db.NullUUID(t.TemplateID), RepoID: db.NullUUID(t.RepoID),
			Name: t.Name, Enabled: t.Enabled, Preset: string(t.Preset), Auth: string(t.Auth()), SignatureHeader: t.SignatureHeader(),
			TimestampHeader: t.TimestampHeader, Secret: sealed, AllowCidrs: cidrsOrEmpty(t.AllowCIDRs), Mapping: mapping,
			OnActive: string(t.OnActive), RateLimit: rl, CreatedBy: db.NullUUID(t.CreatedBy),
		})
		if err != nil {
			return err
		}
		if out, err = toTrigger(row); err != nil {
			return err
		}
		return st.fill(ctx, t.OrgID, t.ID, t.RateLimit)
	})
	switch {
	case db.IsUniqueViolation(err):
		return Trigger{}, ErrNameTaken
	case db.IsForeignKeyViolation(err):
		return Trigger{}, fmt.Errorf("%w: the target does not exist", ErrInvalidInput)
	case err != nil:
		return Trigger{}, fmt.Errorf("create trigger: %w", err)
	}
	return out, nil
}

func (st *SQLStore) fill(ctx context.Context, orgID, id uuid.UUID, rl RateLimit) error {
	capacity, _ := capacityAndRefill(rl)
	return st.q.FillTriggerBucket(ctx, gen.FillTriggerBucketParams{TriggerID: id, OrgID: orgID, Tokens: capacity, At: time.Now()})
}

func (st *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Trigger, error) {
	row, err := st.q.GetTrigger(ctx, gen.GetTriggerParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Trigger{}, ErrNotFound
	}
	if err != nil {
		return Trigger{}, fmt.Errorf("get trigger: %w", err)
	}
	return toTrigger(row)
}

func (st *SQLStore) Resolve(ctx context.Context, id uuid.UUID) (Trigger, []byte, error) {
	row, err := st.q.ResolveTrigger(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Trigger{}, nil, ErrNotFound
	}
	if err != nil {
		return Trigger{}, nil, fmt.Errorf("resolve trigger: %w", err)
	}
	t, err := toTrigger(row)
	return t, row.Secret, err
}

func (st *SQLStore) List(ctx context.Context, orgID uuid.UUID, f ListFilter) ([]Trigger, error) {
	rows, err := st.q.ListTriggers(ctx, gen.ListTriggersParams{OrgID: orgID, TemplateID: db.NullUUID(f.TemplateID), RepoID: db.NullUUID(f.RepoID)})
	if err != nil {
		return nil, fmt.Errorf("list triggers: %w", err)
	}
	out := make([]Trigger, 0, len(rows))
	for _, r := range rows {
		t, err := toTrigger(r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (st *SQLStore) Update(ctx context.Context, t Trigger, revision int64) (Trigger, error) {
	mapping, rl, err := encode(t)
	if err != nil {
		return Trigger{}, err
	}
	var out Trigger
	err = st.d.WithWriteTx(ctx, t.OrgID, func(ctx context.Context) error {
		row, err := st.q.UpdateTrigger(ctx, gen.UpdateTriggerParams{
			OrgID: t.OrgID, ID: t.ID, Revision: revision, Name: t.Name, Enabled: t.Enabled, Preset: string(t.Preset),
			Auth: string(t.Auth()), SignatureHeader: t.SignatureHeader(), TimestampHeader: t.TimestampHeader,
			AllowCidrs: cidrsOrEmpty(t.AllowCIDRs), Mapping: mapping, OnActive: string(t.OnActive), RateLimit: rl,
		})
		if err != nil {
			return err
		}
		if out, err = toTrigger(row); err != nil {
			return err
		}
		return st.fill(ctx, t.OrgID, t.ID, t.RateLimit)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := st.Get(ctx, t.OrgID, t.ID); gerr != nil {
			return Trigger{}, gerr
		}
		return Trigger{}, ErrRevisionMismatch
	}
	if db.IsUniqueViolation(err) {
		return Trigger{}, ErrNameTaken
	}
	if err != nil {
		return Trigger{}, fmt.Errorf("update trigger: %w", err)
	}
	return out, nil
}

func (st *SQLStore) SetSecret(ctx context.Context, orgID, id uuid.UUID, sealed []byte) (Trigger, error) {
	row, err := st.q.SetTriggerSecret(ctx, gen.SetTriggerSecretParams{OrgID: orgID, ID: id, Secret: sealed})
	if errors.Is(err, pgx.ErrNoRows) {
		return Trigger{}, ErrNotFound
	}
	if err != nil {
		return Trigger{}, fmt.Errorf("set trigger secret: %w", err)
	}
	return toTrigger(row)
}

func (st *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := st.q.DeleteTrigger(ctx, gen.DeleteTriggerParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete trigger: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (st *SQLStore) Deliveries(ctx context.Context, orgID, id uuid.UUID, limit int) ([]Delivery, error) {
	rows, err := st.q.ListTriggerDeliveries(ctx, gen.ListTriggerDeliveriesParams{OrgID: orgID, TriggerID: id, Lim: int32(limit)}) //nolint:gosec // clamped by the API
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	out := make([]Delivery, len(rows))
	for i, r := range rows {
		out[i] = Delivery{ID: r.ID, OrgID: r.OrgID, TriggerID: r.TriggerID, At: r.At, SourceIP: r.SourceIp,
			Outcome: Outcome(r.Outcome), Reason: r.Reason, JobGroupID: db.UUIDPtr(r.JobGroupID)}
	}
	return out, nil
}

// TakeToken takes one token from the shared bucket; on a miss it reads the
// bucket once to compute Retry-After.
func (st *SQLStore) TakeToken(ctx context.Context, orgID, id uuid.UUID, rl RateLimit, now time.Time) (bool, time.Duration, error) {
	capacity, refill := capacityAndRefill(rl)
	_, err := st.q.TakeTriggerToken(ctx, gen.TakeTriggerTokenParams{Capacity: capacity, Now: now, Refill: refill, OrgID: orgID, TriggerID: id})
	if err == nil {
		return true, 0, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, 0, fmt.Errorf("take trigger token: %w", err)
	}
	b, err := st.q.GetTriggerBucket(ctx, gen.GetTriggerBucketParams{OrgID: orgID, TriggerID: id})
	if err != nil {
		return false, 0, fmt.Errorf("read trigger bucket: %w", err)
	}
	current := math.Min(capacity, b.Tokens+math.Max(now.Sub(b.UpdatedAt).Seconds(), 0)*refill)
	wait := time.Duration(math.Ceil((1-current)/refill)) * time.Second
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait, nil
}

// RecordDelivery appends a delivery (throttled for rejections) and trims
// the trigger's log to its newest 500 rows.
func (st *SQLStore) RecordDelivery(ctx context.Context, d Delivery, throttle bool) error {
	if err := st.q.InsertTriggerDelivery(ctx, gen.InsertTriggerDeliveryParams{
		ID: d.ID, OrgID: d.OrgID, TriggerID: d.TriggerID, At: d.At, SourceIp: d.SourceIP,
		Outcome: string(d.Outcome), Reason: d.Reason, JobGroupID: db.NullUUID(d.JobGroupID), Throttle: throttle,
	}); err != nil {
		return fmt.Errorf("record delivery: %w", err)
	}
	if err := st.q.TrimTriggerDeliveries(ctx, gen.TrimTriggerDeliveriesParams{OrgID: d.OrgID, TriggerID: d.TriggerID}); err != nil {
		return fmt.Errorf("trim deliveries: %w", err)
	}
	return nil
}

// cidrsOrEmpty keeps allow_cidrs NOT NULL: a nil slice would bind NULL.
func cidrsOrEmpty(p []netip.Prefix) []netip.Prefix {
	if p == nil {
		return []netip.Prefix{}
	}
	return p
}

func encode(t Trigger) (mapping, rl []byte, err error) {
	if mapping, err = json.Marshal(t.Mapping); err != nil {
		return nil, nil, err
	}
	if rl, err = json.Marshal(t.RateLimit); err != nil {
		return nil, nil, err
	}
	return mapping, rl, nil
}

func toTrigger(r gen.JobTrigger) (Trigger, error) {
	t := Trigger{
		ID: r.ID, OrgID: r.OrgID, Kind: Kind(r.Kind), TemplateID: db.UUIDPtr(r.TemplateID), RepoID: db.UUIDPtr(r.RepoID),
		Name: r.Name, Enabled: r.Enabled, Preset: Preset(r.Preset), TimestampHeader: r.TimestampHeader,
		AllowCIDRs: r.AllowCidrs, OnActive: OnActive(r.OnActive), CreatedBy: db.UUIDPtr(r.CreatedBy),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Revision: r.Revision,
	}
	if err := json.Unmarshal(r.Mapping, &t.Mapping); err != nil {
		return Trigger{}, fmt.Errorf("decode trigger mapping: %w", err)
	}
	if err := json.Unmarshal(r.RateLimit, &t.RateLimit); err != nil {
		return Trigger{}, fmt.Errorf("decode trigger rate limit: %w", err)
	}
	return t, nil
}
