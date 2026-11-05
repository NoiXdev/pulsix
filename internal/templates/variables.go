// A template's variables — its extra-vars schema and values — and the
// rules a write applies to them (backlog 7.75): the values of inactive or
// removed fields stay stored, verbatim, until a person sets them again
// (spec owner decision 4, plan decision 10), and the variables have a
// write of their own under the template's revision.
package templates

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// SourceMode says whether a template's variables follow its playbook's
// variable sources: not at all (off, every field manual), when a person
// applies a preview (manual), or after every repo sync (automatic).
type SourceMode string

// Source modes, as stored in job_templates.vars_mode.
const (
	ModeOff       SourceMode = "off"
	ModeManual    SourceMode = "manual"
	ModeAutomatic SourceMode = "automatic"
)

// VariablesInput replaces a template's schema and values (PUT semantics,
// with the values of inactive and removed fields carried over). A nil
// Schema removes the schema; IfRevision, when set, is the revision the
// write is based on.
type VariablesInput struct {
	Schema     *schema.Schema
	ExtraVars  map[string]any
	IfRevision *int64
}

// UpdateVariables replaces a template's schema and values with the same
// checks and sealing a template save applies, and leaves every other
// setting — the variables source mode included — as it is. A projection's
// variables belong to its definition (refuseSharedFieldChange).
func (s *Service) UpdateVariables(ctx context.Context, orgID, id uuid.UUID, in VariablesInput) (Template, error) {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Template{}, err
	}
	vars := in.ExtraVars
	if vars == nil {
		vars = map[string]any{}
	}
	if cur.SharedTemplateID != nil {
		if err := refuseSharedFieldChange(cur, cur.Name, cur.PlaybookID, cur.Limit, withCarriedOver(in.Schema, vars, cur.ExtraVars), in.Schema); err != nil {
			return Template{}, err
		}
	}
	if vars, err = s.prepareVars(orgID, id, vars, in.Schema, cur); err != nil {
		return Template{}, err
	}
	next := cur
	next.ExtraVars, next.ExtraVarsSchema = vars, in.Schema
	updated, err := s.store.UpdateVariables(ctx, next, in.IfRevision)
	if err != nil {
		return Template{}, err
	}
	items, err := s.store.ListVaultCredentials(ctx, orgID, id)
	if err != nil {
		return Template{}, fmt.Errorf("list template vault credentials: %w", err)
	}
	updated.VaultCredentials = items
	return masked(updated), nil
}

// prepareVars checks a write's extra vars and returns them as stored:
// reserved names refused, Ansible markers checked, and — with a schema —
// the schema checked, secrets sealed, the values validated, and the stored
// values of inactive and removed fields carried over (carryOver, then
// sealCarried). cur is the template before this write (zero on create).
func (s *Service) prepareVars(orgID, id uuid.UUID, vars map[string]any, sc *schema.Schema, cur Template) (map[string]any, error) {
	if vars == nil {
		vars = map[string]any{}
	}
	// Extra vars carry Ansible's highest precedence: the variables that
	// decide whether a pinned host key is checked (and with which SSH
	// arguments) are reserved for the worker (re-review N1).
	if names := ansiblevars.ForbiddenIn(vars); len(names) > 0 {
		return nil, forbiddenProblems(names)
	}
	// ForbiddenIn only looks at TOP-LEVEL names. A nested object with a
	// key starting with "__ansible" that is not a well-formed marker is
	// what makes Ansible's JSON load of the run's extra-vars file fail,
	// and the worker's YAML guard then stops the run rather than letting
	// it proceed with every unsafe marker silently dropped. That failure
	// is unreadable where it lands — a parse error in a document the
	// operator never wrote — so it belongs here, where the mistake is
	// made (backlog 2.71). Checked before sealing, which replaces secret
	// values with markers of its own.
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if err := ansiblevars.CheckMarkers(vars[name]); err != nil {
			return nil, &schema.ValidationError{Problems: []schema.Problem{{
				Path:    name,
				Message: fmt.Sprintf("Ansible could not load this: %v", err),
			}}}
		}
	}
	if sc == nil {
		return vars, nil
	}
	if sc.Fields == nil {
		sc.Fields = []schema.Field{}
	}
	if err := sc.Check(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadSchema, err)
	}
	aad := ExtraVarsAAD(orgID, id)
	sealed, err := s.sealExtraVars(aad, sc.Fields, vars, cur.ExtraVars)
	if err != nil {
		return nil, err
	}
	var prev []schema.Field
	if cur.ExtraVarsSchema != nil {
		prev = cur.ExtraVarsSchema.Fields
	}
	return s.sealCarried(aad, sc.Fields, prev, sealed, carryOver(sc.Fields, sealed, cur.ExtraVars))
}

// carryOver returns incoming plus every stored value whose key incoming
// omits and whose field is inactive or no longer declared, verbatim: a
// sealed secret stays the same sealed bytes, and a value that no longer
// fits its field is neither checked nor dropped. A person brings such a
// value back by switching the field active, or adding it again, with the
// value a read returned. An active field incoming omits is cleared, as
// before. incoming has been validated already; carried values are not
// validated, which is what lets them stay. (prepareVars then seals a
// legacy plaintext secret among them, sealCarried.)
func carryOver(fields []schema.Field, incoming, stored map[string]any) map[string]any {
	active := make(map[string]bool, len(fields))
	for _, f := range fields {
		if f.IsActive() {
			active[f.Name] = true
		}
	}
	out := maps.Clone(incoming)
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range stored {
		if _, sent := incoming[k]; sent || active[k] {
			continue
		}
		out[k] = v
	}
	return out
}

// sealCarried seals a legacy plaintext secret among the values carryOver
// added to incoming — one stored before sealing existed (backlog 2.47)
// under a field that is secret-typed in this write's schema (an inactive
// field) or was secret-typed before it (a removed field) — the same seal a
// save gives a legacy secret it keeps. Without it a removed field's
// plaintext would read in the clear, since a read only masks what the
// current schema declares secret. Every other carried value, a sealed
// secret included, stays verbatim.
func (s *Service) sealCarried(aad []byte, fields, prev []schema.Field, incoming, out map[string]any) (map[string]any, error) {
	carried := map[string]any{}
	for k, v := range out {
		if _, sent := incoming[k]; !sent {
			carried[k] = v
		}
	}
	if len(carried) == 0 {
		return out, nil
	}
	declared := make(map[string]bool, len(fields))
	for _, f := range fields {
		declared[f.Name] = true
	}
	removed := make([]schema.Field, 0, len(prev))
	for _, f := range prev {
		if !declared[f.Name] {
			removed = append(removed, f)
		}
	}
	seal := func(path string, v any) (any, error) {
		plain, ok := v.(string)
		if !ok || plain == "" {
			return v, nil
		}
		ct, err := s.box.SealAAD([]byte(plain), aad)
		if err != nil {
			return nil, fmt.Errorf("seal extra var %s: %w", path, err)
		}
		return schema.Sealed(ct), nil
	}
	for _, fs := range [][]schema.Field{fields, removed} {
		var err error
		if carried, err = schema.MapSecrets(fs, carried, seal); err != nil {
			return nil, err
		}
	}
	maps.Copy(out, carried)
	return out, nil
}

// withCarriedOver is carryOver for a body that may come without a schema:
// without one there is no active switch and the body is taken as it is.
func withCarriedOver(sc *schema.Schema, incoming, stored map[string]any) map[string]any {
	if sc == nil {
		return incoming
	}
	return carryOver(sc.Fields, incoming, stored)
}
