// The run request vars adapter (backlog 2.120): the values a run
// receives, secrets in the clear, for the run request resolver's keyed
// vars digest (runrequests.RunVarsSource). It reads what the claim reads:
// the renderer's snapshot of the targets and the template's sendable extra
// vars opened with the box, so the digest changes exactly when the run's
// input does.
package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/templates"
)

type runRequestVars struct {
	lookup   templateLookup
	renderer *inventory.Renderer
}

// NewRunRequestVars builds the adapter both wirings share (app.Build and
// apitest, which mirrors it).
func NewRunRequestVars(t *templates.SQLStore, renderer *inventory.Renderer, box templates.Sealer) runrequests.RunVarsSource {
	return runRequestVars{lookup: templateLookup{templates: t, box: box}, renderer: renderer}
}

// InventoryVars implements runrequests.RunVarsSource: the snapshot the
// claim renders for these hosts, secrets revealed.
func (v runRequestVars) InventoryVars(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (render.Inventory, error) {
	return v.renderer.Snapshot(ctx, orgID, render.Scope{HostIDs: hostIDs})
}

// ExtraVars implements runrequests.RunVarsSource: the template's extra vars
// as TemplateLookup hands them to the claim (only active, declared fields
// when the template has a schema, no stray sealed value, secrets opened),
// read from the store directly for the reason templateLookup's own doc
// comment gives. A stray sealed value is left out without the claim's log
// line: a picture is read on every look at the approval page.
func (v runRequestVars) ExtraVars(ctx context.Context, orgID, templateID uuid.UUID) (map[string]any, error) {
	t, err := v.lookup.templates.Get(ctx, orgID, templateID)
	if err != nil {
		return nil, err
	}
	if t.ExtraVarsSchema != nil {
		t.ExtraVars = schema.Sendable(t.ExtraVarsSchema.Fields, t.ExtraVars)
		t.ExtraVars, _ = withoutStraySealed(t)
	}
	opened, _, err := v.lookup.openExtraVars(t)
	return opened, err
}
