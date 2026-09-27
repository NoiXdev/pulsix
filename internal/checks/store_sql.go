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

// SQLStore implements Store with sqlc queries on *db.DB.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on d itself, never on its raw pool, so
// every statement runs inside the organization bound to the request
// context (db.WithOrgScope) — the same posture hosts.SQLStore and
// maintenance.SQLStore document, and monitoring_groups' row-level security
// relies on.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

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
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
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
