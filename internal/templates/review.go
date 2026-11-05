// What a linked template leaves for a person to review (spec 7.75 goal
// 5): the variables page and the template list show it.
package templates

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/varsync"
)

// Review counts what a template's variables leave for a person: fields
// marked gone from the source (removing them is confirmed), source
// changes held for review, active required fields without a default or
// a value, and source fields not added because of the field limit.
type Review struct {
	Gone            int
	NeedsReview     int
	MissingRequired int
	LeftOut         int
}

// VariablesView is a template's variables page: the template (values
// masked), its playbook's variable sources and what is left to review.
type VariablesView struct {
	Template Template
	Source   SourceInfo
	Review   Review
}

// Variables is the template's variables page.
func (s *Service) Variables(ctx context.Context, orgID, id uuid.UUID) (VariablesView, error) {
	t, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return VariablesView{}, err
	}
	vs, err := s.varSource(ctx, orgID, t.PlaybookID)
	if err != nil {
		return VariablesView{}, err
	}
	src := toSource(vs)
	return VariablesView{Template: masked(t), Source: newSourceInfo(vs, src, t.VarsSourceRev), Review: review(t, src)}, nil
}

// ListReviewed is List with the review of every linked template, by
// template id; the variable sources are read once per playbook.
func (s *Service) ListReviewed(ctx context.Context, orgID uuid.UUID) ([]Template, map[uuid.UUID]Review, error) {
	raw, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, nil, err
	}
	reviews := map[uuid.UUID]Review{}
	sources := map[uuid.UUID]varsync.Source{}
	for _, t := range raw {
		if !isLinked(t) {
			continue
		}
		src, ok := sources[t.PlaybookID]
		if !ok {
			vs, err := s.varSource(ctx, orgID, t.PlaybookID)
			if err != nil {
				return nil, nil, fmt.Errorf("review template %s: %w", t.ID, err)
			}
			src = toSource(vs)
			sources[t.PlaybookID] = src
		}
		reviews[t.ID] = review(t, src)
	}
	list, err := s.listed(ctx, orgID, raw)
	if err != nil {
		return nil, nil, err
	}
	return list, reviews, nil
}

// review counts what t leaves to review. Gone fields and missing values
// are counted from the stored fields; held changes and left-out fields
// from a preview of a linked template's available source.
func review(t Template, src varsync.Source) Review {
	fields := fieldsOf(t)
	var r Review
	for _, f := range fields {
		if f.Origin == schema.OriginGone {
			r.Gone++
		}
		if f.IsActive() && f.Required && f.Default == nil && t.ExtraVars[f.Name] == nil {
			r.MissingRequired++
		}
	}
	if isLinked(t) && src.Available {
		p := varsync.Compute(fields, src, t.ExtraVars)
		for _, c := range p.Changed {
			if c.NeedsReview {
				r.NeedsReview++
			}
		}
		r.LeftOut = p.LeftOut
	}
	return r
}
