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

	"github.com/noixdev/pulsix/internal/rules"
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

// invalidf builds an ErrInvalidInput failure carrying a message an operator
// can act on.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}

// validCheckKeys is the set of check keys the metric catalogue knows,
// derived from rules.Catalogue() rather than hardcoded, so a new plugin
// metric is automatically a valid check_key the moment it is added to the
// catalogue — never a second list to keep in sync with it.
//
// internal/items importing internal/rules does not create an import
// cycle: `go list -deps ./internal/rules/...` carries no path back to
// internal/items, so this is a plain dependency on rules' own package,
// not the narrow-port workaround the task anticipated for the case where
// it would cycle.
func validCheckKeys() map[string]struct{} {
	keys := make(map[string]struct{})
	for _, entry := range rules.Catalogue() {
		keys[entry.CheckKey] = struct{}{}
	}
	return keys
}

// buildDecision validates in into a fully populated Decision — everything
// except ID, OrgID, CreatedBy and the timestamps, which CreateDecision and
// UpdateDecision each set their own way. CreateDecision and UpdateDecision
// both call this, so the two can never disagree about what a valid
// decision is.
func buildDecision(in DecisionInput) (Decision, error) {
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

	if _, ok := validCheckKeys()[in.CheckKey]; !ok {
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

	if n := utf8.RuneCountInString(in.Note); n > maxNoteRunes {
		return Decision{}, invalidf("note must be at most %d characters, got %d", maxNoteRunes, n)
	}

	switch in.Verdict {
	case Ignore, Watch:
	default:
		return Decision{}, invalidf("verdict must be %q or %q", Ignore, Watch)
	}

	return Decision{
		Scope: in.Scope, HostID: in.HostID, CheckKey: in.CheckKey,
		MatchKind: in.MatchKind, Pattern: in.Pattern, Verdict: in.Verdict, Note: in.Note,
	}, nil
}

// CreateDecision validates in and stores a new decision bound to orgID.
func (s *Service) CreateDecision(ctx context.Context, orgID uuid.UUID, in DecisionInput) (Decision, error) {
	d, err := buildDecision(in)
	if err != nil {
		return Decision{}, err
	}
	d.ID, d.OrgID = s.newID(), orgID
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
	d, err := buildDecision(in)
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

// Preview reports which known items would be governed by in, without
// storing it. It resolves in against every known item of orgID — or, when
// in is host-scoped, of that host alone, since previewing a host decision
// against the whole fleet would report a number that has nothing to do
// with what the decision will actually do.
//
// It is the brake, so it reports the truth about size: Matches is capped
// at PreviewLimit, but Total counts every matching item, not len(Matches).
// A pattern that would catch the whole fleet must say so, even though the
// service only ever builds a PreviewLimit-long list in memory to say it.
func (s *Service) Preview(ctx context.Context, orgID uuid.UUID, in DecisionInput) (Preview, error) {
	d, err := buildDecision(in)
	if err != nil {
		return Preview{}, err
	}

	var views []View
	if d.Scope == ScopeHost {
		views, err = s.store.ItemsForHost(ctx, orgID, *d.HostID)
	} else {
		views, err = s.store.ItemsForOrg(ctx, orgID, Filter{})
	}
	if err != nil {
		return Preview{}, fmt.Errorf("preview decision: %w", err)
	}

	var out Preview
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

// ItemsForHost passes through to the store, so the API has one dependency
// (this Service) for both items and decisions.
func (s *Service) ItemsForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]View, error) {
	return s.store.ItemsForHost(ctx, orgID, hostID)
}

// ItemsForOrg passes through to the store, so the API has one dependency
// (this Service) for both items and decisions.
func (s *Service) ItemsForOrg(ctx context.Context, orgID uuid.UUID, f Filter) ([]View, error) {
	return s.store.ItemsForOrg(ctx, orgID, f)
}
