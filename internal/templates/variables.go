// A template's variables — its extra-vars schema and values — and the
// rules a write applies to them (backlog 7.75): the values of inactive or
// removed fields stay stored, verbatim, until a person sets them again
// (spec owner decision 4, plan decision 10), and the variables have a
// write of their own under the template's revision.
package templates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	updated, err := s.store.UpdateVariables(ctx, next, readRevision(in.IfRevision, cur))
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

// readRevision is the revision a read-modify-write is guarded by: the one
// the caller named (If-Match), or else the one the service read, so a write
// landing between this read and this write answers ErrRevisionMismatch
// instead of being undone by a body built on the older row.
func readRevision(ifRevision *int64, cur Template) *int64 {
	if ifRevision != nil {
		return ifRevision
	}
	rev := cur.Revision
	return &rev
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
// before — which is also how a person discards a kept value, in two steps
// for a removed field: declare the field (active) again and omit its
// value, then remove the field in a later save if wanted. A write without
// a schema keeps nothing (prepareVars only carries over under a schema). incoming has been validated already; carried values are not
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
// field) or was secret-typed before it (a removed field, or one retyped
// away from secret and switched inactive), at any depth — the same seal a
// save gives a legacy secret it keeps. Without it such a plaintext would
// read in the clear, since a read only masks what the current schema
// declares secret. Both passes leave a sealed value as it is, so every
// other carried value, a sealed secret included, stays verbatim.
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
	for _, fs := range [][]schema.Field{fields, prev} {
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

// buildKeepingValues is build for a template save (Update) whose values
// failed validation: when the write keeps the schema and every stored value
// as it is — the template dialog echoes them while a person changes the
// template's other settings — a required field without a value is not
// that save's to fix. An automatic apply may have added one; the variables
// page and an apply insist on its value. The write is built again with
// the top-level fields not required (every other check runs as before)
// and taken only when it stores the same schema and values; otherwise
// failed, build's error, is returned.
func (s *Service) buildKeepingValues(ctx context.Context, in UpdateInput, isProjection bool, vars map[string]any, cur Template, failed error) (Template, error) {
	var ve *schema.ValidationError
	if !errors.As(failed, &ve) || in.ExtraVarsSchema == nil || !sameJSON(in.ExtraVarsSchema, cur.ExtraVarsSchema) {
		return Template{}, failed
	}
	relaxed := &schema.Schema{Fields: slices.Clone(in.ExtraVarsSchema.Fields)}
	for i := range relaxed.Fields {
		relaxed.Fields[i].Required = false
	}
	t, err := s.build(ctx, cur.OrgID, cur.ID, in.Name, in.PlaybookID, isProjection, in.Scope, in.Limit, vars, relaxed, in.CredentialID, cur, in.VaultCredentials)
	if err != nil || !sameJSON(t.ExtraVars, cur.ExtraVars) {
		return Template{}, failed
	}
	t.ExtraVarsSchema = in.ExtraVarsSchema
	return t, nil
}

// sameJSON reports whether a and b encode to the same JSON, as the
// database stores them.
func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}
