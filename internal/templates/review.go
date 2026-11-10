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
// changes and additions held for review, active required fields without a default or
// a value, and source fields not added because of the field limit. NotSent
// names the variables a run leaves out until a person enters their value
// again: each holds a stored secret under a field that is no longer a
// secret (backlog 7.87).
type Review struct {
	Gone            int
	NeedsReview     int
	MissingRequired int
	LeftOut         int
	NotSent         []string
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

// List returns every template of the organization, ordered by name, with
// masked extra vars and each one's own vault credential set attached.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Template, error) {
	list, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return s.listed(ctx, orgID, list)
}

// listed is list as List returns it: masked, each with its own vault
// credential set.
func (s *Service) listed(ctx context.Context, orgID uuid.UUID, list []Template) ([]Template, error) {
	// One query for the whole organization (backlog 2.94), not one per
	// row.
	byTemplate, err := s.store.ListOrgVaultCredentials(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list template vault credentials: %w", err)
	}
	out := make([]Template, len(list))
	for i, t := range list {
		t.VaultCredentials = byTemplate[t.ID]
		out[i] = masked(t)
	}
	return out, nil
}

// ListReviewed is List with the review of every linked template, by
// template id; the variable sources are read once per playbook and held
// one playbook at a time. A
// template whose sources cannot be read is listed without a review (and
// logged): the list stays usable.
func (s *Service) ListReviewed(ctx context.Context, orgID uuid.UUID) ([]Template, map[uuid.UUID]Review, error) {
	raw, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, nil, err
	}
	// One playbook's source at a time: it is read once for all the templates
	// that follow it and dropped before the next is read, so a list holds
	// one source, not one per playbook (backlog 7.114).
	var order []uuid.UUID
	byPlaybook := map[uuid.UUID][]Template{}
	for _, t := range raw {
		if !isLinked(t) {
			continue
		}
		if _, seen := byPlaybook[t.PlaybookID]; !seen {
			order = append(order, t.PlaybookID)
		}
		byPlaybook[t.PlaybookID] = append(byPlaybook[t.PlaybookID], t)
	}
	reviews := map[uuid.UUID]Review{}
	for _, playbookID := range order {
		vs, err := s.varSource(ctx, orgID, playbookID)
		if err != nil {
			s.logger().ErrorContext(ctx, "template list: variables review left out", "org", orgID, "playbook", playbookID, "err", err)
			continue
		}
		src := toSource(vs)
		for _, t := range byPlaybook[playbookID] {
			reviews[t.ID] = review(t, src)
		}
	}
	list, err := s.listed(ctx, orgID, raw)
	if err != nil {
		return nil, nil, err
	}
	return list, reviews, nil
}

// review counts what t leaves to review. Gone fields, missing values and
// the variables not sent are counted from the stored fields; held changes and left-out fields
// from a preview of a linked template's available source.
func review(t Template, src varsync.Source) Review {
	fields := fieldsOf(t)
	// What the claim drops (app.dropStraySealed) from what it sends
	// (schema.Sendable): a value under an inactive field is not sent
	// either way.
	r := Review{NotSent: schema.StrayTopLevel(fields, schema.Sendable(fields, t.ExtraVars))}
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
		for _, a := range p.Added {
			if a.NeedsReview {
				r.NeedsReview++
			}
		}
		r.LeftOut = p.LeftOut
	}
	return r
}
