// A person's own personal tokens (backlog 3.100, spec docs/superpowers/
// specs/2026-11-17-personal-tokens-design.md): /api/v1/auth/me/tokens.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/personaltokens"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The details of the 409s the SPA matches on: a contract, not prose.
const (
	personalTokenLimit = "personal_token_limit"
	personalTokensOff  = "personal_tokens_off"
	grantBlocked       = "grant_blocked"
)

type personalGrantInput struct {
	Org  string `json:"org" minLength:"1" doc:"The organization's slug"`
	Role string `json:"role" enum:"owner,admin,operator,viewer" doc:"The highest role the token acts with there; at most your own role"`
}

type personalGrantDTO struct {
	OrgID      string     `json:"org_id"`
	OrgSlug    string     `json:"org_slug"`
	OrgName    string     `json:"org_name"`
	Role       string     `json:"role" enum:"owner,admin,operator,viewer"`
	State      string     `json:"state" enum:"active,blocked,off" doc:"active; blocked by an organization admin; off while the organization does not allow personal tokens"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// personalTokenDTO is a personal token without its secret.
type personalTokenDTO struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Kind       string             `json:"kind" enum:"api,assistant"`
	Areas      []string           `json:"areas,omitempty"`
	ExpiresAt  *time.Time         `json:"expires_at,omitempty"`
	LastUsedAt *time.Time         `json:"last_used_at,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
	Grants     []personalGrantDTO `json:"grants"`
}

type personalTokenListOutput struct {
	Body struct {
		Tokens []personalTokenDTO `json:"tokens"`
	}
}

type personalTokenCreateInput struct {
	Body struct {
		Name      string               `json:"name" minLength:"1" maxLength:"200"`
		Kind      string               `json:"kind,omitempty" enum:"api,assistant" doc:"api (the default) or assistant"`
		Areas     []string             `json:"areas,omitempty" doc:"Assistant tokens only: the areas the token may use, within its highest role"`
		ExpiresAt *time.Time           `json:"expires_at,omitempty"`
		Grants    []personalGrantInput `json:"grants" minItems:"1" maxItems:"50" doc:"The organizations the token acts in, each with its role cap"`
	}
}

// personalTokenCreatedDTO is personalTokenDTO plus the plaintext, which
// this one answer carries and nothing else ever can: only its hash is
// stored.
type personalTokenCreatedDTO struct {
	Token      string             `json:"token"`
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Kind       string             `json:"kind" enum:"api,assistant"`
	Areas      []string           `json:"areas,omitempty"`
	ExpiresAt  *time.Time         `json:"expires_at,omitempty"`
	LastUsedAt *time.Time         `json:"last_used_at,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
	Grants     []personalGrantDTO `json:"grants"`
}

type personalTokenCreateOutput struct{ Body personalTokenCreatedDTO }

type personalTokenIDInput struct {
	ID string `path:"id"`
}

type personalTokenUpdateInput struct {
	ID   string `path:"id"`
	Body struct {
		Name         *string               `json:"name,omitempty" minLength:"1" maxLength:"200"`
		Areas        *[]string             `json:"areas,omitempty" doc:"Assistant tokens only: replaces the areas"`
		ExpiresAt    *time.Time            `json:"expires_at,omitempty" doc:"A new expiry; must be in the future"`
		NeverExpires bool                  `json:"never_expires,omitempty" doc:"Remove the expiry"`
		Grants       *[]personalGrantInput `json:"grants,omitempty" maxItems:"50" doc:"Replaces the grants; organizations an admin blocked stay blocked and cannot be listed"`
	}
}

type personalTokenUpdateOutput struct {
	Body struct {
		Token personalTokenDTO `json:"token"`
	}
}

// registerPersonalTokenRoutes wires the owner's personal tokens. Like the
// other account settings they belong to the person at the browser: every
// route needs an interactive session, so a token — personal or of an
// organization — and a locked session are refused, and no token mints or
// edits a token. Changes are audited on the owner's own feed; each grant
// change also on the affected organization's.
func registerPersonalTokenRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/auth/me/tokens", func(ctx context.Context, _ *struct{}) (*personalTokenListOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		toks, err := deps.PersonalTokens.List(ctx, p.UserID)
		if err != nil {
			return nil, mapPersonalTokenErr(ctx, err, nil)
		}
		out := &personalTokenListOutput{}
		out.Body.Tokens = make([]personalTokenDTO, len(toks))
		for i, t := range toks {
			out.Body.Tokens[i] = toPersonalTokenDTO(t)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/auth/me/tokens", func(ctx context.Context, in *personalTokenCreateInput) (*personalTokenCreateOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		grants, slugs, err := resolveGrantSlugs(ctx, deps, p.UserID, in.Body.Grants)
		if err != nil {
			return nil, err
		}
		plain, tok, err := deps.PersonalTokens.Create(ctx, p.UserID, personaltokens.NewToken{
			Name: in.Body.Name, Kind: auth.TokenKind(in.Body.Kind), Areas: in.Body.Areas,
			ExpiresAt: in.Body.ExpiresAt, Grants: grants,
		})
		if err != nil {
			return nil, mapPersonalTokenErr(ctx, err, slugs)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, tok.ID.String(), "created "+personalTokenLabel(tok)+" ("+grantList(tok.Grants)+areaSuffix(tok)+")")
		for _, g := range tok.Grants {
			recordOrgEvent(ctx, deps, g.OrgID, p.UserID, "addPersonalTokenGrant", "personal_token", tok.ID.String(),
				"added personal token "+tok.Name+" ("+string(g.Role)+")")
		}
		d := toPersonalTokenDTO(tok)
		return &personalTokenCreateOutput{Body: personalTokenCreatedDTO{
			Token: plain, ID: d.ID, Name: d.Name, Kind: d.Kind, Areas: d.Areas,
			ExpiresAt: d.ExpiresAt, LastUsedAt: d.LastUsedAt, CreatedAt: d.CreatedAt, Grants: d.Grants,
		}}, nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("createPersonalToken", "personal_token"))

	huma.Patch(hapi, "/api/v1/auth/me/tokens/{id}", func(ctx context.Context, in *personalTokenUpdateInput) (*personalTokenUpdateOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid token id")
		}
		if in.Body.ExpiresAt != nil && in.Body.NeverExpires {
			return nil, huma.Error422UnprocessableEntity("expires_at and never_expires exclude each other")
		}
		ch := personaltokens.Change{Name: in.Body.Name, Areas: in.Body.Areas}
		switch {
		case in.Body.ExpiresAt != nil:
			ch.Expiry, ch.ExpiresAt = auth.ExpirySet, in.Body.ExpiresAt
		case in.Body.NeverExpires:
			ch.Expiry = auth.ExpiryClear
		}
		var slugs map[uuid.UUID]string
		if in.Body.Grants != nil {
			grants, s, err := resolveGrantSlugs(ctx, deps, p.UserID, *in.Body.Grants)
			if err != nil {
				return nil, err
			}
			ch.Grants, slugs = &grants, s
		}
		before, after, changes, err := deps.PersonalTokens.Update(ctx, p.UserID, id, ch)
		if err != nil {
			return nil, mapPersonalTokenErr(ctx, err, slugs)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, id.String(), personalTokenChangeSummary(before, after, changes))
		recordGrantChanges(ctx, deps, p.UserID, after, changes)
		out := &personalTokenUpdateOutput{}
		out.Body.Token = toPersonalTokenDTO(after)
		return out, nil
	}, withAudit("updatePersonalToken", "personal_token"))

	huma.Delete(hapi, "/api/v1/auth/me/tokens/{id}", func(ctx context.Context, in *personalTokenIDInput) (*okOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid token id")
		}
		tok, err := deps.PersonalTokens.Revoke(ctx, p.UserID, id)
		if err != nil {
			return nil, mapPersonalTokenErr(ctx, err, nil)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, id.String(), "revoked "+personalTokenLabel(tok))
		// Every organization the token held a grant in loses it at once
		// (controller ruling P11), blocked grants included: their entry
		// leaves the organization's token page too.
		for _, g := range tok.Grants {
			recordOrgEvent(ctx, deps, g.OrgID, p.UserID, "removePersonalTokenGrant", "personal_token", id.String(),
				"removed personal token "+tok.Name+" ("+string(g.Role)+"): the token was revoked")
		}
		return &okOutput{}, nil
	}, withAudit("revokePersonalToken", "personal_token"))
}

// resolveGrantSlugs turns the request's organization slugs into ids
// through the caller's own memberships. An unknown slug and one the caller
// is not a member of are refused alike, so the answer reveals nothing about
// organizations the caller does not belong to. It also returns the slugs
// by id, for wording refusals.
func resolveGrantSlugs(ctx context.Context, deps Deps, userID uuid.UUID, in []personalGrantInput) ([]personaltokens.GrantInput, map[uuid.UUID]string, error) {
	orgs, err := deps.Tenancy.ListOrgsForUser(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "list memberships for personal token grants", "err", err)
		return nil, nil, huma.Error500InternalServerError("internal error")
	}
	bySlug := make(map[string]uuid.UUID, len(orgs))
	for _, o := range orgs {
		bySlug[o.Slug] = o.OrgID
	}
	grants := make([]personaltokens.GrantInput, len(in))
	slugs := make(map[uuid.UUID]string, len(in))
	for i, g := range in {
		id, ok := bySlug[g.Org]
		if !ok {
			return nil, nil, huma.Error422UnprocessableEntity("you are not a member of organization "+g.Org,
				&huma.ErrorDetail{Location: "body.grants", Message: g.Org})
		}
		grants[i] = personaltokens.GrantInput{OrgID: id, Role: tenancy.Role(g.Role)}
		slugs[id] = g.Org
	}
	return grants, slugs, nil
}

// recordGrantChanges writes one event per changed grant into the affected
// organization's feed (spec rule 12), naming the token as it is now.
func recordGrantChanges(ctx context.Context, deps Deps, userID uuid.UUID, after personaltokens.Token, changes []personaltokens.GrantChange) {
	for _, c := range changes {
		var action, summary string
		switch c.Kind {
		case personaltokens.GrantAdded:
			action, summary = "addPersonalTokenGrant", "added personal token "+after.Name+" ("+string(c.To)+")"
		case personaltokens.GrantChanged:
			action, summary = "changePersonalTokenGrant", "changed personal token "+after.Name+" ("+string(c.From)+" → "+string(c.To)+")"
		case personaltokens.GrantRemoved:
			action, summary = "removePersonalTokenGrant", "removed personal token "+after.Name+" ("+string(c.From)+")"
		default:
			continue
		}
		recordOrgEvent(ctx, deps, c.OrgID, userID, action, "personal_token", after.ID.String(), summary)
	}
}

func toPersonalTokenDTO(t personaltokens.Token) personalTokenDTO {
	out := personalTokenDTO{
		ID: t.ID.String(), Name: t.Name, Kind: string(t.Kind), Areas: t.Areas,
		ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, CreatedAt: t.CreatedAt,
		Grants: make([]personalGrantDTO, len(t.Grants)),
	}
	for i, g := range t.Grants {
		out.Grants[i] = personalGrantDTO{OrgID: g.OrgID.String(), OrgSlug: g.OrgSlug, OrgName: g.OrgName,
			Role: string(g.Role), State: string(g.State), LastUsedAt: g.LastUsedAt}
	}
	return out
}

// personalTokenLabel is "personal API token <name>" or "personal assistant
// token <name>".
func personalTokenLabel(t personaltokens.Token) string {
	if t.Kind == auth.TokenKindAssistant {
		return "personal assistant token " + t.Name
	}
	return "personal API token " + t.Name
}

// grantList words grants as "slug: role, slug: role".
func grantList(grants []personaltokens.Grant) string {
	parts := make([]string, len(grants))
	for i, g := range grants {
		parts[i] = g.OrgSlug + ": " + string(g.Role)
	}
	return strings.Join(parts, ", ")
}

func areaSuffix(t personaltokens.Token) string {
	if t.Kind != auth.TokenKindAssistant {
		return ""
	}
	return "; " + strings.Join(t.Areas, ", ")
}

// personalTokenChangeSummary words an update for the owner's feed: the old
// and the new value of everything that differs, grants by slug.
func personalTokenChangeSummary(before, after personaltokens.Token, changes []personaltokens.GrantChange) string {
	var parts []string
	if before.Name != after.Name {
		parts = append(parts, "name "+before.Name+" → "+after.Name)
	}
	if !sameAreaSet(before.Areas, after.Areas) {
		parts = append(parts, "areas "+strings.Join(before.Areas, ", ")+" → "+strings.Join(after.Areas, ", "))
	}
	if expiryText(before.ExpiresAt) != expiryText(after.ExpiresAt) {
		parts = append(parts, "expiry "+expiryText(before.ExpiresAt)+" → "+expiryText(after.ExpiresAt))
	}
	slug := map[uuid.UUID]string{}
	for _, g := range before.Grants {
		slug[g.OrgID] = g.OrgSlug
	}
	for _, g := range after.Grants {
		slug[g.OrgID] = g.OrgSlug
	}
	for _, c := range changes {
		switch c.Kind {
		case personaltokens.GrantAdded:
			parts = append(parts, "added "+slug[c.OrgID]+" ("+string(c.To)+")")
		case personaltokens.GrantChanged:
			parts = append(parts, slug[c.OrgID]+" "+string(c.From)+" → "+string(c.To))
		case personaltokens.GrantRemoved:
			parts = append(parts, "removed "+slug[c.OrgID]+" ("+string(c.From)+")")
		}
	}
	if len(parts) == 0 {
		parts = []string{"no change"}
	}
	return "changed " + personalTokenLabel(before) + ": " + strings.Join(parts, "; ")
}

// mapPersonalTokenErr answers a personal token service error. slugs names
// the organizations of the request's grants, for a refusal of one of them,
// which also carries the slug as an error detail at body.grants.
func mapPersonalTokenErr(ctx context.Context, err error, slugs map[uuid.UUID]string) error {
	org := ""
	var ge *personaltokens.GrantError
	if errors.As(err, &ge) {
		if org = slugs[ge.OrgID]; org == "" {
			org = ge.OrgID.String()
		}
	}
	var at []error
	if org != "" {
		at = []error{&huma.ErrorDetail{Location: "body.grants", Message: org}}
	}
	switch {
	case errors.Is(err, personaltokens.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, personaltokens.ErrLimit):
		return huma.Error409Conflict(personalTokenLimit)
	case errors.Is(err, personaltokens.ErrSwitchedOff):
		return huma.Error409Conflict(personalTokensOff, at...)
	case errors.Is(err, personaltokens.ErrBlocked):
		return huma.Error409Conflict(grantBlocked, at...)
	case errors.Is(err, personaltokens.ErrAboveRole):
		return huma.Error422UnprocessableEntity("the role cap is above your role in organization "+org, at...)
	case errors.Is(err, personaltokens.ErrNotMember):
		return huma.Error422UnprocessableEntity("you are not a member of organization "+org, at...)
	case errors.Is(err, personaltokens.ErrInvalid):
		if ge != nil {
			return huma.Error422UnprocessableEntity(ge.Err.Error()+": "+org, at...)
		}
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, auth.ErrTokenExpired):
		return huma.Error409Conflict(err.Error())
	}
	slog.ErrorContext(ctx, "personal tokens", "err", err)
	return huma.Error500InternalServerError("internal error")
}
