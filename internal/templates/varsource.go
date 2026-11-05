// A template's variables linked to its playbook's variable sources
// (backlog 7.75, spec goal 5): the mode, the preview and a person's apply.
// The automatic apply after a sync is autoapply.go, the review summary
// review.go.
package templates

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/varsync"
)

// ErrSourceChanged means an apply names another source revision than the
// one it would apply: the person previewed an older read (409 at the API).
var ErrSourceChanged = errors.New("the source changed since the preview")

// SourceLookup is the slice of playbooks.Service the variables link
// reads: a playbook's variable sources as last read (PlaybookLookup).
type SourceLookup interface {
	VarSource(ctx context.Context, orgID, id uuid.UUID) (playbooks.VarSource, error)
}

// VarsStore is the part of Store the variables link adds.
type VarsStore interface {
	// UpdateVarsSource writes a template's schema and variables source
	// columns (VarsMode, VarsSourceRev, VarsAppliedAt), never its values,
	// under Update's revision guard; a non-nil ifSourceRev the template's
	// applied source revision has moved past is ErrRevisionMismatch too.
	UpdateVarsSource(ctx context.Context, t Template, ifRevision *int64, ifSourceRev *string) (Template, error)
	// RecordSourceRev writes only VarsSourceRev and VarsAppliedAt, without
	// moving the revision, when the template is still at ifRevision and
	// ifSourceRev; ErrRevisionMismatch otherwise.
	RecordSourceRev(ctx context.Context, t Template, ifRevision int64, ifSourceRev string) (Template, error)
	// ListAutomaticForRepo lists the organization's templates in automatic
	// mode whose playbook belongs to the repository, by name.
	ListAutomaticForRepo(ctx context.Context, orgID, repoID uuid.UUID) ([]Template, error)
	// ListAutomaticForPlaybook lists the playbook's templates in automatic
	// mode, by name.
	ListAutomaticForPlaybook(ctx context.Context, orgID, playbookID uuid.UUID) ([]Template, error)
}

// SourceInfo is a template's playbook's variable sources as the variables
// page shows them. Rev is the read's revision (a commit, or an editor
// playbook's text revision), AppliedRev the one the template last
// applied; Newer says the read is usable and not applied yet. Available
// is false, with Reason saying why, when nothing may change the template:
// no read (Found false), a worker that reads no sources, a playbook the
// last sync left out, or one that was not read.
type SourceInfo struct {
	Kind        string
	Rev         string
	SyncedAt    *time.Time
	AppliedRev  string
	Newer       bool
	WorkerReads bool
	Incomplete  bool
	Found       bool
	LeftOut     bool
	Available   bool
	Reason      string
	RepoID      *uuid.UUID
	Path        string
}

// ApplyInput is a person's apply of a preview. Mode is the mode the
// template is in afterwards (manual or automatic); SourceRev and
// SourceSyncedAt are the source revision and sync time the person
// previewed (SourceSyncedAt nil for an editor playbook, whose read has
// none); RemoveGone confirms the removal of the gone fields. Values are entered over the stored ones: a value for a
// field that no longer fits, a new required field's value; null removes a
// stored value. IfRevision, when set, is the revision the apply is based on.
type ApplyInput struct {
	Mode           SourceMode
	SourceRev      string
	SourceSyncedAt *time.Time
	RemoveGone     bool
	Values         map[string]any
	IfRevision     *int64
}

// SetVariablesMode switches a linked template between manual and
// automatic, or unlinks it (off): every field turns manual, keeping its
// content, its active switch and its values, and the applied revision is
// forgotten. The mode the template is in already changes nothing (no
// write, no new revision). Linking goes through a preview
// (ApplyVariables), so off → manual or automatic is ErrInvalidInput; a
// projection's variables belong to its definition (ErrForbidden).
func (s *Service) SetVariablesMode(ctx context.Context, orgID, id uuid.UUID, mode SourceMode, ifRevision *int64) (Template, error) {
	cur, err := s.linkable(ctx, orgID, id)
	if err != nil {
		return Template{}, err
	}
	if mode == cur.VarsMode || (mode == ModeOff && !isLinked(cur)) {
		if ifRevision != nil && *ifRevision != cur.Revision {
			return Template{}, ErrRevisionMismatch
		}
		return masked(cur), nil
	}
	next := cur
	next.VarsMode = mode
	switch {
	case mode == ModeOff:
		next.ExtraVarsSchema = unlinked(cur.ExtraVarsSchema)
		next.VarsSourceRev, next.VarsAppliedAt = "", nil
	case mode != ModeManual && mode != ModeAutomatic:
		return Template{}, fmt.Errorf("%w: unknown mode %q; must be off, manual or automatic", ErrInvalidInput, mode)
	case !isLinked(cur):
		return Template{}, fmt.Errorf("%w: the variables are not linked to the playbook yet; link them through the preview", ErrInvalidInput)
	}
	updated, err := s.store.UpdateVarsSource(ctx, next, readRevision(ifRevision, cur), &cur.VarsSourceRev)
	if err != nil {
		return Template{}, err
	}
	return masked(updated), nil
}

// PreviewVariables compares the template's fields with its playbook's
// variable sources as last read. It always answers: a source that is not
// available yields no items, only the source_unavailable note.
func (s *Service) PreviewVariables(ctx context.Context, orgID, id uuid.UUID) (varsync.Preview, SourceInfo, error) {
	cur, err := s.linkable(ctx, orgID, id)
	if err != nil {
		return varsync.Preview{}, SourceInfo{}, err
	}
	vs, err := s.varSource(ctx, orgID, cur.PlaybookID)
	if err != nil {
		return varsync.Preview{}, SourceInfo{}, err
	}
	src := toSource(vs)
	return varsync.Compute(fieldsOf(cur), src, cur.ExtraVars), newSourceInfo(vs, src, cur.VarsSourceRev), nil
}

// ApplyVariables applies a person's preview: it computes the preview anew
// (a preview the client sends back is never applied), refuses one of
// another source revision or sync time (ErrSourceChanged), and stores the resulting
// fields with the values validated and sealed the way a save does them,
// in.Values entered over the stored ones. The template is in in.Mode
// afterwards and records the applied revision.
func (s *Service) ApplyVariables(ctx context.Context, orgID, id uuid.UUID, in ApplyInput) (Template, error) {
	if in.Mode != ModeManual && in.Mode != ModeAutomatic {
		return Template{}, fmt.Errorf("%w: an apply leaves the variables in mode manual or automatic, not %q", ErrInvalidInput, in.Mode)
	}
	cur, err := s.linkable(ctx, orgID, id)
	if err != nil {
		return Template{}, err
	}
	vs, err := s.varSource(ctx, orgID, cur.PlaybookID)
	if err != nil {
		return Template{}, err
	}
	src := toSource(vs)
	if !src.Available {
		return Template{}, fmt.Errorf("%w: nothing to apply: %s", ErrInvalidInput, src.Reason)
	}
	if in.SourceRev != src.Rev || !sameTime(in.SourceSyncedAt, vs.SyncedAt) {
		return Template{}, ErrSourceChanged
	}
	current := fieldsOf(cur)
	sc := &schema.Schema{Fields: varsync.Apply(current, varsync.Compute(current, src, cur.ExtraVars), varsync.ApplyOptions{RemoveGone: in.RemoveGone})}
	incoming, err := applyValues(sc.Fields, cur.ExtraVars, in.Values)
	if err != nil {
		return Template{}, err
	}
	vars, err := s.prepareVars(orgID, id, incoming, sc, cur)
	if err != nil {
		return Template{}, err
	}
	for name, v := range in.Values {
		if v == nil {
			delete(vars, name)
		}
	}
	next := cur
	next.ExtraVars, next.ExtraVarsSchema = vars, sc
	next.VarsMode, next.VarsSourceRev = in.Mode, varsync.AppliedRev(cur.VarsSourceRev, src)
	at := s.now()
	next.VarsAppliedAt = &at
	updated, err := s.store.UpdateVariables(ctx, next, readRevision(in.IfRevision, cur))
	if err != nil {
		return Template{}, err
	}
	return masked(updated), nil
}

// applyValues is what an apply validates under fields: the stored value
// of every active field, a sealed one as the mask a read returns (so the
// save keeps it), with values entered over them (null removes). A stored
// secret under a field that is no longer secret cannot be kept: unless a
// value is entered for it, it is a problem.
func applyValues(fields []schema.Field, stored, values map[string]any) (map[string]any, error) {
	kept := map[string]any{}
	for _, f := range fields {
		v, ok := stored[f.Name]
		if _, entered := values[f.Name]; ok && v != nil && f.IsActive() && !entered {
			kept[f.Name] = v
		}
	}
	var problems []schema.Problem
	for _, stray := range schema.StraySealed(fields, kept) {
		problems = append(problems, schema.Problem{Path: stray.Path,
			Message: "the stored value was a secret and this field no longer is one; enter the value again"})
	}
	if len(problems) > 0 {
		return nil, &schema.ValidationError{Problems: problems}
	}
	out := schema.MaskVars(kept)
	for name, v := range values {
		if v != nil {
			out[name] = v
		}
	}
	return out, nil
}

// sameTime reports whether a and b are the same instant, or both absent.
// A sync with a commit the worker could not read keeps the previous
// commit, so only the sync time tells a read with new content apart.
func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// linkable is the template whose variables a mode change, preview or
// apply works on: ErrForbidden for a projection, whose variables belong to
// its installation-owned definition.
func (s *Service) linkable(ctx context.Context, orgID, id uuid.UUID) (Template, error) {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Template{}, err
	}
	if cur.SharedTemplateID != nil {
		return Template{}, fmt.Errorf("%w: this template is a projection of an installation-owned definition; its variables are not linked to a playbook here", ErrForbidden)
	}
	return cur, nil
}

// varSource is the playbook's variable sources as last read; the zero
// VarSource (nothing found) for a template without a playbook or one that
// no longer exists.
func (s *Service) varSource(ctx context.Context, orgID, playbookID uuid.UUID) (playbooks.VarSource, error) {
	if playbookID == uuid.Nil {
		return playbooks.VarSource{}, nil
	}
	vs, err := s.playbooks.VarSource(ctx, orgID, playbookID)
	if errors.Is(err, playbooks.ErrNotFound) {
		return playbooks.VarSource{}, nil
	}
	if err != nil {
		return playbooks.VarSource{}, fmt.Errorf("read the playbook's variable sources: %w", err)
	}
	return vs, nil
}

// toSource is vs as a preview reads it: available only when the playbook
// was read by the last sync of a worker that reads sources.
func toSource(vs playbooks.VarSource) varsync.Source {
	return varsync.NewSource(varsync.Read{Rev: vs.Rev, Found: vs.Found, LeftOut: vs.LeftOut, WorkerReads: vs.WorkerReads, Playbook: vs.Playbook})
}

func newSourceInfo(vs playbooks.VarSource, src varsync.Source, applied string) SourceInfo {
	return SourceInfo{
		Kind: vs.Kind, Rev: vs.Rev, SyncedAt: vs.SyncedAt, AppliedRev: applied, Newer: varsync.Newer(applied, src),
		WorkerReads: vs.WorkerReads, Incomplete: vs.Incomplete, Found: vs.Found, LeftOut: vs.LeftOut,
		Available: src.Available, Reason: src.Reason, RepoID: vs.RepoID, Path: vs.Path,
	}
}

// isLinked reports whether t's variables follow its playbook's sources.
func isLinked(t Template) bool {
	return t.VarsMode == ModeManual || t.VarsMode == ModeAutomatic
}

// fieldsOf is t's top-level fields, none without a schema.
func fieldsOf(t Template) []schema.Field {
	if t.ExtraVarsSchema == nil {
		return nil
	}
	return t.ExtraVarsSchema.Fields
}

// unlinked is sc with every field manual: origin, source, the source's
// version and set_in_playbook cleared (Task 1's invariant for a field
// that turns manual), content and active switch kept.
func unlinked(sc *schema.Schema) *schema.Schema {
	if sc == nil {
		return nil
	}
	out := &schema.Schema{Fields: make([]schema.Field, len(sc.Fields))}
	for i, f := range sc.Fields {
		f.Origin, f.Source, f.SourceField, f.SetInPlaybook = "", nil, nil, false
		out.Fields[i] = f
	}
	return out
}
