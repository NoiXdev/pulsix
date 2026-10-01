package items

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// ErrNotFound means no decision row exists for a given (org, id) pair — the
// same sentinel shape every other domain store in this codebase returns
// (see rules.ErrNotFound's own doc comment). There is no not-found case for
// an item: check_items rows are discovered, never looked up by identity
// alone.
var ErrNotFound = errors.New("not found")

// Item is one (host, check_key, item) tuple the evaluation ticker has
// discovered — check_items' own row (v0.3b-3 design, "check_items — what
// the machine found"). Name holds what the database column `item` holds:
// the column keeps that name because it is the word the spec, the schema
// and the operator use, while Go calls the field Name so that View, which
// embeds Item, never reads as View.Item.Item.
type Item struct {
	OrgID, HostID  uuid.UUID
	CheckKey, Name string
	FirstSeen      time.Time
}

// Seen is one item the ticker observed on a host this tick — Discover's
// own input shape, carrying nothing an insert does not need.
type Seen struct {
	CheckKey, Name string
}

// View is an item plus what the read side needs: when it was last
// reported — max(at) over that tuple's check_current rows, and therefore
// never stored (check_items carries no last_seen column; see migration
// 00048's own comment for why) — and whether it is being reported at all.
// LastSeen is the Go zero time.Time when Reported is false; a caller must
// check Reported first, never infer "never reported" from a zero
// timestamp alone.
type View struct {
	Item
	LastSeen time.Time
	Reported bool
}

// Filter narrows ItemsForOrg. The zero Filter matches every item in the
// organization.
type Filter struct {
	// CheckKey, when non-empty, restricts the result to one check.
	CheckKey string
	// ItemGlob, when non-empty, is the same one-character '*' glob Match
	// implements, translated to a SQL LIKE pattern at the store (see
	// globToLike and queries/items.sql's own comment on the translation).
	ItemGlob string
}

// Store is discovery and decisions' storage: check_items (Discover,
// ItemsForHost, ItemsForOrg) and check_item_decisions (everything else).
// Both tables belong to the same migration (00048) and the same package;
// a caller that only needs one half is free to use it and ignore the
// other.
type Store interface {
	// Discover inserts every item of items it has not seen before, and
	// leaves an existing row exactly as it is — first_seen never moves.
	// Calling it with an empty slice is a valid no-op.
	Discover(ctx context.Context, orgID, hostID uuid.UUID, items []Seen) error
	// ItemsForHost returns every item ever discovered on hostID, each with
	// its own last-reported moment.
	ItemsForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]View, error)
	// ItemsForOrg returns every item discovered anywhere in the
	// organization, across every host, narrowed by f.
	ItemsForOrg(ctx context.Context, orgID uuid.UUID, f Filter) ([]View, error)

	// Decisions returns the organization's decisions plus, when hostID is
	// non-nil, that host's — the input Decide resolves per item. One
	// query regardless of hostID, since the evaluator calls this once per
	// host and must not pay per item.
	Decisions(ctx context.Context, orgID uuid.UUID, hostID *uuid.UUID) ([]Decision, error)
	CreateDecision(ctx context.Context, d Decision) error
	UpdateDecision(ctx context.Context, d Decision) error
	DeleteDecision(ctx context.Context, orgID, id uuid.UUID) error
	GetDecision(ctx context.Context, orgID, id uuid.UUID) (Decision, error)
	ListDecisions(ctx context.Context, orgID uuid.UUID) ([]Decision, error)
}

// SQLStore implements Store with sqlc queries on *db.DB, run through
// whatever organization scope the caller's context already carries
// (db.WithOrgScope) — the posture migration 00048's row-level security
// pairs rely on, the same as every other domain store in this codebase.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store on d itself, never its raw pool, so every
// statement runs inside the request's bound organization.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

// Discover writes each of items via InsertCheckItem's own ON CONFLICT DO
// NOTHING — one statement per item, not a single batched insert, because
// the common case is a handful of items already known (every tick after
// the first) and a failed primary-key probe per item is cheap. Discover
// does not open a transaction of its own: it is designed to run inside
// the evaluation ticker's own per-host transaction (a later task), so a
// crash mid-batch leaves exactly the items already committed to that
// transaction and nothing is duplicated when the tick retries.
func (s *SQLStore) Discover(ctx context.Context, orgID, hostID uuid.UUID, seen []Seen) error {
	for _, it := range seen {
		if err := s.q.InsertCheckItem(ctx, gen.InsertCheckItemParams{
			OrgID: orgID, HostID: hostID, CheckKey: it.CheckKey, Item: it.Name,
		}); err != nil {
			return fmt.Errorf("discover items: %w", err)
		}
	}
	return nil
}

// toView adapts one joined row — the shape ListItemsForHost and
// ListItemsForOrg's generated row types share field-for-field — into a
// View. The sentinel last_seen the query's COALESCE produces for an
// unreported item (see queries/items.sql's own comment) is discarded here
// in favour of the Go zero time.Time, so no SQL-only sentinel ever
// reaches a caller.
func toView(orgID, hostID uuid.UUID, checkKey, item string, firstSeen, lastSeen time.Time, reported bool) View {
	v := View{
		Item:     Item{OrgID: orgID, HostID: hostID, CheckKey: checkKey, Name: item, FirstSeen: firstSeen},
		Reported: reported,
	}
	if reported {
		v.LastSeen = lastSeen
	}
	return v
}

func (s *SQLStore) ItemsForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]View, error) {
	rows, err := s.q.ListItemsForHost(ctx, gen.ListItemsForHostParams{OrgID: orgID, HostID: hostID})
	if err != nil {
		return nil, fmt.Errorf("list items for host: %w", err)
	}
	out := make([]View, len(rows))
	for i, r := range rows {
		out[i] = toView(r.OrgID, r.HostID, r.CheckKey, r.Item, r.FirstSeen, r.LastSeen, r.Reported)
	}
	return out, nil
}

// likeEscape is the LIKE escape character globToLike uses and
// queries/items.sql's ListItemsForOrg statement names explicitly
// (ESCAPE '\'), so a literal backslash in an operator's own pattern is
// never mistaken for the start of an escape sequence by accident.
const likeEscape = '\\'

// globToLike translates one items.Match pattern into the SQL LIKE pattern
// that matches exactly the same strings. '*' becomes '%' — LIKE's own
// wildcard for "any run of characters, including none", exactly Match's
// semantics for '*' — and every character LIKE would otherwise treat
// specially in the OPERATOR's OWN pattern ('%', '_' and the escape
// character itself) is escaped so it stays a literal instead of becoming
// a second, accidental wildcard.
//
// This translation is exact, not approximate, because our glob
// (items.Match's own doc comment) is a strict subset of LIKE: one
// metacharacter, always anchored at both ends, no '?' and no character
// class of its own. The two syntaxes can therefore never drift apart
// without a change to Match itself — and if that ever happens, this
// function and ListItemsForOrg's own comment on the same translation are
// the first things to revisit.
func globToLike(pattern string) string {
	var b strings.Builder
	b.Grow(len(pattern))
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteByte('%')
		case '%', '_', likeEscape:
			b.WriteRune(likeEscape)
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nullableString adapts a Filter field ("" means unset) to the *string a
// sqlc.narg text parameter takes — the same idiom rules.nullableString
// already uses for Filter's own optional fields.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *SQLStore) ItemsForOrg(ctx context.Context, orgID uuid.UUID, f Filter) ([]View, error) {
	var itemLike *string
	if f.ItemGlob != "" {
		like := globToLike(f.ItemGlob)
		itemLike = &like
	}
	rows, err := s.q.ListItemsForOrg(ctx, gen.ListItemsForOrgParams{
		OrgID: orgID, CheckKey: nullableString(f.CheckKey), ItemLike: itemLike,
	})
	if err != nil {
		return nil, fmt.Errorf("list items for org: %w", err)
	}
	out := make([]View, len(rows))
	for i, r := range rows {
		out[i] = toView(r.OrgID, r.HostID, r.CheckKey, r.Item, r.FirstSeen, r.LastSeen, r.Reported)
	}
	return out, nil
}

// toDecision adapts one check_item_decisions row to a Decision.
func toDecision(r gen.CheckItemDecision) Decision {
	return Decision{
		ID: r.ID, OrgID: r.OrgID, Scope: Scope(r.Scope), HostID: db.UUIDPtr(r.HostID),
		CheckKey: r.CheckKey, MatchKind: MatchKind(r.MatchKind), Pattern: r.Pattern,
		Verdict: Verdict(r.Decision), Note: r.Note, CreatedBy: db.UUIDPtr(r.CreatedBy),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (s *SQLStore) Decisions(ctx context.Context, orgID uuid.UUID, hostID *uuid.UUID) ([]Decision, error) {
	rows, err := s.q.ListItemDecisionsForEval(ctx, gen.ListItemDecisionsForEvalParams{
		OrgID: orgID, HostID: db.NullUUID(hostID),
	})
	if err != nil {
		return nil, fmt.Errorf("list decisions for evaluation: %w", err)
	}
	out := make([]Decision, len(rows))
	for i, r := range rows {
		out[i] = toDecision(r)
	}
	return out, nil
}

func (s *SQLStore) CreateDecision(ctx context.Context, d Decision) error {
	err := s.q.CreateItemDecision(ctx, gen.CreateItemDecisionParams{
		ID: d.ID, OrgID: d.OrgID, Scope: string(d.Scope), HostID: db.NullUUID(d.HostID),
		CheckKey: d.CheckKey, MatchKind: string(d.MatchKind), Pattern: d.Pattern,
		Decision: string(d.Verdict), Note: d.Note, CreatedBy: db.NullUUID(d.CreatedBy),
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: the referenced host does not exist", ErrInvalidInput)
		}
		return fmt.Errorf("create decision: %w", err)
	}
	return nil
}

func (s *SQLStore) UpdateDecision(ctx context.Context, d Decision) error {
	n, err := s.q.UpdateItemDecision(ctx, gen.UpdateItemDecisionParams{
		Scope: string(d.Scope), HostID: db.NullUUID(d.HostID), CheckKey: d.CheckKey,
		MatchKind: string(d.MatchKind), Pattern: d.Pattern, Decision: string(d.Verdict),
		Note: d.Note, UpdatedAt: d.UpdatedAt, OrgID: d.OrgID, ID: d.ID,
	})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: the referenced host does not exist", ErrInvalidInput)
		}
		return fmt.Errorf("update decision: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) DeleteDecision(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteItemDecision(ctx, gen.DeleteItemDecisionParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete decision: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) GetDecision(ctx context.Context, orgID, id uuid.UUID) (Decision, error) {
	row, err := s.q.GetItemDecision(ctx, gen.GetItemDecisionParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, ErrNotFound
	}
	if err != nil {
		return Decision{}, fmt.Errorf("get decision: %w", err)
	}
	return toDecision(row), nil
}

func (s *SQLStore) ListDecisions(ctx context.Context, orgID uuid.UUID) ([]Decision, error) {
	rows, err := s.q.ListItemDecisions(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list decisions: %w", err)
	}
	out := make([]Decision, len(rows))
	for i, r := range rows {
		out[i] = toDecision(r)
	}
	return out, nil
}
