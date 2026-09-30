package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store and StateStore with sqlc queries on *db.DB, run
// through whatever organization scope the caller's context already carries
// (db.WithOrgScope) — the posture migration 00045's row-level security
// pair relies on, the same as every other domain store in this codebase.
//
// db is used directly only by HostStates (states_store.go): every other
// method goes through q, the ordinary sqlc-generated path, but HostStates'
// WHERE clause is built at runtime from FleetFilter's tag and group
// conditions, which sqlc's static-query generation has no way to express —
// the same reason hosts.SQLStore.QueryHosts bypasses q for its own
// selector-built query.
type SQLStore struct {
	q  *gen.Queries
	db *db.DB
}

// NewSQLStore builds the store on d itself, never its raw pool, so every
// statement — sqlc-generated or hand-written — runs inside the request's
// bound organization.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d), db: d} }

// marshalNode encodes a trigger tree, always present, so never nil.
func marshalNode(n Node) ([]byte, error) { return json.Marshal(n) }

// marshalClear encodes a clear tree into the nullable clear_ast column: the
// zero Node (Form.Compile and compileRule's own convention for "no clear
// condition") marshals to SQL NULL, not the literal JSON object "{}" —
// decodeNode's "empty bytes" branch below restores the same zero value on
// read, since a NULL column scans into a zero-length []byte.
func marshalClear(n Node) ([]byte, error) {
	if n.Kind == "" {
		return nil, nil
	}
	return json.Marshal(n)
}

func decodeNode(b []byte) (Node, error) {
	var n Node
	if len(b) == 0 {
		return Node{}, nil
	}
	if err := json.Unmarshal(b, &n); err != nil {
		return Node{}, err
	}
	return n, nil
}

// toInterval converts a validated for_duration (Service.buildRule bounds
// it to [0, 24h], which never carries a month or day component) into the
// pgtype.Interval sqlc generates for the column's "interval" type.
func toInterval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

// fromInterval reverses toInterval.
func fromInterval(i pgtype.Interval) time.Duration {
	return time.Duration(i.Microseconds) * time.Microsecond
}

func toRule(r gen.CheckRule) (Rule, error) {
	trigger, err := decodeNode(r.Ast)
	if err != nil {
		return Rule{}, fmt.Errorf("decode rule ast: %w", err)
	}
	clear, err := decodeNode(r.ClearAst)
	if err != nil {
		return Rule{}, fmt.Errorf("decode rule clear_ast: %w", err)
	}
	return Rule{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, Enabled: r.Enabled, Origin: r.Origin,
		Scope: Scope(r.Scope), GroupID: db.UUIDPtr(r.GroupID), HostID: db.UUIDPtr(r.HostID),
		Item: r.Item, CheckKey: r.CheckKey, Metric: r.Metric, Severity: Severity(r.Severity),
		Mode: r.Mode, Trigger: trigger, Clear: clear, Source: r.Source,
		For: fromInterval(r.ForDuration), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

func (s *SQLStore) Create(ctx context.Context, r Rule) error {
	ast, err := marshalNode(r.Trigger)
	if err != nil {
		return fmt.Errorf("encode rule ast: %w", err)
	}
	clearAst, err := marshalClear(r.Clear)
	if err != nil {
		return fmt.Errorf("encode rule clear_ast: %w", err)
	}
	err = s.q.CreateRule(ctx, gen.CreateRuleParams{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, Enabled: r.Enabled, Origin: r.Origin,
		Scope: string(r.Scope), GroupID: db.NullUUID(r.GroupID), HostID: db.NullUUID(r.HostID),
		Item: r.Item, CheckKey: r.CheckKey, Metric: r.Metric, Severity: string(r.Severity),
		Mode: r.Mode, Ast: ast, ClearAst: clearAst, Source: r.Source, ForDuration: toInterval(r.For),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: the referenced group or host does not exist", ErrInvalidInput)
		}
		return fmt.Errorf("create rule: %w", err)
	}
	return nil
}

func (s *SQLStore) Update(ctx context.Context, r Rule) error {
	ast, err := marshalNode(r.Trigger)
	if err != nil {
		return fmt.Errorf("encode rule ast: %w", err)
	}
	clearAst, err := marshalClear(r.Clear)
	if err != nil {
		return fmt.Errorf("encode rule clear_ast: %w", err)
	}
	n, err := s.q.UpdateRule(ctx, gen.UpdateRuleParams{
		Name: r.Name, Enabled: r.Enabled, Scope: string(r.Scope),
		GroupID: db.NullUUID(r.GroupID), HostID: db.NullUUID(r.HostID), Item: r.Item,
		CheckKey: r.CheckKey, Metric: r.Metric, Severity: string(r.Severity),
		Mode: r.Mode, Ast: ast, ClearAst: clearAst, Source: r.Source, ForDuration: toInterval(r.For),
		UpdatedAt: r.UpdatedAt, OrgID: r.OrgID, ID: r.ID,
	})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: the referenced group or host does not exist", ErrInvalidInput)
		}
		return fmt.Errorf("update rule: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteRule(ctx, gen.DeleteRuleParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete rule: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Rule, error) {
	row, err := s.q.GetRule(ctx, gen.GetRuleParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, ErrNotFound
	}
	if err != nil {
		return Rule{}, fmt.Errorf("get rule: %w", err)
	}
	return toRule(row)
}

// nullableString adapts a Filter field ("" means unset) to the *string a
// sqlc.narg text parameter takes.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID, f Filter) ([]Rule, error) {
	rows, err := s.q.ListRules(ctx, gen.ListRulesParams{
		OrgID: orgID, Scope: nullableString(string(f.Scope)), CheckKey: nullableString(f.CheckKey),
		HostID: db.NullUUID(f.HostID),
	})
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	out := make([]Rule, len(rows))
	for i, row := range rows {
		r, err := toRule(row)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}
