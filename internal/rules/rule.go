// This file gives rules a home: the persisted shape (Rule), the
// caller-supplied shape used to create or replace one (RuleInput), and the
// Store this package's Service persists through. ast.go, catalog.go,
// parse.go and form.go (Tasks 1-3) stay pure — nothing here changes them,
// this only stores the trees they already know how to build and check.
package rules

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Scope names the level a rule binds at, narrowest last — the same order
// the design's precedence table (item > host > group > org) resolves by.
type Scope string

const (
	ScopeOrg   Scope = "org"
	ScopeGroup Scope = "group"
	ScopeHost  Scope = "host"
	ScopeItem  Scope = "item"
)

// Severity is the one outcome a rule judges towards. A rule judges exactly
// one metric with exactly one severity (the migration's own doc comment):
// "disk" warn and "disk" crit are two rows, never one row with two
// thresholds, so a later precedence resolution never has to untangle a row
// that won on one severity from the same row losing on the other.
type Severity string

const (
	SeverityWarn Severity = "warn"
	SeverityCrit Severity = "crit"
)

// The two values Rule.Origin and RuleInput's implicit origin ever take.
// Unexported: Create always stamps originUser, and nothing outside a
// seeding path (Task 5) ever stamps originSeed — there is no exported way
// to ask for a seeded rule, the same posture checks.Definition's origin
// takes for its own seed set.
const (
	originSeed = "seed"
	originUser = "user"
)

// The two values Rule.Mode and RuleInput.Mode ever take. Unexported for the
// same reason origin's constants are: Mode is a plain string in the
// exported shape (documented inline, not a type of its own), so nothing
// outside this file needs the constant names.
const (
	modeForm       = "form"
	modeExpression = "expression"
)

// Rule is one judgment over one metric: a scope it binds at, the tree it
// evaluates (Trigger), an optional clear condition for hysteresis (Clear),
// and how long a deterioration must hold before it counts (For).
//
// CheckKey is derived, never caller-supplied: the service looks Metric up
// in Catalogue() and stores that entry's own check key, so a rule can never
// claim a metric belongs to a check it does not (see Service.Create's own
// comment).
type Rule struct {
	ID, OrgID uuid.UUID
	Name      string
	Enabled   bool
	Origin    string // "seed" | "user"
	Scope     Scope
	GroupID   *uuid.UUID
	HostID    *uuid.UUID
	Item      string
	CheckKey  string
	Metric    string
	Severity  Severity
	Mode      string // "form" | "expression"
	Trigger   Node
	Clear     Node // the zero Node when the rule has no clear condition
	Source    string
	For       time.Duration

	CreatedAt, UpdatedAt time.Time
}

// RuleInput is the caller-supplied shape for creating or fully replacing a
// rule. Mode decides which of Form and Expression the service reads: it
// compiles a Form through Form.Compile (Task 3) in "form" mode, or parses
// Expression through Parse (Task 2) in "expression" mode. Setting both, or
// neither, is refused — see Service.Create's validation.
type RuleInput struct {
	Name       string
	Enabled    bool
	Scope      Scope
	GroupID    *uuid.UUID
	HostID     *uuid.UUID
	Item       string
	Metric     string
	Severity   Severity
	Mode       string
	Form       *Form  // mode "form"
	Expression string // mode "expression"
	For        time.Duration
}

// Filter narrows List's result. A zero field means "no filter on this
// dimension" — Scope == "", CheckKey == "" or HostID == nil each pass
// everything through on their own axis.
type Filter struct {
	Scope    Scope
	CheckKey string
	HostID   *uuid.UUID
}

// Checked is Check's answer: whether in compiles at all (OK), where it
// failed when it does not (Pos, Message — a byte offset and a message an
// editor can put under the caret, exactly what *ParseError and
// *TypeCheckError themselves carry), and whether the resulting tree still
// fits the form editor's subset (FormReady, see AsForm) — the "switching to
// expression mode is one-way" warning's own data source. Source is what
// Create/Update would store: a rendered summary in form mode, the
// caller's own text in expression mode.
type Checked struct {
	OK        bool
	Pos       int
	Message   string
	FormReady bool
	Source    string
}

// Sentinel errors mapped to HTTP status by the transport layer, the same
// shape every other domain service in this codebase uses (see e.g.
// checks.ErrInvalidInput's own doc comment).
var (
	// ErrInvalidInput means in itself is malformed: an out-of-range field,
	// a scope/binding mismatch, or a rule tree ErrInvalidRule, *ParseError
	// or *TypeCheckError would otherwise have rejected. Create, Update and
	// Check all wrap those three into this one sentinel — never leaking
	// them — so the HTTP layer (a later task) maps exactly one sentinel to
	// 422 while Checked.Pos still carries the caret position the editor
	// needs. Never a store failure.
	ErrInvalidInput = errors.New("invalid input")
	// ErrNotFound means no genuine row exists for the given (org, id) pair
	// — including one that belongs to a different organization, which
	// reads exactly the same as one that does not exist at all.
	ErrNotFound = errors.New("not found")
)

// Store persists rules (org-scoped). Create and Update return only an
// error, not the row the database now holds: unlike checks.Store's groups
// and definitions, nothing here round-trips through a RETURNING clause
// stamped by the database's own now() — the service's injected clock
// (WithClock) stamps CreatedAt/UpdatedAt itself before calling either
// method, and hands the same Rule value back to its own caller.
type Store interface {
	// Create inserts r. r.ID, r.OrgID, r.Origin, r.CreatedAt and
	// r.UpdatedAt are already set by the caller.
	Create(ctx context.Context, r Rule) error
	// Update replaces every mutable column of the rule identified by
	// (r.OrgID, r.ID); ErrNotFound if no such row exists. r.CreatedAt is
	// carried through unchanged by the caller, not rewritten here.
	Update(ctx context.Context, r Rule) error
	// Delete removes the rule identified by (orgID, id); ErrNotFound if no
	// such row exists.
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	// Get returns the single rule identified by (orgID, id); ErrNotFound if
	// no such row exists — including one belonging to another organization.
	Get(ctx context.Context, orgID, id uuid.UUID) (Rule, error)
	// List returns orgID's rules matching f.
	List(ctx context.Context, orgID uuid.UUID, f Filter) ([]Rule, error)
}
