package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB. d itself is kept,
// not only q, for the one multi-statement operation this package needs —
// RecordObservations' single transaction — the same shape jobs.SQLStore
// documents for CreateJobGroup.
type SQLStore struct {
	q *gen.Queries
	d *db.DB
}

// NewSQLStore builds the store on d itself, never on its raw pool, so
// every statement runs inside the organization bound to the request
// context (db.WithOrgScope) — the same posture hosts.SQLStore and
// maintenance.SQLStore document, and monitoring_groups' row-level security
// relies on.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d), d: d} }

func toGroup(r gen.MonitoringGroup) Group {
	return Group{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, Selector: r.Selector,
		Priority: int(r.Priority), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (s *SQLStore) Create(ctx context.Context, g Group) (Group, error) {
	row, err := s.q.CreateMonitoringGroup(ctx, gen.CreateMonitoringGroupParams{
		ID: g.ID, OrgID: g.OrgID, Name: g.Name, Selector: g.Selector, Priority: int32(g.Priority), //nolint:gosec // bounded to int32 by normalize
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Group{}, ErrNameTaken
		}
		return Group{}, fmt.Errorf("create monitoring group: %w", err)
	}
	return toGroup(row), nil
}

func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Group, error) {
	rows, err := s.q.ListMonitoringGroups(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list monitoring groups: %w", err)
	}
	out := make([]Group, len(rows))
	for i, r := range rows {
		out[i] = toGroup(r)
	}
	return out, nil
}

func (s *SQLStore) Update(ctx context.Context, g Group) (Group, error) {
	row, err := s.q.UpdateMonitoringGroup(ctx, gen.UpdateMonitoringGroupParams{
		Name: g.Name, Selector: g.Selector, Priority: int32(g.Priority), //nolint:gosec // bounded to int32 by normalize
		OrgID: g.OrgID, ID: g.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Group{}, ErrNotFound
		}
		if db.IsUniqueViolation(err) {
			return Group{}, ErrNameTaken
		}
		return Group{}, fmt.Errorf("update monitoring group: %w", err)
	}
	return toGroup(row), nil
}

func (s *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteMonitoringGroup(ctx, gen.DeleteMonitoringGroupParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete monitoring group: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// marshalParams encodes a definition's Params the same way
// agents.RecordFacts encodes host facts: nil becomes "{}", never NULL — the
// column is NOT NULL DEFAULT '{}'::jsonb, and every reader (toDefinition)
// can then assume a params map that is empty rather than absent.
func marshalParams(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func toDefinition(r gen.CheckDefinition) (Definition, error) {
	d := Definition{
		ID: r.ID, OrgID: r.OrgID, Key: r.Key, Plugin: r.Plugin,
		Interval: time.Duration(r.IntervalSeconds) * time.Second,
		Enabled:  r.Enabled, Origin: r.Origin, BindingKind: BindingKind(r.BindingKind),
		GroupID: db.UUIDPtr(r.GroupID), HostID: db.UUIDPtr(r.HostID),
		// SharedDefinitionID is never written by CreateCheckDefinition/
		// UpdateCheckDefinition (neither statement's column list mentions
		// it) — only internal/shared's own InsertProjectedCheckDefinition(s)
		// set it, directly in SQL, exactly like SharedRepoID/SharedTemplateID
		// on the other two kinds.
		SharedDefinitionID: db.UUIDPtr(r.SharedDefinitionID),
		CreatedAt:          r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if len(r.Params) > 0 {
		if err := json.Unmarshal(r.Params, &d.Params); err != nil {
			return Definition{}, fmt.Errorf("decode check definition params: %w", err)
		}
	}
	return d, nil
}

func (s *SQLStore) CreateDefinition(ctx context.Context, d Definition) (Definition, error) {
	params, err := marshalParams(d.Params)
	if err != nil {
		return Definition{}, fmt.Errorf("encode check definition params: %w", err)
	}
	row, err := s.q.CreateCheckDefinition(ctx, gen.CreateCheckDefinitionParams{
		ID: d.ID, OrgID: d.OrgID, Key: d.Key, Plugin: d.Plugin, Params: params,
		IntervalSeconds: int32(d.Interval / time.Second), //nolint:gosec // bounded to [10s, 24h] by normalizeDefinition
		Enabled:         d.Enabled, Origin: d.Origin, BindingKind: string(d.BindingKind),
		GroupID: db.NullUUID(d.GroupID), HostID: db.NullUUID(d.HostID),
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Definition{}, ErrKeyTaken
		}
		if db.IsForeignKeyViolation(err) {
			return Definition{}, fmt.Errorf("%w: the referenced group or host does not exist", ErrInvalidInput)
		}
		return Definition{}, fmt.Errorf("create check definition: %w", err)
	}
	return toDefinition(row)
}

func (s *SQLStore) GetDefinition(ctx context.Context, orgID, id uuid.UUID) (Definition, error) {
	row, err := s.q.GetCheckDefinition(ctx, gen.GetCheckDefinitionParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Definition{}, ErrNotFound
	}
	if err != nil {
		return Definition{}, fmt.Errorf("get check definition: %w", err)
	}
	return toDefinition(row)
}

func (s *SQLStore) ListDefinitions(ctx context.Context, orgID uuid.UUID) ([]Definition, error) {
	rows, err := s.q.ListCheckDefinitions(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list check definitions: %w", err)
	}
	out := make([]Definition, len(rows))
	for i, r := range rows {
		d, err := toDefinition(r)
		if err != nil {
			return nil, err
		}
		out[i] = d
	}
	return out, nil
}

func (s *SQLStore) UpdateDefinition(ctx context.Context, d Definition) (Definition, error) {
	params, err := marshalParams(d.Params)
	if err != nil {
		return Definition{}, fmt.Errorf("encode check definition params: %w", err)
	}
	row, err := s.q.UpdateCheckDefinition(ctx, gen.UpdateCheckDefinitionParams{
		Key: d.Key, Plugin: d.Plugin, Params: params,
		IntervalSeconds: int32(d.Interval / time.Second), //nolint:gosec // bounded to [10s, 24h] by normalizeDefinition
		Enabled:         d.Enabled, BindingKind: string(d.BindingKind),
		GroupID: db.NullUUID(d.GroupID), HostID: db.NullUUID(d.HostID),
		OrgID: d.OrgID, ID: d.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Definition{}, ErrNotFound
		}
		if db.IsUniqueViolation(err) {
			return Definition{}, ErrKeyTaken
		}
		if db.IsForeignKeyViolation(err) {
			return Definition{}, fmt.Errorf("%w: the referenced group or host does not exist", ErrInvalidInput)
		}
		return Definition{}, fmt.Errorf("update check definition: %w", err)
	}
	return toDefinition(row)
}

func (s *SQLStore) DeleteDefinition(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteCheckDefinition(ctx, gen.DeleteCheckDefinitionParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete check definition: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// errPtr converts Observation/Current's Err ("" means none) to the
// pointer check_current's nullable error column takes.
func errPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// errString reverses errPtr.
func errString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func toCurrent(r gen.CheckCurrent) Current {
	return Current{
		CheckKey: r.CheckKey, Item: r.Item, Metric: r.Metric,
		At: r.At, Value: r.Value, Label: r.Label, Err: errString(r.Error),
	}
}

// RecordObservations writes obs (already validated by the service —
// plausibility window, well-shaped value/label/err) in one transaction:
// check_samples rows inserted, check_current rows upserted, both
// committing together, so a batch that fails partway never leaves samples
// without their matching current value or vice versa.
//
// Before writing anything it looks up which of obs' host ids actually
// belong to orgID (HostsExisting) and skips the rest, counting them in
// rejected. This is deliberately a lookup-and-skip, not reliance on the
// (org_id, host_id) foreign key on check_samples/check_current: a
// constraint violation would abort the whole transaction on the first bad
// row, failing every other Observation in the batch along with it — the
// opposite of "one unknown host must not fail the rest of its batch".
func (s *SQLStore) RecordObservations(ctx context.Context, orgID uuid.UUID, obs []Observation) (accepted, rejected int, err error) {
	// Grouped by host, in first-seen order: each host's own rows are
	// written inside their own savepoint (recordHostObservations), so a
	// foreign-key violation on one host's insert can never take another
	// host's rows down with it.
	byHost := make(map[uuid.UUID][]Observation, len(obs))
	hostIDs := make([]uuid.UUID, 0, len(obs))
	for _, o := range obs {
		if _, seen := byHost[o.HostID]; !seen {
			hostIDs = append(hostIDs, o.HostID)
		}
		byHost[o.HostID] = append(byHost[o.HostID], o)
	}

	err = s.d.WithOrg(ctx, orgID, func(tx pgx.Tx) error {
		q := gen.New(tx)
		existingRows, err := q.HostsExisting(ctx, gen.HostsExistingParams{OrgID: orgID, HostIds: hostIDs})
		if err != nil {
			return fmt.Errorf("check existing hosts: %w", err)
		}
		exists := make(map[uuid.UUID]struct{}, len(existingRows))
		for _, id := range existingRows {
			exists[id] = struct{}{}
		}

		for _, hostID := range hostIDs {
			hostObs := byHost[hostID]
			if _, ok := exists[hostID]; !ok {
				rejected += len(hostObs)
				continue
			}
			ok, err := recordHostObservations(ctx, tx, orgID, hostID, hostObs)
			if err != nil {
				return err
			}
			if ok {
				accepted += len(hostObs)
			} else {
				rejected += len(hostObs)
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return accepted, rejected, nil
}

// recordHostObservations writes hostObs — all for hostID, already
// confirmed to exist at the top of RecordObservations' own transaction —
// inside a savepoint of tx (internal/db/scope.go: a pgx.Tx.Begin nested in
// a bound transaction is a savepoint, not a fresh transaction). A
// foreign-key violation here means the host was deleted in the narrow
// window between that existence check and this insert — a real, if rare,
// race, not merely a defensive belt-and-braces check — and is handled
// exactly like "the host does not exist": ok=false, err=nil, the
// savepoint rolled back so none of hostObs' rows persist, and the outer
// transaction (and every other host's already-committed savepoint) left
// untouched. Any other error is a genuine store failure and propagates,
// aborting the whole batch — the same posture every other unexpected
// store error already has.
func recordHostObservations(ctx context.Context, tx pgx.Tx, orgID, hostID uuid.UUID, hostObs []Observation) (ok bool, err error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin host savepoint: %w", err)
	}
	q := gen.New(sp)
	for _, o := range hostObs {
		if o.Err == "" {
			if err := q.InsertCheckSample(ctx, gen.InsertCheckSampleParams{
				At: o.At, OrgID: orgID, HostID: o.HostID, CheckKey: o.CheckKey, Item: o.Item, Metric: o.Metric,
				Value: o.Value, Label: o.Label,
			}); err != nil {
				_ = sp.Rollback(ctx)
				if db.IsForeignKeyViolation(err) {
					return false, nil
				}
				return false, fmt.Errorf("insert check sample: %w", err)
			}
		}
		if err := q.UpsertCheckCurrent(ctx, gen.UpsertCheckCurrentParams{
			OrgID: orgID, HostID: o.HostID, CheckKey: o.CheckKey, Item: o.Item, Metric: o.Metric,
			Value: o.Value, Label: o.Label, At: o.At, Error: errPtr(o.Err),
		}); err != nil {
			_ = sp.Rollback(ctx)
			if db.IsForeignKeyViolation(err) {
				return false, nil
			}
			return false, fmt.Errorf("upsert check current: %w", err)
		}
	}
	// The evaluation mark (v0.3b-2 spec, "Trigger"): one single-row upsert
	// per host per batch, telling the rule engine's ticker that this host
	// has samples it has not judged yet. It belongs inside this savepoint
	// and not after the transaction for two reasons: a mark written after
	// the commit could be lost (the batch would be stored and never
	// evaluated), and a host whose rows roll back here — one deleted in the
	// window between the existence check and these inserts — must not leave
	// a cursor behind claiming it has new data. It is one statement per
	// host, never one per observation, because the ticker only needs to
	// know THAT the host changed. check_eval_cursor belongs to
	// internal/rules; this is a call to a generated query, not a dependency
	// on that package (see samples.go's own note).
	if err := q.MarkHostDirty(ctx, gen.MarkHostDirtyParams{OrgID: orgID, HostID: hostID}); err != nil {
		_ = sp.Rollback(ctx)
		if db.IsForeignKeyViolation(err) {
			return false, nil
		}
		return false, fmt.Errorf("mark host dirty: %w", err)
	}
	if err := sp.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit host savepoint: %w", err)
	}
	return true, nil
}

// CurrentForHost returns hostID's latest known value per (check_key,
// item, metric), rows never deleted (Current's own doc comment).
func (s *SQLStore) CurrentForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]Current, error) {
	rows, err := s.q.ListCheckCurrent(ctx, gen.ListCheckCurrentParams{OrgID: orgID, HostID: hostID})
	if err != nil {
		return nil, fmt.Errorf("list check current: %w", err)
	}
	out := make([]Current, len(rows))
	for i, r := range rows {
		out[i] = toCurrent(r)
	}
	return out, nil
}

// SeriesForHost downsamples check_samples server-side into at most
// buckets points via time_bucket; bucket width is (to-from)/buckets,
// computed here in floating-point seconds rather than a Go time.Duration
// so it isn't rounded to the nearest nanosecond-representable duration
// before reaching PostgreSQL.
func (s *SQLStore) SeriesForHost(ctx context.Context, orgID, hostID uuid.UUID, checkKey, item, metric string, from, to time.Time, buckets int) (Series, error) {
	widthSeconds := to.Sub(from).Seconds() / float64(buckets)
	rows, err := s.q.SeriesForHost(ctx, gen.SeriesForHostParams{
		BucketSeconds: widthSeconds, OrgID: orgID, HostID: hostID,
		CheckKey: checkKey, Item: item, Metric: metric, FromAt: from, ToAt: to,
	})
	if err != nil {
		return Series{}, fmt.Errorf("series for host: %w", err)
	}
	points := make([]Point, len(rows))
	for i, r := range rows {
		points[i] = Point{At: r.Bucket, Value: r.Value}
	}
	return Series{Metric: metric, Points: points}, nil
}
