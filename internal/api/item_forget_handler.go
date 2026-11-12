package api

import (
	"context"
	"fmt"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// forgetItemInput addresses one item of one host. The item travels in the
// query, URL-encoded, never in the path: its name comes from an agent and
// may contain any character, a "/" included. check_key and item are both
// required, but neither is huma-required: huma reads an empty query value
// as a missing one, and item= (present, empty) is a real request — the
// whole-host item "". Resolve records whether item was sent at all, so a
// client that left it out is refused instead of wiping the host-wide
// values by accident; a missing check_key is the service's own
// ErrInvalidInput. Resolve also records a repeated check_key or item: the
// request is ambiguous, and huma would quietly keep just one of them, so
// it is refused rather than forgetting whichever item the decoder picked.
type forgetItemInput struct {
	Org      string `path:"org"`
	ID       string `path:"id"`
	CheckKey string `query:"check_key" doc:"Required, once: the check the item belongs to"`
	Item     string `query:"item" doc:"Required, once, may be empty: the item's name, URL-encoded; item= (empty) is the whole-host item"`
	itemSet  bool
	repeated bool
}

func (in *forgetItemInput) Resolve(ctx huma.Context) []error {
	u := ctx.URL()
	q := u.Query()
	in.itemSet = q.Has("item")
	in.repeated = len(q["item"]) > 1 || len(q["check_key"]) > 1
	return nil
}

// registerItemForgetRoute wires DELETE /hosts/{id}/items (backlog 3.12b,
// spec docs/superpowers/specs/2026-11-12-forget-items-design.md, "Manual
// forget"): forget one item of a host at once — its current values, rule
// states and their history, its discovery row, and its open alerts,
// closed as forgotten. Samples (graphs) and item decisions stay.
//
// The role is the item decisions' (checks.write): forgetting changes what
// is judged, exactly like a decision does. The server forgets a fresh item
// too when asked; the host page simply does not offer it (spec, "Claude's
// call"). Nor can the page offer it for an item its check no longer asks
// for: GET /hosts/{id}/checks hides those values (stillAsked,
// host_check_handlers.go), so on the host page only stale rows carry the
// action and the not-asked ones are left to the automatic expiry.
//
// The audit entry is the middleware's, written after the forget's own
// transaction has committed — the same order every other audited route
// here has, and no outer transaction holds the host's evaluation lock
// while it is written.
func registerItemForgetRoute(hapi huma.API, deps Deps) {
	huma.Delete(hapi, "/api/v1/orgs/{org}/hosts/{id}/items", func(ctx context.Context, in *forgetItemInput) (*noContentOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		hostID, err := parseUUID(in.ID, "host id")
		if err != nil {
			return nil, err
		}
		if in.repeated {
			return nil, huma.Error400BadRequest("check_key and item may each be sent only once")
		}
		if !in.itemSet {
			return nil, huma.Error400BadRequest("item is required; send item= (empty) for the whole-host item")
		}
		if err := deps.Checks.ForgetItem(ctx, scope.OrgID, hostID, in.CheckKey, in.Item); err != nil {
			return nil, mapChecksErr(ctx, err)
		}
		setAuditObject(ctx, hostID.String(), fmt.Sprintf("forgot item %q of check %q", in.Item, in.CheckKey))
		return &noContentOutput{}, nil
	}, withAudit("forgetHostItem", "host"))
}
