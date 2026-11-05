package varsync

import "github.com/noixdev/pulsix/internal/inventory/schema"

// ApplyOptions says who applies a preview. Automatic (after a sync) holds
// changes that need review and never removes; a person applies everything
// and removes gone fields only with RemoveGone.
type ApplyOptions struct {
	Automatic  bool
	RemoveGone bool
}

// Apply writes p into a copy of current, which must be the fields Compute
// built p from: the existing order kept, added fields appended in the
// source's order up to schema.MaxFields. A field's active switch never
// changes once it exists, and a manual field is never touched. An item
// whose field changed origin since Compute (a person adjusted it, made it
// manual or removed it) is skipped; an adjusted field only takes the new
// provenance, so a person's adjustment is never overwritten.
func Apply(current []schema.Field, p Preview, opt ApplyOptions) []schema.Field {
	changed := make(map[string]Changed, len(p.Changed))
	for _, c := range p.Changed {
		if !opt.Automatic || !c.NeedsReview {
			changed[c.Name] = c
		}
	}
	adjusted := make(map[string]Adjusted, len(p.Adjusted))
	for _, a := range p.Adjusted {
		adjusted[a.Name] = a
	}
	gone := make(map[string]Gone, len(p.Gone))
	for _, g := range p.Gone {
		gone[g.Name] = g
	}

	out := make([]schema.Field, 0, len(current)+len(p.Added))
	have := make(map[string]bool, len(current))
	for _, cur := range current {
		have[cur.Name] = true
		origin := cur.EffectiveOrigin()
		c, isChanged := changed[cur.Name]
		a, isAdjusted := adjusted[cur.Name]
		g, isGone := gone[cur.Name]
		r, isRefresh := p.Refresh[cur.Name]
		switch {
		case !linked(cur):
			out = append(out, cur)
		case isChanged && c.CurrentOrigin == origin:
			f := c.Field
			f.Active = cur.Active
			out = append(out, f)
		case isAdjusted && origin == schema.OriginAdjusted:
			out = append(out, provenance(cur, a.Field))
		case isGone && g.CurrentOrigin == origin:
			if !opt.Automatic && opt.RemoveGone {
				continue
			}
			out = append(out, markGone(cur))
		case isRefresh && r.EffectiveOrigin() == origin:
			out = append(out, provenance(cur, r))
		default:
			out = append(out, cur)
		}
	}
	for _, a := range p.Added {
		if !have[a.Field.Name] && len(out) < schema.MaxFields {
			have[a.Field.Name] = true
			out = append(out, a.Field)
		}
	}
	return out
}

// provenance is cur with from's source location, set_in_playbook and, for
// an adjusted field, source version; cur's content and active stay.
func provenance(cur, from schema.Field) schema.Field {
	cur.Source, cur.SetInPlaybook = from.Source, from.SetInPlaybook
	if cur.Origin == schema.OriginAdjusted && from.SourceField != nil {
		cur.SourceField = from.SourceField
	}
	return cur
}
