package templates

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// maxFollowUps bounds the follow-ups of one template.
const maxFollowUps = 10

// ErrFollowUpCycle is the sentinel behind *CycleError: errors.Is matches it
// and ErrInvalidInput, so the transport answers 422.
var ErrFollowUpCycle = fmt.Errorf("%w: follow-ups would form a cycle", ErrInvalidInput)

// FollowUp is one template started after another's successful run.
type FollowUp struct {
	TemplateID uuid.UUID
	Name       string
}

// CycleError says which template names close a loop: Names run in path
// order, the first and the last being the template being saved.
type CycleError struct{ Names []string }

func (e *CycleError) Error() string {
	return "follow-ups would form a cycle: " + strings.Join(e.Names, " → ")
}

// Is makes the error match ErrFollowUpCycle and ErrInvalidInput.
func (e *CycleError) Is(target error) bool {
	return target == ErrFollowUpCycle || target == ErrInvalidInput
}

// FollowUpsError says the follow-up list itself is the problem: too many
// entries, the template itself, one listed twice, or a template that does
// not exist (deleted while the dialog was open). It is an ErrInvalidInput;
// the API answers 422 at the body's follow_ups field, like a cycle.
type FollowUpsError struct{ err error }

func (e *FollowUpsError) Error() string { return e.err.Error() }
func (e *FollowUpsError) Unwrap() error { return e.err }

func invalidFollowUps(format string, args ...any) error {
	return &FollowUpsError{err: fmt.Errorf("%w: "+format, append([]any{ErrInvalidInput}, args...)...)}
}

// InUseError refuses deleting a template other templates name as a
// follow-up; Referrers are their names. The API answers 409.
type InUseError struct{ Referrers []string }

func (e *InUseError) Error() string {
	return "this template is a follow-up of: " + strings.Join(e.Referrers, ", ")
}

// FollowUpStore is the follow-up part of Store.
type FollowUpStore interface {
	// LockFollowUpGraph takes the organization's transaction-scoped advisory
	// lock on the follow-up graph. It must run inside InTx, before the
	// validation reads the graph: two saves that would together close a
	// cycle then serialize, and the second sees the first's committed edges.
	LockFollowUpGraph(ctx context.Context, orgID uuid.UUID) error
	// ReplaceFollowUps replaces a template's follow-ups; ids order is the
	// start order.
	ReplaceFollowUps(ctx context.Context, orgID, templateID uuid.UUID, ids []uuid.UUID) error
	// ListFollowUps returns a template's follow-ups in start order.
	ListFollowUps(ctx context.Context, orgID, templateID uuid.UUID) ([]FollowUp, error)
	// ListOrgFollowUps is ListFollowUps for every template of the
	// organization in one call, keyed by template id.
	ListOrgFollowUps(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID][]FollowUp, error)
	// FollowUpReferrers returns the names of the templates that name
	// templateID as a follow-up.
	FollowUpReferrers(ctx context.Context, orgID, templateID uuid.UUID) ([]string, error)
}

// setFollowUps validates ids for template id (named name) and writes them.
// It runs inside InTx: the graph lock comes first, so the cycle check reads
// a graph no concurrent save can change before this one commits. It returns
// the stored list with names.
func (s *Service) setFollowUps(ctx context.Context, orgID, id uuid.UUID, name string, ids []uuid.UUID) ([]FollowUp, error) {
	if err := s.store.LockFollowUpGraph(ctx, orgID); err != nil {
		return nil, fmt.Errorf("lock the follow-up graph: %w", err)
	}
	if err := s.validateFollowUps(ctx, orgID, id, name, ids); err != nil {
		return nil, err
	}
	if err := s.store.ReplaceFollowUps(ctx, orgID, id, ids); err != nil {
		return nil, fmt.Errorf("store template follow-ups: %w", err)
	}
	return s.store.ListFollowUps(ctx, orgID, id)
}

// validateFollowUps refuses a list with too many, duplicate or self
// entries, an unknown template, or a loop through the stored graph.
func (s *Service) validateFollowUps(ctx context.Context, orgID, id uuid.UUID, name string, ids []uuid.UUID) error {
	if len(ids) > maxFollowUps {
		return invalidFollowUps("at most %d follow-ups", maxFollowUps)
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	names := map[uuid.UUID]string{id: name}
	for _, fid := range ids {
		switch {
		case fid == id:
			return invalidFollowUps("a template cannot be its own follow-up")
		case seen[fid]:
			return invalidFollowUps("a follow-up is listed twice")
		}
		seen[fid] = true
		fu, err := s.store.Get(ctx, orgID, fid)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return invalidFollowUps("follow-up template does not exist")
			}
			return fmt.Errorf("look up follow-up template: %w", err)
		}
		names[fid] = fu.Name
	}
	stored, err := s.store.ListOrgFollowUps(ctx, orgID)
	if err != nil {
		return fmt.Errorf("list follow-ups: %w", err)
	}
	edges := make(map[uuid.UUID][]uuid.UUID, len(stored)+1)
	for from, list := range stored {
		if from == id {
			continue
		}
		for _, fu := range list {
			edges[from] = append(edges[from], fu.TemplateID)
			names[fu.TemplateID] = fu.Name
		}
	}
	edges[id] = ids
	path := findCycle(edges, id)
	if path == nil {
		return nil
	}
	out := make([]string, len(path))
	for i, p := range path {
		out[i] = names[p]
	}
	return &CycleError{Names: out}
}

// findCycle returns the path of a cycle through from — starting and ending
// at from — or nil when none of from's follow-up paths leads back to it.
// Cycles elsewhere in the graph that do not pass through from are not its
// concern: the stored graph is acyclic and only from's edges changed.
func findCycle(edges map[uuid.UUID][]uuid.UUID, from uuid.UUID) []uuid.UUID {
	visited := map[uuid.UUID]bool{}
	path := []uuid.UUID{from}
	var walk func(node uuid.UUID) bool
	walk = func(node uuid.UUID) bool {
		for _, next := range edges[node] {
			if next == from {
				path = append(path, from)
				return true
			}
			if visited[next] {
				continue
			}
			visited[next] = true
			path = append(path, next)
			if walk(next) {
				return true
			}
			path = path[:len(path)-1]
		}
		return false
	}
	if walk(from) {
		return path
	}
	return nil
}
