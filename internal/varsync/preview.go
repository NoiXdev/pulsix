// Package varsync compares a job template's variable fields with its
// playbook's variable sources and applies the result (spec 7.75 goal 5):
// Compute builds the preview, Apply writes it into the fields. It is pure:
// no I/O, no database.
package varsync

import (
	"fmt"
	"strings"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/varsource"
)

// Change is one attribute of a field that differs: old and new value,
// absent when the attribute is unset.
type Change struct {
	Attr string `json:"attr"`
	Old  any    `json:"old,omitempty"`
	New  any    `json:"new,omitempty"`
}

// ReviewReason says why automatic mode holds a change for a person.
type ReviewReason string

// Review reasons.
const (
	// ReasonType: the field's type changes.
	ReasonType ReviewReason = "type"
	// ReasonSecret: the field, or a sub-field, turns secret or stops being
	// one; only a person seals or unseals a stored value.
	ReasonSecret ReviewReason = "secret"
	// ReasonValue: the stored value no longer fits the new field.
	ReasonValue ReviewReason = "value"
	// ReasonCut: the sub-fields change while a limit cut fields from the
	// source, so the change may come from the cut.
	ReasonCut ReviewReason = "cut"
)

// Added is a field the source declares and the template does not have,
// as it will be stored (origin source, its default activation).
type Added struct {
	Field schema.Field `json:"field"`
}

// Changed is a template field that takes the source's new content: a
// source field the source changed, or a gone field the source declares
// again. Field is the field as it will be stored.
type Changed struct {
	Name  string       `json:"name"`
	Field schema.Field `json:"field"`
	// CurrentOrigin is the field's origin Compute saw; Apply skips the
	// item when the field's origin changed since.
	CurrentOrigin schema.Origin `json:"current_origin"`
	Changes       []Change      `json:"changes"`
	NeedsReview   bool          `json:"needs_review"`
	Reason        ReviewReason  `json:"reason,omitempty"`
}

// Adjusted is a field a person adjusted whose source changed: the
// adjustment stays, SourceField is the source's new version, Changes are
// between its old and new version.
type Adjusted struct {
	Name        string       `json:"name"`
	Field       schema.Field `json:"field"`
	SourceField schema.Field `json:"source_field"`
	Changes     []Change     `json:"changes"`
}

// Gone is a field the source was read completely without; Field is the
// field as it will be stored when marked (origin gone).
type Gone struct {
	Name  string       `json:"name"`
	Field schema.Field `json:"field"`
	// CurrentOrigin is the field's origin Compute saw; Apply skips the
	// item when the field's origin changed since.
	CurrentOrigin schema.Origin `json:"current_origin"`
}

// Preview is what applying a source would change in a template's fields.
// Apply takes the Preview Compute returned for the same current fields:
// Refresh does not travel through JSON, so a preview decoded from JSON
// applies its items but not the moved source locations.
type Preview struct {
	SourceRev string     `json:"source_rev"`
	Added     []Added    `json:"added"`
	Changed   []Changed  `json:"changed"`
	Adjusted  []Adjusted `json:"adjusted"`
	Gone      []Gone     `json:"gone"`
	Unchanged int        `json:"unchanged"`
	// LeftOut counts the source's fields not added because the template
	// would hold more than schema.MaxFields (the fields_left_out warning).
	LeftOut int              `json:"left_out"`
	Notes   []varsource.Note `json:"notes"`

	// Refresh holds the unchanged fields whose provenance moved (source
	// location, set_in_playbook, an adjusted field's source version), as
	// Compute saw them with the new provenance.
	Refresh map[string]schema.Field `json:"-"`
}

// Compute compares current, a template's fields, with src. stored are the
// template's stored values, which decide whether a change needs review.
// A source that is not available yields no items, only why; manual fields
// are never touched. Added stops where the template would hold more than
// schema.MaxFields fields (gone ones count until removed), with a
// fields_left_out warning naming the rest.
func Compute(current []schema.Field, src Source, stored map[string]any) Preview {
	pb := src.Playbook
	p := Preview{SourceRev: src.Rev, Added: []Added{}, Changed: []Changed{}, Adjusted: []Adjusted{}, Gone: []Gone{},
		Notes: []varsource.Note{}, Refresh: map[string]schema.Field{}}
	if !src.Available {
		reason := src.Reason
		if reason == "" {
			reason = "the playbook's variable sources are not available"
		}
		p.Notes = append(p.Notes, varsource.Note{Level: varsource.LevelWarning, Code: codeSourceUnavailable, File: pb.Path,
			Message: reason + "; the fields stay as they are"})
		p.Notes = append(p.Notes, pb.Notes...)
		return p
	}
	c := comparison{p: &p, stored: stored, capped: capped(pb), absence: newAbsence(pb), candidates: make(map[string]schema.Field, len(pb.Fields))}
	for _, f := range pb.Fields {
		c.candidates[f.Name] = f
	}
	have := make(map[string]bool, len(current))
	for _, f := range current {
		have[f.Name] = true
		c.compare(f)
	}
	room := max(schema.MaxFields-len(current), 0)
	var left []string
	for _, f := range pb.Fields {
		switch {
		case have[f.Name]:
		case len(p.Added) < room:
			p.Added = append(p.Added, Added{Field: added(f)})
		default:
			left = append(left, f.Name)
		}
	}
	p.LeftOut = len(left)
	p.Notes = append(p.Notes, fieldsLeftOut(left, pb.Path)...)
	p.Notes = append(p.Notes, c.absence.note(pb.Path)...)
	p.Notes = append(p.Notes, c.taken...)
	p.Notes = append(p.Notes, pb.Notes...)
	return p
}

// comparison is one Compute's state.
type comparison struct {
	p          *Preview
	stored     map[string]any
	capped     bool
	absence    *absence
	candidates map[string]schema.Field
	taken      []varsource.Note
}

// compare files one current field under the preview's lists.
func (c *comparison) compare(cur schema.Field) {
	cand, declared := c.candidates[cur.Name]
	switch {
	case cur.EffectiveOrigin() == schema.OriginManual:
		if declared {
			c.taken = append(c.taken, manualNameTaken(cand))
		}
	case !linked(cur):
		// An unknown origin is not the sync's to touch.
	case !declared:
		if c.absence.gone(cur.Name) {
			c.p.Gone = append(c.p.Gone, Gone{Name: cur.Name, Field: markGone(cur), CurrentOrigin: cur.EffectiveOrigin()})
		}
	case cur.Origin == schema.OriginGone:
		c.returning(cur, cand)
	case cur.Origin == schema.OriginAdjusted:
		c.adjusted(cur, cand)
	default:
		c.source(cur, cand)
	}
}

// source compares a field that follows the source.
func (c *comparison) source(cur, cand schema.Field) {
	if sameContent(cur, cand) {
		c.unchanged(cur, withProvenance(cur, cand))
		return
	}
	next := fromSource(cand, cur.Active)
	changes := diff(content(cur), content(cand))
	c.changed(cur, next, changes)
}

// adjusted compares a field a person adjusted: its content stays, its
// record of the source's version follows the source.
func (c *comparison) adjusted(cur, cand schema.Field) {
	next := withProvenance(cur, cand)
	sf := content(cand)
	next.SourceField = &sf
	if cur.SourceField != nil && sameContent(*cur.SourceField, cand) {
		c.unchanged(cur, next)
		return
	}
	var old schema.Field
	if cur.SourceField != nil {
		old = content(*cur.SourceField)
	}
	c.p.Adjusted = append(c.p.Adjusted, Adjusted{Name: cur.Name, Field: next, SourceField: sf, Changes: diff(old, sf)})
}

// returning brings a gone field the source declares again back: as source
// with the source's content, or as adjusted (its content kept) when it had
// been adjusted.
func (c *comparison) returning(cur, cand schema.Field) {
	if cur.SourceField != nil {
		next := withProvenance(cur, cand)
		next.Origin = schema.OriginAdjusted
		sf := content(cand)
		next.SourceField = &sf
		c.p.Changed = append(c.p.Changed, Changed{Name: cur.Name, Field: next, CurrentOrigin: schema.OriginGone,
			Changes: []Change{originChange(schema.OriginAdjusted)}})
		return
	}
	next := fromSource(cand, cur.Active)
	changes := append([]Change{originChange(schema.OriginSource)}, diff(content(cur), content(cand))...)
	c.changed(cur, next, changes)
}

func (c *comparison) changed(cur, next schema.Field, changes []Change) {
	needs, why := review(content(cur), content(next), c.stored, c.capped, changes)
	c.p.Changed = append(c.p.Changed, Changed{Name: cur.Name, Field: next, CurrentOrigin: cur.EffectiveOrigin(),
		Changes: changes, NeedsReview: needs, Reason: why})
}

// unchanged counts a field whose content the source did not change and
// keeps its refreshed provenance for Apply when that moved.
func (c *comparison) unchanged(cur, next schema.Field) {
	c.p.Unchanged++
	if !sameJSON(cur, next) {
		c.p.Refresh[cur.Name] = next
	}
}

// maxNamesListed bounds the names a fields_left_out warning lists.
const maxNamesListed = 10

// fieldsLeftOut is the warning about source fields not added because the
// template would hold more than schema.MaxFields fields (counting gone
// fields, which stay until a person removes them).
func fieldsLeftOut(names []string, file string) []varsource.Note {
	if len(names) == 0 {
		return nil
	}
	listed := strings.Join(names[:min(len(names), maxNamesListed)], ", ")
	if len(names) > maxNamesListed {
		listed += fmt.Sprintf(" and %d more", len(names)-maxNamesListed)
	}
	return []varsource.Note{{Level: varsource.LevelWarning, Code: codeFieldsLeftOut, File: file,
		Message: fmt.Sprintf("%d fields from the source are not added, because a template holds at most %d fields: %s; remove gone fields and preview again to add the rest", len(names), schema.MaxFields, listed)}}
}

func originChange(to schema.Origin) Change {
	return Change{Attr: "origin", Old: schema.OriginGone, New: to}
}

func manualNameTaken(cand schema.Field) varsource.Note {
	n := varsource.Note{Level: varsource.LevelHint, Code: codeManualNameTaken,
		Message: fmt.Sprintf("%s is a manual field of this template; the playbook's declaration of it is not used", cand.Name)}
	if cand.Source != nil {
		n.File, n.Line = cand.Source.File, cand.Source.Line
	}
	return n
}

// added is a new field from the source as stored: origin source, active
// when it is required without a default and the playbook does not set it
// (spec 7.75 default activation).
func added(cand schema.Field) schema.Field {
	return fromSource(cand, new(cand.Required && cand.Default == nil && !cand.SetInPlaybook))
}

// fromSource is the field the source declares, as a field that follows
// it, with the given active switch.
func fromSource(cand schema.Field, active *bool) schema.Field {
	f := withProvenance(content(cand), cand)
	f.Origin, f.Active = schema.OriginSource, active
	return f
}

// withProvenance is f with the source location and set_in_playbook of
// the source's declaration cand.
func withProvenance(f, cand schema.Field) schema.Field {
	f.Source, f.SetInPlaybook = nil, cand.SetInPlaybook
	if cand.Source != nil {
		src := *cand.Source
		f.Source = &src
	}
	return f
}
