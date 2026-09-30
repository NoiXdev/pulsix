package rules

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// maxNameRunes bounds a rule's name, the same 100-rune shape
// checks.Group.Name and maintenance windows already use.
const maxNameRunes = 100

// maxForDuration is the design's own bound on for_duration (docs/
// superpowers/specs/2026-09-29-v0.3b-2-rules-design.md, "Data model"):
// deterioration can be made to wait, but never for longer than a day.
const maxForDuration = 24 * time.Hour

// Service validates and persists rules. It carries a clock because, unlike
// checks.Service's groups and definitions, Store.Create/Update never hand
// a database-stamped row back (see Store's own doc comment) — the
// timestamps a caller sees are exactly the ones this Service stamped
// before calling the store.
type Service struct {
	store Store
	rand  io.Reader
	now   func() time.Time
}

// Option configures a Service.
type Option func(*Service)

// WithRand injects the entropy source for ids.
func WithRand(r io.Reader) Option { return func(s *Service) { s.rand = r } }

// WithClock injects the clock Create/Update stamp CreatedAt/UpdatedAt
// against; time.Now is the default. Tests pin timestamps exactly.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// NewService wires store.
func NewService(store Store, opts ...Option) *Service {
	s := &Service{store: store, rand: rand.Reader, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Service) newID() uuid.UUID {
	id, err := uuid.NewV7FromReader(s.rand)
	if err != nil {
		panic(err) //nolint:forbidigo // entropy failure is unrecoverable, like uuid.Must
	}
	return id
}

// invalidf builds an ErrInvalidInput failure carrying no byte position —
// every validation failure below except a rule-tree compile failure (see
// ruleError) is one of these.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}

// ruleError converts a Parse or Form.Compile failure into this service's
// own ErrInvalidInput, extracting a byte offset when the underlying error
// carries one (*ParseError, and *TypeCheckError should TypeCheck ever be
// called directly here in the future). ErrInvalidRule, *ParseError and
// *TypeCheckError must never leak past this function — see ErrInvalidInput's
// own doc comment for why: the HTTP layer maps exactly one sentinel to 422,
// and a caller that needs the position reads it from the returned pos, not
// from unwrapping a second error type.
func ruleError(err error) (pos int, wrapped error) {
	var pe *ParseError
	if errors.As(err, &pe) {
		return pe.Pos, invalidf("%s", pe.Msg)
	}
	var tce *TypeCheckError
	if errors.As(err, &tce) {
		return tce.Pos, invalidf("%s", tce.Msg)
	}
	return 0, invalidf("%s", err.Error())
}

// validateScope checks in's scope against the columns it requires —
// exactly the CHECK constraint migration 00045 enforces in the database,
// checked here first so a mismatch is ErrInvalidInput with a clear message
// rather than an opaque constraint-violation error from the store.
func validateScope(in RuleInput) error {
	switch in.Scope {
	case ScopeOrg:
		if in.GroupID != nil || in.HostID != nil {
			return invalidf("scope %q must not set a group or host", ScopeOrg)
		}
		if in.Item != "" {
			return invalidf("scope %q must not set an item", ScopeOrg)
		}
	case ScopeGroup:
		if in.GroupID == nil || in.HostID != nil {
			return invalidf("scope %q must set exactly a group", ScopeGroup)
		}
		if in.Item != "" {
			return invalidf("scope %q must not set an item", ScopeGroup)
		}
	case ScopeHost:
		if in.HostID == nil || in.GroupID != nil {
			return invalidf("scope %q must set exactly a host", ScopeHost)
		}
		if in.Item != "" {
			return invalidf("scope %q must not set an item; use scope %q for that", ScopeHost, ScopeItem)
		}
	case ScopeItem:
		if in.HostID == nil || in.GroupID != nil {
			return invalidf("scope %q must set exactly a host", ScopeItem)
		}
		if strings.TrimSpace(in.Item) == "" {
			return invalidf("scope %q must set a non-empty item", ScopeItem)
		}
	default:
		return invalidf("scope must be %q, %q, %q or %q", ScopeOrg, ScopeGroup, ScopeHost, ScopeItem)
	}
	return nil
}

// compileRule turns in's Form or Expression (per in.Mode) into a trigger
// tree, an optional clear tree, and the Source column: a Format-rendered
// summary of the trigger in form mode, or in.Expression verbatim in
// expression mode — the controller's own decision (see this package's
// Format for why regenerating rather than trusting client-sent text is
// right even there). Expression mode never produces a clear tree: Parse's
// grammar (Task 2) has no clear syntax, only the form editor offers one.
func compileRule(in RuleInput) (trigger, clear Node, source string, pos int, err error) {
	switch in.Mode {
	case modeForm:
		if in.Form == nil {
			return Node{}, Node{}, "", 0, invalidf("mode %q requires a form", modeForm)
		}
		if in.Expression != "" {
			return Node{}, Node{}, "", 0, invalidf("mode %q must not also set an expression", modeForm)
		}
		if in.Form.Metric != in.Metric {
			return Node{}, Node{}, "", 0, invalidf(
				"the form's metric %q does not match the rule's metric %q", in.Form.Metric, in.Metric)
		}
		trig, clr, cerr := in.Form.Compile()
		if cerr != nil {
			p, wrapped := ruleError(cerr)
			return Node{}, Node{}, "", p, wrapped
		}
		return trig, clr, Format(trig), 0, nil
	case modeExpression:
		if in.Expression == "" {
			return Node{}, Node{}, "", 0, invalidf("mode %q requires an expression", modeExpression)
		}
		if in.Form != nil {
			return Node{}, Node{}, "", 0, invalidf("mode %q must not also set a form", modeExpression)
		}
		trig, perr := Parse(in.Expression)
		if perr != nil {
			p, wrapped := ruleError(perr)
			return Node{}, Node{}, "", p, wrapped
		}
		return trig, Node{}, in.Expression, 0, nil
	default:
		return Node{}, Node{}, "", 0, invalidf("mode must be %q or %q", modeForm, modeExpression)
	}
}

// buildRule validates in into a fully populated Rule — everything except
// ID, OrgID, Origin and the timestamps, which Create and Update each set
// their own way — and the byte position of a compile failure, so Check can
// report it without a second validation pass. Create, Update and Check all
// call this, so the three can never disagree about what a valid rule is.
func (s *Service) buildRule(in RuleInput) (Rule, int, error) {
	name := strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameRunes {
		return Rule{}, 0, invalidf("name must be 1-%d characters", maxNameRunes)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return Rule{}, 0, invalidf("name must not contain control characters")
		}
	}
	if in.For < 0 || in.For > maxForDuration {
		return Rule{}, 0, invalidf("for_duration must be between 0 and %s", maxForDuration)
	}
	switch in.Severity {
	case SeverityWarn, SeverityCrit:
	default:
		return Rule{}, 0, invalidf("severity must be %q or %q", SeverityWarn, SeverityCrit)
	}
	entry, ok := Catalogue()[in.Metric]
	if !ok {
		return Rule{}, 0, invalidf("unknown metric %q", in.Metric)
	}
	if err := validateScope(in); err != nil {
		return Rule{}, 0, err
	}
	trigger, clear, source, pos, err := compileRule(in)
	if err != nil {
		return Rule{}, pos, err
	}

	return Rule{
		Name: name, Enabled: in.Enabled, Scope: in.Scope,
		GroupID: in.GroupID, HostID: in.HostID, Item: in.Item,
		CheckKey: entry.CheckKey, Metric: in.Metric, Severity: in.Severity,
		Mode: in.Mode, Trigger: trigger, Clear: clear, Source: source, For: in.For,
	}, 0, nil
}

// createRule is Create and SeedForOrg's shared body; only the origin they
// stamp differs — the same split checks.Service uses between
// CreateDefinition and its own SeedForOrg (internal/checks/definitions.go's
// createDefinition).
func (s *Service) createRule(ctx context.Context, orgID uuid.UUID, in RuleInput, origin string) (Rule, error) {
	r, _, err := s.buildRule(in)
	if err != nil {
		return Rule{}, err
	}
	r.ID, r.OrgID, r.Origin = s.newID(), orgID, origin
	r.CreatedAt, r.UpdatedAt = s.now(), s.now()
	if err := s.store.Create(ctx, r); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// Create validates in and stores a new, operator-created rule (origin =
// "user" — only a seeding path, Task 5, ever stores "seed").
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in RuleInput) (Rule, error) {
	return s.createRule(ctx, orgID, in, originUser)
}

// Update validates in and replaces the rule identified by (orgID, id) — a
// full replace, exactly like checks.UpdateDefinition — except that a
// seeded rule's metric, scope and severity are fixed: an operator who wants
// a different one at that binding copies the rule (Create) and edits the
// copy, rather than this call quietly turning a seeded default into
// something else out from under whatever assumed it still judged the same
// thing. Its thresholds (the compiled tree), duration and enabled flag are
// all ordinary mutable columns even on a seeded row — the same contract
// checks.UpdateDefinition gives seeded check definitions.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, in RuleInput) (Rule, error) {
	existing, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Rule{}, err
	}
	r, _, err := s.buildRule(in)
	if err != nil {
		return Rule{}, err
	}
	if existing.Origin == originSeed {
		switch {
		case r.Metric != existing.Metric:
			return Rule{}, invalidf("a seeded rule's metric is fixed; copy it to change it")
		case r.Scope != existing.Scope:
			return Rule{}, invalidf("a seeded rule's scope is fixed; copy it to change it")
		case r.Severity != existing.Severity:
			return Rule{}, invalidf("a seeded rule's severity is fixed; copy it to change it")
		}
	}
	r.ID, r.OrgID, r.Origin = id, orgID, existing.Origin
	r.CreatedAt, r.UpdatedAt = existing.CreatedAt, s.now()
	if err := s.store.Update(ctx, r); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// Delete removes the rule identified by (orgID, id).
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}

// Get returns the single rule identified by (orgID, id), or ErrNotFound —
// including one belonging to another organization, which reads exactly the
// same (see ErrNotFound's own doc comment).
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Rule, error) {
	return s.store.Get(ctx, orgID, id)
}

// List returns orgID's rules matching f.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, f Filter) ([]Rule, error) {
	return s.store.List(ctx, orgID, f)
}

// checkMessage extracts the human-readable text of an ErrInvalidInput
// failure, stripping the sentinel's own "invalid input: " prefix. Every
// failure buildRule produces wraps that way (invalidf, ruleError), so this
// is a plain, deterministic strip rather than a type switch over every
// possible validation error.
func checkMessage(err error) string {
	return strings.TrimPrefix(err.Error(), ErrInvalidInput.Error()+": ")
}

// Check validates in without storing it: the same validation Create and
// Update run, reported as a Checked rather than only an error, so the
// editor gets the caret position of a parse or type-check failure
// (Checked.Pos) and whether a rule that compiled fine still fits the form
// subset (Checked.FormReady, see AsForm) — the "switching to expression
// mode is one-way" warning's own data source.
//
// Check still returns a non-nil error on an invalid in (wrapping
// ErrInvalidInput, exactly like Create and Update), so a caller that only
// wants a yes/no can use errors.Is uniformly across all three methods; a
// caller that wants the caret position reads Checked instead.
func (s *Service) Check(_ context.Context, _ uuid.UUID, in RuleInput) (Checked, error) {
	r, pos, err := s.buildRule(in)
	if err != nil {
		return Checked{Pos: pos, Message: checkMessage(err)}, err
	}
	_, formReady := AsForm(r.Trigger, r.Clear)
	return Checked{OK: true, FormReady: formReady, Source: r.Source}, nil
}
