package runrequests

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// Why a follow-up would not start (PictureFollowUp.Stop).
const (
	StopDeleted = "deleted"
	StopInChain = "in_chain"
	StopTooDeep = "too_deep"
)

// MaxPictureFollowUps bounds the nodes of one picture's follow-up tree.
const MaxPictureFollowUps = 100

// maxChainDepth mirrors the chain starter's limit: a row whose parent run is
// at this depth is skipped (internal/chain/starter_decide.go).
const maxChainDepth = 10

// FollowUpRef is a follow-up as its parent lists it: id and name.
type FollowUpRef struct {
	TemplateID uuid.UUID
	Name       string
}

// FollowUpTemplate is what the walk needs of a template: its name, its
// revision and its follow-ups in start order.
type FollowUpTemplate struct {
	Name      string
	Revision  int64
	FollowUps []FollowUpRef
	// RepoID and RepoName are the repository its playbook comes from, nil
	// and "" for an editor playbook.
	RepoID   *uuid.UUID
	RepoName string
}

// FollowUpLookup reads a template's current follow-up data; found is false
// when the template is gone.
type FollowUpLookup func(ctx context.Context, id uuid.UUID) (t FollowUpTemplate, found bool, err error)

// FollowUpRoot is a direct follow-up of the request. Depth and Path are its
// row's, that is the parent run's: a run request's template is depth 1 with
// Path [template]; a retry's copied rows keep theirs.
type FollowUpRoot struct {
	TemplateID uuid.UUID
	Name       string
	Depth      int
	Path       []uuid.UUID
}

// BuildFollowUps builds the picture's follow-up tree, nil for no roots.
// With start false the nodes carry id and name only and lookup is never
// called. With start true it walks depth-first in position order, applying
// the starter's rules before reading a template: too deep, already on the
// path, then deleted. Stop-marked nodes are leaves. Every emitted node
// counts toward MaxPictureFollowUps; past it the walk ends with Truncated.
func BuildFollowUps(ctx context.Context, start bool, roots []FollowUpRoot, lookup FollowUpLookup) (*PictureFollowUps, error) {
	return buildFollowUps(ctx, start, roots, lookup, nil)
}

// buildFollowUps is BuildFollowUps for a run pinned to a commit of
// repository pinRepo (nil: not pinned). Each node then says what it runs
// (PictureFollowUp.Commit): a follow-up of the same repository under
// pinned runs only runs the same commit (jobs.Service.chainPin).
func buildFollowUps(ctx context.Context, start bool, roots []FollowUpRoot, lookup FollowUpLookup, pinRepo *uuid.UUID) (*PictureFollowUps, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	out := &PictureFollowUps{Start: start, Templates: make([]PictureFollowUp, 0, len(roots))}
	if !start {
		for _, r := range roots {
			out.Templates = append(out.Templates, PictureFollowUp{TemplateID: r.TemplateID.String(), TemplateName: r.Name})
		}
		return out, nil
	}
	w := walker{lookup: lookup, pinRepo: pinRepo}
	for _, r := range roots {
		n, ok, err := w.node(ctx, r.TemplateID, r.Name, r.Depth, r.Path, true)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		out.Templates = append(out.Templates, n)
	}
	out.Truncated = w.truncated
	return out, nil
}

type walker struct {
	lookup    FollowUpLookup
	pinRepo   *uuid.UUID
	emitted   int
	truncated bool
}

// node emits id at the row depth and path of its parent run; ok is false
// when the cap stopped the walk before it. parentPinned says that the run
// starting it is pinned.
func (w *walker) node(ctx context.Context, id uuid.UUID, name string, depth int, path []uuid.UUID, parentPinned bool) (PictureFollowUp, bool, error) {
	if w.emitted >= MaxPictureFollowUps {
		w.truncated = true
		return PictureFollowUp{}, false, nil
	}
	w.emitted++
	n := PictureFollowUp{TemplateID: id.String(), TemplateName: name}
	switch {
	case depth >= maxChainDepth:
		n.Stop = StopTooDeep
		return n, true, nil
	case slices.Contains(path, id):
		n.Stop = StopInChain
		return n, true, nil
	}
	t, found, err := w.lookup(ctx, id)
	if err != nil {
		return PictureFollowUp{}, false, fmt.Errorf("read follow-up template %s: %w", id, err)
	}
	if !found {
		n.Stop = StopDeleted
		return n, true, nil
	}
	n.TemplateName, n.TemplateRevision = t.Name, t.Revision
	pinned := false
	if w.pinRepo != nil {
		switch {
		case parentPinned && t.RepoID != nil && *t.RepoID == *w.pinRepo:
			n.Commit, pinned = FollowUpPinned, true
		case t.RepoID != nil:
			n.Commit, n.Repo = FollowUpCurrent, t.RepoName
		default:
			n.Commit = FollowUpEditor
		}
	}
	childPath := append(slices.Clone(path), id)
	for _, c := range t.FollowUps {
		child, ok, err := w.node(ctx, c.TemplateID, c.Name, depth+1, childPath, pinned)
		if err != nil {
			return PictureFollowUp{}, false, err
		}
		if !ok {
			break
		}
		n.FollowUps = append(n.FollowUps, child)
	}
	return n, true, nil
}

// startable is the direct follow-ups an approval can start, those without a
// Stop, in order, beside their parsed ids. None when the setting is off or
// the tree was truncated. The picture writes every id from a uuid.UUID, so
// one that does not parse — a stop-marked node's included — is a fault and
// an error, never a follow-up quietly left out.
func (f *PictureFollowUps) startable() ([]PictureFollowUp, []uuid.UUID, error) {
	if f == nil || !f.Start || f.Truncated {
		return nil, nil, nil
	}
	var nodes []PictureFollowUp
	var ids []uuid.UUID
	for _, t := range f.Templates {
		id, err := uuid.Parse(t.TemplateID)
		if err != nil {
			return nil, nil, fmt.Errorf("follow-up template id %q in the run picture: %w", t.TemplateID, err)
		}
		if t.Stop != "" {
			continue
		}
		nodes, ids = append(nodes, t), append(ids, id)
	}
	return nodes, ids, nil
}

// selectFollowUps resolves an approval's selection against the picture's
// follow-ups block f into the direct follow-ups to start and their parsed
// ids, in picture order and never nil. A nil selection is the default:
// every startable one when the setting is on and the tree is complete, none
// otherwise. A given selection must name startable ones only; with the
// setting off, no follow-ups or a truncated tree, only an empty one is
// accepted. An id in f that does not parse is an error (see startable).
func selectFollowUps(f *PictureFollowUps, selection *[]uuid.UUID) ([]PictureFollowUp, []uuid.UUID, error) {
	out, outIDs := []PictureFollowUp{}, []uuid.UUID{}
	given := selection != nil && len(*selection) > 0
	switch {
	case f != nil && !f.Start:
		if given {
			return nil, nil, fmt.Errorf("%w: approved run requests start no follow-ups in this organization", ErrInvalidInput)
		}
		return out, outIDs, nil
	case f != nil && f.Truncated:
		if given {
			return nil, nil, fmt.Errorf("%w: too many follow-ups to show; approve without follow-ups or start them by hand", ErrInvalidInput)
		}
		return out, outIDs, nil
	}
	nodes, ids, err := f.startable()
	if err != nil {
		return nil, nil, err
	}
	if selection != nil {
		for _, id := range *selection {
			if !slices.Contains(ids, id) {
				return nil, nil, fmt.Errorf("%w: follow-up %s is not one this request would start", ErrInvalidInput, id)
			}
		}
	}
	for i, id := range ids {
		if selection == nil || slices.Contains(*selection, id) {
			out, outIDs = append(out, nodes[i]), append(outIDs, id)
		}
	}
	return out, outIDs, nil
}
