package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	d   *db.DB
	q   *gen.Queries
	log *slog.Logger
}

func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{d: d, q: gen.New(d), log: slog.Default()} }

func (st *SQLStore) Create(ctx context.Context, t Trigger, sealed []byte) (Trigger, error) {
	mapping, rl, err := encode(t)
	if err != nil {
		return Trigger{}, err
	}
	var out Trigger
	// WithOrg, not WithWriteTx: inside a bound write transaction (an
	// approved trigger request's decision) it is a savepoint of that
	// transaction, so the trigger commits with the decision or not at all.
	err = st.d.WithOrg(ctx, t.OrgID, func(tx pgx.Tx) error {
		q := st.q.WithTx(tx)
		row, err := q.CreateTrigger(ctx, gen.CreateTriggerParams{
			ID: t.ID, OrgID: t.OrgID, Kind: string(t.Kind), TemplateID: db.NullUUID(t.TemplateID), RepoID: db.NullUUID(t.RepoID),
			Name: t.Name, Enabled: t.Enabled, Preset: string(t.Preset), Auth: string(t.Auth()), SignatureHeader: t.SignatureHeader(),
			TimestampHeader: t.TimestampHeader, Secret: sealed, AllowCidrs: cidrsOrEmpty(t.AllowCIDRs), Mapping: mapping,
			OnActive: string(t.OnActive), RateLimit: rl, CreatedBy: db.NullUUID(t.CreatedBy),
			SettleSeconds: int32(t.SettleSeconds), MaxWaitSeconds: int32Ptr(t.MaxWaitSeconds), //nolint:gosec // bounded by checkSettle
		})
		if err != nil {
			return err
		}
		if out, err = toTrigger(row); err != nil {
			return err
		}
		return fill(ctx, q, t.OrgID, t.ID, t.RateLimit)
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

func fill(ctx context.Context, q *gen.Queries, orgID, id uuid.UUID, rl RateLimit) error {
	capacity, _ := capacityAndRefill(rl)
	return q.FillTriggerBucket(ctx, gen.FillTriggerBucketParams{TriggerID: id, OrgID: orgID, Tokens: capacity, At: time.Now()})
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
	// WithOrg nests in a bound write transaction, as in Create.
	err = st.d.WithOrg(ctx, t.OrgID, func(tx pgx.Tx) error {
		q := st.q.WithTx(tx)
		// The stored row, read inside this transaction: whether the
		// bucket is refilled below turns on whether the rate limit
		// actually changed, and reading it here rather than taking the
		// caller's word keeps that decision atomic with the write.
		prevRow, err := q.GetTrigger(ctx, gen.GetTriggerParams{OrgID: t.OrgID, ID: t.ID})
		if err != nil {
			return err
		}
		prev, err := toTrigger(prevRow)
		if err != nil {
			return err
		}
		row, err := q.UpdateTrigger(ctx, gen.UpdateTriggerParams{
			OrgID: t.OrgID, ID: t.ID, Revision: revision, Name: t.Name, Enabled: t.Enabled, Preset: string(t.Preset),
			Auth: string(t.Auth()), SignatureHeader: t.SignatureHeader(), TimestampHeader: t.TimestampHeader,
			AllowCidrs: cidrsOrEmpty(t.AllowCIDRs), Mapping: mapping, OnActive: string(t.OnActive), RateLimit: rl,
			SettleSeconds: int32(t.SettleSeconds), MaxWaitSeconds: int32Ptr(t.MaxWaitSeconds), //nolint:gosec // bounded by checkSettle
		})
		if err != nil {
			return err
		}
		if out, err = toTrigger(row); err != nil {
			return err
		}
		// Only when the rate limit itself changed (backlog 2.62). The
		// fill resets the bucket to capacity, so doing it on every edit
		// meant anyone who could rename a trigger could clear its
		// throttle — as often as they liked, by saving it again. A
		// changed limit IS a refill: the operator has said what the
		// budget should be, and the bucket was sized for the old one.
		if prev.RateLimit == t.RateLimit {
			return nil
		}
		return fill(ctx, q, t.OrgID, t.ID, t.RateLimit)
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

// SetSecretAt replaces the secret of the trigger at revision:
// ErrRevisionMismatch when it changed since, ErrNotFound when it is gone.
func (st *SQLStore) SetSecretAt(ctx context.Context, orgID, id uuid.UUID, revision int64, sealed []byte) (Trigger, error) {
	row, err := st.q.SetTriggerSecretAt(ctx, gen.SetTriggerSecretAtParams{OrgID: orgID, ID: id, Revision: revision, Secret: sealed})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := st.Get(ctx, orgID, id); gerr != nil {
			return Trigger{}, gerr
		}
		return Trigger{}, ErrRevisionMismatch
	}
	if err != nil {
		return Trigger{}, fmt.Errorf("set trigger secret: %w", err)
	}
	return toTrigger(row)
}

// Disable turns the trigger off; a disabled one is returned as it is.
func (st *SQLStore) Disable(ctx context.Context, orgID, id uuid.UUID) (Trigger, error) {
	row, err := st.q.DisableTrigger(ctx, gen.DisableTriggerParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return st.Get(ctx, orgID, id)
	}
	if err != nil {
		return Trigger{}, fmt.Errorf("disable trigger: %w", err)
	}
	return toTrigger(row)
}

// InTx runs fn in one organization-scoped write transaction that every
// store on the same database joins (db.WithWriteTx).
func (st *SQLStore) InTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error {
	return st.d.WithWriteTx(ctx, orgID, fn)
}

// DisableForReview disables a trigger for a person's review (reason
// DisabledForReview): an enabled one, and one a person disabled; ok is
// false, and the trigger comes back as it is, when it was disabled for
// review already.
func (st *SQLStore) DisableForReview(ctx context.Context, orgID, id uuid.UUID) (Trigger, bool, error) {
	row, err := st.q.DisableTriggerForReview(ctx, gen.DisableTriggerForReviewParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		cur, gerr := st.Get(ctx, orgID, id)
		return cur, false, gerr
	}
	if err != nil {
		return Trigger{}, false, fmt.Errorf("disable trigger for review: %w", err)
	}
	t, err := toTrigger(row)
	return t, err == nil, err
}

// EnableAt enables the trigger at revision and clears why it was
// disabled: ErrRevisionMismatch when it changed since, ErrNotFound when it
// is gone.
func (st *SQLStore) EnableAt(ctx context.Context, orgID, id uuid.UUID, revision int64) (Trigger, error) {
	row, err := st.q.EnableTriggerAt(ctx, gen.EnableTriggerAtParams{OrgID: orgID, ID: id, Revision: revision})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := st.Get(ctx, orgID, id); gerr != nil {
			return Trigger{}, gerr
		}
		return Trigger{}, ErrRevisionMismatch
	}
	if err != nil {
		return Trigger{}, fmt.Errorf("enable trigger: %w", err)
	}
	return toTrigger(row)
}

// LockForReview reads the trigger with its row locked until the
// transaction ends (ErrNotFound).
func (st *SQLStore) LockForReview(ctx context.Context, orgID, id uuid.UUID) (Trigger, error) {
	row, err := st.q.LockTriggerForReview(ctx, gen.LockTriggerForReviewParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Trigger{}, ErrNotFound
	}
	if err != nil {
		return Trigger{}, fmt.Errorf("lock trigger: %w", err)
	}
	return toTrigger(row)
}

// ClearReasonAt clears why the trigger is disabled, leaving it disabled,
// at revision: ErrRevisionMismatch when it changed since, ErrNotFound when
// it is gone.
func (st *SQLStore) ClearReasonAt(ctx context.Context, orgID, id uuid.UUID, revision int64) (Trigger, error) {
	row, err := st.q.ClearTriggerDisabledReasonAt(ctx, gen.ClearTriggerDisabledReasonAtParams{OrgID: orgID, ID: id, Revision: revision})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := st.Get(ctx, orgID, id); gerr != nil {
			return Trigger{}, gerr
		}
		return Trigger{}, ErrRevisionMismatch
	}
	if err != nil {
		return Trigger{}, fmt.Errorf("clear trigger disabled reason: %w", err)
	}
	return toTrigger(row)
}

// ListForReview lists the triggers that run any of templateIDs or sync
// repoID (nil for none).
func (st *SQLStore) ListForReview(ctx context.Context, orgID uuid.UUID, templateIDs []uuid.UUID, repoID *uuid.UUID) ([]Trigger, error) {
	if templateIDs == nil {
		templateIDs = []uuid.UUID{}
	}
	rows, err := st.q.ListTriggersForReview(ctx, gen.ListTriggersForReviewParams{OrgID: orgID, TemplateIds: templateIDs, RepoID: db.NullUUID(repoID)})
	if err != nil {
		return nil, fmt.Errorf("list triggers for review: %w", err)
	}
	out := make([]Trigger, len(rows))
	for i, r := range rows {
		if out[i], err = toTrigger(r); err != nil {
			return nil, err
		}
	}
	return out, nil
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
	rows, err := st.q.ListTriggerDeliveries(ctx, gen.ListTriggerDeliveriesParams{OrgID: orgID, TriggerID: id, Lim: int32(limit)}) //nolint:gosec // clamped to 1-500 by Service.Deliveries
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	out := make([]Delivery, len(rows))
	for i, r := range rows {
		out[i] = Delivery{ID: r.ID, OrgID: r.OrgID, TriggerID: r.TriggerID, At: r.At, SourceIP: r.SourceIp,
			Outcome: Outcome(r.Outcome), Reason: r.Reason, JobGroupID: db.UUIDPtr(r.JobGroupID), PendingID: db.UUIDPtr(r.PendingID)}
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

// RecordDelivery appends a delivery and trims the trigger's log to its
// newest 500 rows. Throttling (one row per trigger, outcome and minute for
// the outcomes that need it) is enforced declaratively by
// trigger_deliveries_throttle_idx, a partial unique index the insert
// conflicts into — see InsertTriggerDelivery's own comment. The caller
// used to restate that decision as a bool, which this ignored: two
// statements of the same rule, one of them unread, free to drift apart
// (backlog 2.68). The outcome alone decides, here and in the index.
// When the insert is deduplicated (0 rows affected), the trim is
// skipped: nothing changed, so there is nothing to trim for (Task 9
// review, Minor 2).
func (st *SQLStore) RecordDelivery(ctx context.Context, d Delivery) error {
	return recordDelivery(ctx, st.q, d)
}

// recordDelivery is RecordDelivery on q (the store's own, or a
// transaction's: Collect records inside its upsert's transaction).
func recordDelivery(ctx context.Context, q *gen.Queries, d Delivery) error {
	rows, err := q.InsertTriggerDelivery(ctx, gen.InsertTriggerDeliveryParams{
		ID: d.ID, OrgID: d.OrgID, TriggerID: d.TriggerID, At: d.At, SourceIp: d.SourceIP,
		Outcome: string(d.Outcome), Reason: d.Reason, JobGroupID: db.NullUUID(d.JobGroupID), PendingID: db.NullUUID(d.PendingID),
	})
	if err != nil {
		return fmt.Errorf("record delivery: %w", err)
	}
	if rows == 0 {
		return nil
	}
	if err := q.TrimTriggerDeliveries(ctx, gen.TrimTriggerDeliveriesParams{OrgID: d.OrgID, TriggerID: d.TriggerID}); err != nil {
		return fmt.Errorf("trim deliveries: %w", err)
	}
	return nil
}

// NoteRateLimited is NoteDeliveryRateLimited (see its own comment): a
// no-op UPDATE matching no row is expected whenever outcome never has a
// stored row (OutcomeNotFound) or the owning org/trigger are the zero
// value (no trigger was ever resolved for this call), so it is never
// treated as an error here.
func (st *SQLStore) NoteRateLimited(ctx context.Context, orgID, triggerID uuid.UUID, outcome Outcome, at time.Time) error {
	if err := st.q.NoteDeliveryRateLimited(ctx, gen.NoteDeliveryRateLimitedParams{
		OrgID: orgID, TriggerID: triggerID, Outcome: string(outcome), At: at,
	}); err != nil {
		return fmt.Errorf("note rate-limited delivery: %w", err)
	}
	return nil
}

// int32Ptr binds an optional int (bounded by its validation) as int4.
func int32Ptr(v *int) *int32 {
	if v == nil {
		return nil
	}
	n := int32(*v) //nolint:gosec // callers pass validated, bounded values
	return &n
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

// toTrigger maps a stored row onto the domain type. It never copies
// `secret`, which is why the management queries can keep SELECT * —
// weighed and declined under backlog 2.68.
//
// Naming the columns in GetTrigger, ListTriggers and the two RETURNING
// clauses would stop the secret being read at all, at the cost of four
// more generated row types with identical fields and four adapters to
// keep in step: every future column would then have to be added in five
// places, and forgetting one is a silent omission rather than a
// compile error. What that buys is small, because the column is SEALED
// (migration 00026, AAD trigger:<org>:<id>) — a management read carries
// ciphertext bound to this trigger, not a usable secret, and the thing
// worth protecting is the box key. Resolve is the one caller that needs
// the value, and it opens it deliberately.
func toTrigger(r gen.JobTrigger) (Trigger, error) {
	t := Trigger{
		ID: r.ID, OrgID: r.OrgID, Kind: Kind(r.Kind), TemplateID: db.UUIDPtr(r.TemplateID), RepoID: db.UUIDPtr(r.RepoID),
		Name: r.Name, Enabled: r.Enabled, Preset: Preset(r.Preset), TimestampHeader: r.TimestampHeader,
		AllowCIDRs: r.AllowCidrs, OnActive: OnActive(r.OnActive), CreatedBy: db.UUIDPtr(r.CreatedBy),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Revision: r.Revision, SettleSeconds: int(r.SettleSeconds),
	}
	if r.MaxWaitSeconds != nil {
		v := int(*r.MaxWaitSeconds)
		t.MaxWaitSeconds = &v
	}
	if r.DisabledReason != nil {
		t.DisabledReason = *r.DisabledReason
	}
	if err := json.Unmarshal(r.Mapping, &t.Mapping); err != nil {
		return Trigger{}, fmt.Errorf("decode trigger mapping: %w", err)
	}
	if err := json.Unmarshal(r.RateLimit, &t.RateLimit); err != nil {
		return Trigger{}, fmt.Errorf("decode trigger rate limit: %w", err)
	}
	return t, nil
}
