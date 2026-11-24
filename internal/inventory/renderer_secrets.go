package inventory

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// varRow is one group's or one membership's stored vars inside a
// render.Input, with what preparing them for rendering needs.
type varRow struct {
	subject string // names the row in errors
	fields  []schema.Field
	aad     []byte
	vars    *map[string]any // the row's Vars inside the render.Input
	group   uuid.UUID
	host    uuid.UUID // uuid.Nil for a group row
}

func (v varRow) carriedBy(c render.Carried) bool {
	if v.host == uuid.Nil {
		return c.Groups[v.group]
	}
	return c.Memberships[render.MembershipKey{GroupID: v.group, HostID: v.host}]
}

// varRows points at every group's and membership's vars in in, groups
// first, each in in's order. in must not grow afterwards.
func varRows(orgID uuid.UUID, in *render.Input, byID map[uuid.UUID]Group, hostNames map[uuid.UUID]string) []varRow {
	rows := make([]varRow, 0, len(in.Groups)+len(in.Memberships))
	for i := range in.Groups {
		g := &in.Groups[i]
		rows = append(rows, varRow{subject: "inventory group " + g.Name, fields: g.Schema.GroupFields(),
			aad: GroupAAD(orgID, g.ID), vars: &g.Vars, group: g.ID})
	}
	for i := range in.Memberships {
		m := &in.Memberships[i]
		g := byID[m.GroupID]
		rows = append(rows, varRow{subject: "inventory group " + g.Name + ", host " + hostNames[m.HostID],
			fields: g.Schema.MembershipFields(), aad: MemberAAD(orgID, m.GroupID, m.HostID), vars: &m.Vars,
			group: m.GroupID, host: m.HostID})
	}
	return rows
}

// mask prepares every row for a preview: every non-null value of a secret
// field and every other sealed value, declared or not, becomes
// render.MaskedSecret, so no ciphertext envelope and no plaintext stored
// before a field became secret is ever shown. It never opens a ciphertext.
func mask(rows []varRow) error {
	for _, row := range rows {
		masked, err := schema.MapSecrets(row.fields, *row.vars, func(string, any) (any, error) { return render.MaskedSecret, nil })
		if err != nil {
			return err
		}
		*row.vars = schema.ReplaceSealed(masked, func() any { return render.MaskedSecret })
	}
	return nil
}

// reveal prepares rows for a snapshot and returns the secret values it
// revealed.
//
// First it fails closed with ErrUnrenderable when a declared field that is
// no longer secret still holds a sealed value, in any row of the whole
// organization: Ansible must neither receive the envelope nor silently
// lose the value, and a snapshot for one group or host still fails on a
// stray that belongs to a group or host entirely outside its scope
// (backlog 2.22's decision, recorded in the v0.2b plan).
//
// Then it opens the sealed values of secret fields, but only in the rows
// render.Carries names: the groups and memberships whose vars the rendered
// document carries. A run for one host does not decrypt the rest of the
// organization. render.Carries makes the very selection render.Render
// makes, from the same render.Input, so the narrowing happens inside the
// one read transaction (backlog 2.16). A row it misses would lose its
// secrets silently — render.Render's schema.Resolve drops a value still
// sealed — which is why the selection lives in one place. A row it does
// not name keeps its stored vars: render.Render never puts them in the
// document, and Resolve would drop the sealed values if it did.
//
// The accepted gap: a carried row's sealed value that schema.Resolve then
// leaves out (an inactive field, a value whose shape no longer fits) is
// still opened; it stays inside the run's scope and secretsInDoc keeps it
// off the redact list. Undeclared (orphaned) values stay sealed; the
// renderer drops them.
func (r *Renderer) reveal(in render.Input, rows []varRow) ([]string, error) {
	for _, row := range rows {
		if stray := schema.StraySealed(row.fields, *row.vars); len(stray) > 0 {
			return nil, fmt.Errorf("%w: %s: %s", ErrUnrenderable, row.subject, straySealedMessage(stray))
		}
	}
	carried, err := r.carries(in)
	if err != nil {
		return nil, err
	}
	var revealed []string
	for _, row := range rows {
		if !row.carriedBy(carried) {
			continue
		}
		opened, err := r.open(row, &revealed)
		if err != nil {
			return nil, err
		}
		*row.vars = opened
	}
	return revealed, nil
}

// open returns row's vars with the sealed values of its secret fields
// opened, appending every value it reveals to revealed.
func (r *Renderer) open(row varRow, revealed *[]string) (map[string]any, error) {
	return schema.MapSecrets(row.fields, *row.vars, func(_ string, v any) (any, error) {
		ct, ok := schema.SealedBytes(v)
		if !ok {
			// A plain value left under a field changed to secret (a schema
			// change reseals nothing, see schema.MaskDeclared) is a secret
			// to every read, so the run's redact list names it too
			// (backlog 7.12).
			if s, isString := v.(string); isString && s != "" {
				*revealed = append(*revealed, s)
			}
			return v, nil
		}
		plain, err := r.box.OpenAAD(ct, row.aad)
		if err != nil {
			return nil, fmt.Errorf("open sealed inventory value: %w", err)
		}
		*revealed = append(*revealed, string(plain))
		return string(plain), nil
	})
}
