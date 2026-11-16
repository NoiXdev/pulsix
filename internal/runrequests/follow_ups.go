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

// FollowUpTemplate is what the walk needs of a template: its name, its
// revision and its follow-ups' ids in start order.
type FollowUpTemplate struct {
	Name      string
	Revision  int64
	FollowUps []uuid.UUID
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
	w := walker{lookup: lookup}
	for _, r := range roots {
		n, ok, err := w.node(ctx, r.TemplateID, r.Name, r.Depth, r.Path)
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
	emitted   int
	truncated bool
}

// node emits id at the row depth and path of its parent run; ok is false
// when the cap stopped the walk before it.
func (w *walker) node(ctx context.Context, id uuid.UUID, name string, depth int, path []uuid.UUID) (PictureFollowUp, bool, error) {
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
	childPath := append(slices.Clone(path), id)
	for _, c := range t.FollowUps {
		child, ok, err := w.node(ctx, c, "", depth+1, childPath)
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

// Startable is the direct follow-ups an approval can start: those without a
// Stop, in order. None when the setting is off or the tree was truncated.
func (f *PictureFollowUps) Startable() []uuid.UUID {
	if f == nil || !f.Start || f.Truncated {
		return nil
	}
	var ids []uuid.UUID
	for _, t := range f.Templates {
		if t.Stop != "" {
			continue
		}
		if id, err := uuid.Parse(t.TemplateID); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
