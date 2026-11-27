package runrequests

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
)

// pictureMemo holds, for one picture computation, what its parts would
// otherwise read again (review M1 of backlog 2.143): a run's follow-ups
// are mostly bound to the same hosts, and each binding renders their
// inventory, secrets revealed, and describes them. Keyed by the host ids
// in the order asked: a follow-up asks sorted (bindFollowUp), so equal
// host sets share an entry without the run's own order, which its
// fingerprint already depends on, changing. It lives only as long as the
// computation's context.
type pictureMemo struct {
	inventories map[string]render.Inventory
	hosts       map[string][]PictureHost
	extraVars   map[uuid.UUID]map[string]any
}

type pictureMemoKey struct{}

// withPictureMemo is ctx carrying a fresh memo for one picture.
func withPictureMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, pictureMemoKey{}, &pictureMemo{
		inventories: map[string]render.Inventory{}, hosts: map[string][]PictureHost{}, extraVars: map[uuid.UUID]map[string]any{},
	})
}

// memoOf is ctx's memo, nil outside a picture computation.
func memoOf(ctx context.Context) *pictureMemo {
	m, _ := ctx.Value(pictureMemoKey{}).(*pictureMemo)
	return m
}

func hostSetKey(ids []uuid.UUID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id.String()
	}
	return strings.Join(parts, ",")
}

// targetInventory is the rendered inventory of ids as the vars digest
// reads it (secrets list and gathered facts dropped), from the memo when
// ctx carries one.
func (r *Resolver) targetInventory(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (render.Inventory, error) {
	m, key := memoOf(ctx), hostSetKey(ids)
	if m != nil {
		if inv, ok := m.inventories[key]; ok {
			return inv, nil
		}
	}
	inv, err := r.src.Vars.InventoryVars(ctx, orgID, ids)
	if err != nil {
		return render.Inventory{}, err
	}
	// The redact list only repeats values the document holds.
	inv.Secrets = nil
	inv.Doc = withoutFacts(inv.Doc)
	if m != nil {
		m.inventories[key] = inv
	}
	return inv, nil
}

// templateExtraVars is a template's extra vars as a run receives them,
// from the memo when ctx carries one.
func (r *Resolver) templateExtraVars(ctx context.Context, orgID, templateID uuid.UUID) (map[string]any, error) {
	m := memoOf(ctx)
	if m != nil {
		if v, ok := m.extraVars[templateID]; ok {
			return v, nil
		}
	}
	v, err := r.src.Vars.ExtraVars(ctx, orgID, templateID)
	if err != nil {
		return nil, err
	}
	if m != nil {
		m.extraVars[templateID] = v
	}
	return v, nil
}

// describedHosts is describeHosts' target list for ids, from the memo
// when ctx carries one; the caller gets its own slice.
func (r *Resolver) describedHosts(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]PictureHost, error) {
	m, key := memoOf(ctx), hostSetKey(ids)
	if m != nil {
		if hs, ok := m.hosts[key]; ok {
			return slices.Clone(hs), nil
		}
	}
	var p Picture
	if err := r.describeHosts(ctx, orgID, ids, &p); err != nil {
		return nil, err
	}
	if m != nil {
		m.hosts[key] = slices.Clone(p.Targets)
	}
	return p.Targets, nil
}
