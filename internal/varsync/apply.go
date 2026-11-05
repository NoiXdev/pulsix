package varsync

import "github.com/noixdev/pulsix/internal/inventory/schema"

// ApplyOptions says who applies a preview. Automatic (after a sync) holds
// changes that need review and never removes; a person applies everything
// and removes gone fields only with RemoveGone.
type ApplyOptions struct {
	Automatic  bool
	RemoveGone bool
}

// Apply writes p, computed from current, into a copy of current: the
// existing order kept, added fields appended in the source's order. A
// field's active switch never changes once it exists, and a manual field
// is never touched, even by a preview computed against other fields.
func Apply(current []schema.Field, p Preview, opt ApplyOptions) []schema.Field {
	next := make(map[string]schema.Field, len(p.Changed)+len(p.Adjusted)+len(p.Gone)+len(p.refresh))
	for name, f := range p.refresh {
		next[name] = f
	}
	for _, c := range p.Changed {
		if !opt.Automatic || !c.NeedsReview {
			next[c.Name] = c.Field
		}
	}
	for _, a := range p.Adjusted {
		next[a.Name] = a.Field
	}
	remove := map[string]bool{}
	for _, g := range p.Gone {
		next[g.Name] = g.Field
		remove[g.Name] = !opt.Automatic && opt.RemoveGone
	}

	out := make([]schema.Field, 0, len(current)+len(p.Added))
	have := make(map[string]bool, len(current))
	for _, cur := range current {
		have[cur.Name] = true
		f, ok := next[cur.Name]
		switch {
		case !ok || !linked(cur):
			out = append(out, cur)
		case remove[cur.Name]:
		default:
			f.Active = cur.Active
			out = append(out, f)
		}
	}
	for _, a := range p.Added {
		if !have[a.Field.Name] {
			have[a.Field.Name] = true
			out = append(out, a.Field)
		}
	}
	return out
}
