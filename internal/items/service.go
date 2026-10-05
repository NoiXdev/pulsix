package items

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// maxNoteRunes bounds Decision.Note: long enough for an operator's reason,
// short enough that a note never becomes a second place to paste a log.
const maxNoteRunes = 500

// PreviewLimit bounds how many matches Preview ever materialises. It is
// not a bound on Total — see Preview's own doc comment.
const PreviewLimit = 200

// DecisionInput is the caller-supplied shape used to create or replace a
// Decision. It carries everything buildDecision validates and nothing
// a caller does not control: no ID, OrgID, CreatedBy or timestamps, all of
// which Create and Update each stamp their own way.
type DecisionInput struct {
	Scope     Scope
	HostID    *uuid.UUID
	CheckKey  string
	MatchKind MatchKind
	Pattern   string
	Verdict   Verdict
	Note      string
}

// Preview is what a pattern would catch. Matches is capped at
// PreviewLimit; Total is not, because the count is the only brake on a
// pattern that catches the fleet. A caller renders "this would match N
// items" from Total, and Capped tells it whether Matches is the whole
// story or only the first PreviewLimit of it.
type Preview struct {
	Matches []Target
	Total   int
	Capped  bool
}

// Service validates and persists decisions, and previews what one would
// catch before it is stored. It carries a clock because, like
// rules.Service, Store.CreateDecision/UpdateDecision never hand a
// database-stamped row back — the timestamps a caller sees are exactly the
// ones this Service stamped before calling the store.
//
// It also carries checkKeys rather than knowing the metric catalogue
// itself: this package must stay a leaf (see this package's own doc
// comment for why), so the set of valid check keys is supplied by
// whoever constructs the Service, not looked up here.
type Service struct {
	store     Store
	checkKeys func() map[string]struct{}
	rand      io.Reader
	now       func() time.Time
}

// Option configures a Service.
type Option func(*Service)

// WithRand injects the entropy source for ids.
func WithRand(r io.Reader) Option { return func(s *Service) { s.rand = r } }

// WithClock injects the clock Create/Update stamp CreatedAt/UpdatedAt
// against; time.Now is the default. Tests pin timestamps exactly.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// NewService wires store and checkKeys, the set of check_key values a
// decision may name. checkKeys is a required positional argument, not an
// Option: forgetting it is then a compile error at every construction
// site, rather than a Service that silently accepts a check key naming no
// real metric at whichever call site someone forgot it. It is a function,
// not a snapshot, so it reflects the catalogue at call time — the moment a
// decision is written or previewed — rather than at wiring time.
//
// internal/items must not import internal/rules (see this package's own
// doc comment), so the real catalogue lives at the wiring point that may
// import both — internal/app, supplying rules.Catalogue()'s check keys —
// not here.
func NewService(store Store, checkKeys func() map[string]struct{}, opts ...Option) *Service {
	s := &Service{store: store, checkKeys: checkKeys, rand: rand.Reader, now: time.Now}
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

// invalidf builds an ErrInvalidInput failure carrying a message an operator
// can act on.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}

// validateMatch validates exactly the fields that decide which items a
// decision governs — Scope, HostID, CheckKey, MatchKind and Pattern — and
// returns a Decision with only those fields set. It is split out of
// buildDecision so Preview can call it directly: Preview answers "what
// would this pattern match", and Verdict or Note cannot change that
// answer, so neither belongs in the validation a preview runs.
//
// CheckKey is checked against s.checkKeys(), called fresh here rather than
// once at construction, so a plugin added to the catalogue after this
// Service was built is already a valid check_key on the next call.
func (s *Service) validateMatch(in DecisionInput) (Decision, error) {
	switch in.Scope {
	case ScopeOrg:
		if in.HostID != nil {
			return Decision{}, invalidf("scope %q must not set a host", ScopeOrg)
		}
	case ScopeHost:
		if in.HostID == nil {
			return Decision{}, invalidf("scope %q must set a host", ScopeHost)
		}
	default:
		return Decision{}, invalidf("scope must be %q or %q", ScopeOrg, ScopeHost)
	}

	if _, ok := s.checkKeys()[in.CheckKey]; !ok {
		return Decision{}, invalidf("unknown check key %q", in.CheckKey)
	}

	switch in.MatchKind {
	case MatchExact:
		// A star in an "exact" pattern is a user who meant "glob": refusing
		// it with that message is kinder than silently matching nothing,
		// which is the exact failure mode this milestone exists to prevent.
		if strings.Contains(in.Pattern, "*") {
			return Decision{}, invalidf(
				"exact match pattern must not contain %q; did you mean match kind %q?", "*", MatchGlob)
		}
	case MatchGlob:
		// No constraint beyond ValidPattern below.
	default:
		return Decision{}, invalidf("match kind must be %q or %q", MatchExact, MatchGlob)
	}

	if err := ValidPattern(in.Pattern); err != nil {
		return Decision{}, err
	}

	return Decision{
		Scope: in.Scope, HostID: in.HostID, CheckKey: in.CheckKey,
		MatchKind: in.MatchKind, Pattern: in.Pattern,
	}, nil
}

// buildDecision validates in into a fully populated Decision — everything
// except ID, OrgID, CreatedBy and the timestamps, which CreateDecision and
// UpdateDecision each set their own way. It runs validateMatch first, so
// Create, Update and Preview can never disagree about what a valid scope,
// check key, match kind or pattern is, then validates the two fields a
// preview has no use for: Note and Verdict. CreateDecision and
// UpdateDecision both call this, so the two can never disagree about what
// a valid decision is, either.
func (s *Service) buildDecision(in DecisionInput) (Decision, error) {
	d, err := s.validateMatch(in)
	if err != nil {
		return Decision{}, err
	}

	if n := utf8.RuneCountInString(in.Note); n > maxNoteRunes {
		return Decision{}, invalidf("note must be at most %d characters, got %d", maxNoteRunes, n)
	}

	switch in.Verdict {
	case Ignore, Watch:
	default:
		return Decision{}, invalidf("verdict must be %q or %q", Ignore, Watch)
	}

	// An organization-wide watch is a no-op by construction: watching is
	// already what happens to anything no decision names, so a decision
	// that only restates the default can never change what gets judged.
	// Refusing it here — rather than leaving it to the GUI to merely not
	// offer the option — means the rule and the absent option say the same
	// thing, and no such row can ever exist for a dialog (or any other
	// caller) to later have to render truthfully and refuse to save.
	if in.Scope == ScopeOrg && in.Verdict == Watch {
		return Decision{}, invalidf(
			"scope %q with verdict %q would have no effect: watching is already what happens by default", ScopeOrg, Watch)
	}

	d.Verdict, d.Note = in.Verdict, in.Note
	return d, nil
}

// CreateDecision validates in and stores a new decision bound to orgID,
// stamped with actor as CreatedBy — the house shape schedules.Service.Create,
// triggers.Service.Create and maintenance.Service.Create all take the same
// parameter for the same reason: a decision is an operator's call, and the
// note alone answers why; CreatedBy is what lets a reader six months later
// answer who, without having to dig through the audit log for the same
// create event.
func (s *Service) CreateDecision(ctx context.Context, orgID, actor uuid.UUID, in DecisionInput) (Decision, error) {
	d, err := s.buildDecision(in)
	if err != nil {
		return Decision{}, err
	}
	d.ID, d.OrgID = s.newID(), orgID
	d.CreatedBy = &actor
	d.CreatedAt, d.UpdatedAt = s.now(), s.now()
	if err := s.store.CreateDecision(ctx, d); err != nil {
		return Decision{}, err
	}
	return d, nil
}

// UpdateDecision validates in and replaces the decision identified by
// (orgID, id) — a full replace, the same contract rules.Service.Update
// gives a rule. CreatedBy is preserved from the existing row: it names who
// first made the call to ignore or watch this pattern, and an edit of the
// pattern or verdict does not change who that was.
func (s *Service) UpdateDecision(ctx context.Context, orgID, id uuid.UUID, in DecisionInput) (Decision, error) {
	existing, err := s.store.GetDecision(ctx, orgID, id)
	if err != nil {
		return Decision{}, err
	}
	d, err := s.buildDecision(in)
	if err != nil {
		return Decision{}, err
	}
	d.ID, d.OrgID, d.CreatedBy = id, orgID, existing.CreatedBy
	d.CreatedAt, d.UpdatedAt = existing.CreatedAt, s.now()
	if err := s.store.UpdateDecision(ctx, d); err != nil {
		return Decision{}, err
	}
	return d, nil
}

// DeleteDecision removes the decision identified by (orgID, id).
func (s *Service) DeleteDecision(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.DeleteDecision(ctx, orgID, id)
}

// GetDecision returns the single decision identified by (orgID, id), or
// ErrNotFound — including one belonging to another organization, which
// reads exactly the same.
func (s *Service) GetDecision(ctx context.Context, orgID, id uuid.UUID) (Decision, error) {
	return s.store.GetDecision(ctx, orgID, id)
}

// ListDecisions returns every decision bound to orgID.
func (s *Service) ListDecisions(ctx context.Context, orgID uuid.UUID) ([]Decision, error) {
	return s.store.ListDecisions(ctx, orgID)
}

// Decisions returns the decisions that can govern an item on hostID: the
// organization's own, plus — when hostID is non-nil — that one host's.
// It is ListDecisions' narrow sibling, and the one every per-host caller
// should use: the evaluation ticker already does (through the Store
// directly), and so do GET /hosts/{id}/checks and GET /hosts/{id}/items,
// both of which are polled per open host page. The underlying query
// (`scope = 'org' OR (scope = 'host' AND host_id = $2)`) is backed by
// check_item_decisions_lookup_idx, where ListDecisions is a scan of every
// decision in the organization — including every other host's, which
// Decide would only drop again in applies().
//
// A nil hostID asks for the organization-scoped decisions alone, which is
// a meaningful question (nothing host-specific can apply) rather than a
// mistake, so it is not refused here.
func (s *Service) Decisions(ctx context.Context, orgID uuid.UUID, hostID *uuid.UUID) ([]Decision, error) {
	return s.store.Decisions(ctx, orgID, hostID)
}

// Preview reports which known items would be governed by in, without
// storing it. It resolves in against every known item of orgID — or, when
// in is host-scoped, of that host alone, since previewing a host decision
// against the whole fleet would report a number that has nothing to do
// with what the decision will actually do.
//
// It is the brake, so it reports the truth about size: Matches is capped
// at PreviewLimit, but Total counts every matching item, not len(Matches).
// A pattern that would catch the whole fleet must say so.
//
// The org-scope branch pushes CheckKey and Pattern into ItemsForOrg's own
// Filter rather than fetching Filter{} — every item in the organization —
// and narrowing in Go: the store already translates CheckKey and ItemGlob
// into the query (the SPA's own organization-wide items listing uses the
// same Filter), so there is no reason this call site should be the one
// that materialises the whole table to count a match. That matters most
// for exactly the pattern this preview is the brake against (review focus
// 1, "a pattern of '*'"): the one that matches everything is now also the
// cheapest to count, not the most expensive. Total is simply len(views)
// once the query itself has done the filtering, so a caller still gets the
// true count even though Matches is capped. The host-scope branch keeps
// filtering in Go: ItemsForHost already bounds the fetch to one host, so
// there is no org-wide table to avoid materialising, and adding a Filter
// parameter there would be a second code path for the one case this fix is
// about.
//
// Preview validates only the fields it reads — Scope, HostID, CheckKey,
// MatchKind and Pattern — not Verdict or Note: neither can change what a
// pattern matches, so an unset verdict or an over-long note, both
// unremarkable while an operator is still shaping the pattern, must not
// turn a count into an error. A previewed input that passes here can
// still be rejected by CreateDecision/UpdateDecision for Verdict or Note;
// a caller already has to let a pattern be previewed and then abandoned.
func (s *Service) Preview(ctx context.Context, orgID uuid.UUID, in DecisionInput) (Preview, error) {
	d, err := s.validateMatch(in) // not buildDecision — see this function's own doc comment
	if err != nil {
		return Preview{}, err
	}

	var out Preview
	if d.Scope == ScopeHost {
		views, err := s.store.ItemsForHost(ctx, orgID, *d.HostID)
		if err != nil {
			return Preview{}, fmt.Errorf("preview decision: %w", err)
		}
		for _, v := range views {
			if v.CheckKey != d.CheckKey || !Match(d.Pattern, v.Name) {
				continue
			}
			out.Total++
			if len(out.Matches) < PreviewLimit {
				out.Matches = append(out.Matches, Target{HostID: v.HostID, CheckKey: v.CheckKey, Item: v.Name})
			}
		}
		out.Capped = out.Total > PreviewLimit
		return out, nil
	}

	// d.Pattern is a valid Filter.ItemGlob regardless of MatchKind: an
	// exact pattern is, by validateMatch's own check, one with no '*' in
	// it, which is exactly a glob that matches only its own literal value.
	views, err := s.store.ItemsForOrg(ctx, orgID, Filter{CheckKey: d.CheckKey, ItemGlob: d.Pattern})
	if err != nil {
		return Preview{}, fmt.Errorf("preview decision: %w", err)
	}
	out.Total = len(views)
	out.Capped = out.Total > PreviewLimit
	n := out.Total
	if n > PreviewLimit {
		n = PreviewLimit
	}
	if n > 0 {
		out.Matches = make([]Target, n)
		for i, v := range views[:n] {
			out.Matches[i] = Target{HostID: v.HostID, CheckKey: v.CheckKey, Item: v.Name}
		}
	}
	return out, nil
}

// ItemsForHost passes through to the store, so the API has one dependency
// (this Service) for both items and decisions.
func (s *Service) ItemsForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]View, error) {
	return s.store.ItemsForHost(ctx, orgID, hostID)
}

// ItemsForOrg lists the organization's items, narrowed by f.
//
// f.ItemGlob, when set, goes through ValidPattern first — the same bound
// every write path applies to the pattern an operator types. It is a
// read, but not a free one: the store translates the glob into a
// `LIKE ... ESCAPE '\'` over check_items, and a kilobytes-long
// alternating-'%x' pattern is an authenticated but very cheap way to make
// that scan expensive. Validating here rather than in the handler keeps
// the bound in one place, next to the only other code that knows what a
// pattern may be.
func (s *Service) ItemsForOrg(ctx context.Context, orgID uuid.UUID, f Filter) ([]View, error) {
	if f.ItemGlob != "" {
		if err := ValidPattern(f.ItemGlob); err != nil {
			return nil, err
		}
	}
	return s.store.ItemsForOrg(ctx, orgID, f)
}
