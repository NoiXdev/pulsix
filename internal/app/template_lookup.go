// The job-template lookup adapter: the templates and playbooks SQL stores
// behind jobs.TemplateSource, the port the job queue's enqueue and claim
// paths read through. It replaces Task 5's SQLTemplates (whose private join
// query duplicated what the templates store now owns) — the conversion from
// the two stores' rows into TemplateRow lives here, in the wiring, because
// internal/jobs must not import internal/templates and vice versa.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/templates"
)

// templateLookup adapts the raw stores to jobs.TemplateSource. It reads the
// stores — not templates.Service — on purpose: the service masks extra-var
// secrets on its API reads, while the claim payload the queue builds needs
// them in the clear, so the lookup opens the sealed values with the box
// (the same split credentials.Service draws between its metadata reads and
// OpenSecret).
type templateLookup struct {
	templates *templates.SQLStore
	playbooks *playbooks.SQLStore
	box       templates.Sealer
}

// NewTemplateLookup builds the adapter both wirings share (app.Build and
// apitest.New, which mirrors it).
func NewTemplateLookup(t *templates.SQLStore, p *playbooks.SQLStore, box templates.Sealer) jobs.TemplateSource {
	return templateLookup{templates: t, playbooks: p, box: box}
}

// TemplateLookup implements jobs.TemplateSource: the template row joined
// with its playbook, so the queue can build the worker payload (inline YAML
// or "repo:<repo-id>:<path>"). An unknown template or playbook — including
// one belonging to another organization — is jobs.ErrNotFound, exactly what
// the pre-adapter SQL join answered.
func (a templateLookup) TemplateLookup(ctx context.Context, orgID, id uuid.UUID) (jobs.TemplateRow, error) {
	t, err := a.templates.Get(ctx, orgID, id)
	if err != nil {
		return jobs.TemplateRow{}, notFound(err)
	}
	p, err := a.playbooks.Get(ctx, orgID, t.PlaybookID)
	if err != nil {
		return jobs.TemplateRow{}, notFound(err)
	}
	scope, err := json.Marshal(t.Scope)
	if err != nil {
		return jobs.TemplateRow{}, fmt.Errorf("encode template scope: %w", err)
	}
	opened, err := a.openExtraVars(t)
	if err != nil {
		return jobs.TemplateRow{}, err
	}
	vars, err := json.Marshal(opened)
	if err != nil {
		return jobs.TemplateRow{}, fmt.Errorf("encode template extra vars: %w", err)
	}
	var schemaRaw json.RawMessage
	if t.ExtraVarsSchema != nil {
		b, err := json.Marshal(*t.ExtraVarsSchema)
		if err != nil {
			return jobs.TemplateRow{}, fmt.Errorf("encode template extra vars schema: %w", err)
		}
		schemaRaw = b
	}
	// The template's own vault identities (spec decision 1), read from the
	// store directly like the rest of this adapter — not templates.Service,
	// which this type deliberately bypasses (see its own doc comment) — so
	// the claim can resolve them against the repository's (VaultResolver,
	// spec decision 2).
	vaultCreds, err := a.templates.ListVaultCredentials(ctx, orgID, id)
	if err != nil {
		return jobs.TemplateRow{}, fmt.Errorf("list template vault credentials: %w", err)
	}
	vaultIdentities := make([]jobs.VaultIdentity, len(vaultCreds))
	for i, vc := range vaultCreds {
		v := vc.ToVaultIdentity()
		vaultIdentities[i] = jobs.VaultIdentity{CredentialID: v.CredentialID, Label: v.Label, FromTemplate: v.FromTemplate}
	}
	return jobs.TemplateRow{
		ID: t.ID, OrgID: t.OrgID, PlaybookID: t.PlaybookID,
		PlaybookRepo: p.RepoID, PlaybookPath: playbookText(p.Path), PlaybookInline: playbookText(p.Inline),
		Scope: scope, Limit: t.Limit,
		ExtraVars: vars, ExtraVarsSchema: schemaRaw, CredentialID: t.CredentialID,
		VaultIdentities: vaultIdentities,
	}, nil
}

// openExtraVars reveals the template's sealed extra vars (secret-typed
// values are sealed at rest under templates.ExtraVarsAAD) for the claim
// payload — the one place they travel in the clear, inside the mTLS claim.
// A value that does not open under this template's AAD fails the lookup,
// and with it the claim, closed.
func (a templateLookup) openExtraVars(t templates.Template) (map[string]any, error) {
	aad := templates.ExtraVarsAAD(t.OrgID, t.ID)
	var open func(v any) (any, error)
	open = func(v any) (any, error) {
		if ct, ok := schema.SealedBytes(v); ok {
			plain, err := a.box.OpenAAD(ct, aad)
			if err != nil {
				return nil, fmt.Errorf("open template extra var: %w", err)
			}
			return string(plain), nil
		}
		switch x := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, e := range x {
				o, err := open(e)
				if err != nil {
					return nil, err
				}
				out[k] = o
			}
			return out, nil
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				o, err := open(e)
				if err != nil {
					return nil, err
				}
				out[i] = o
			}
			return out, nil
		default:
			return v, nil
		}
	}
	out, err := open(t.ExtraVars)
	if err != nil {
		return nil, err
	}
	m, _ := out.(map[string]any)
	return m, nil
}

// playbookText dereferences an optional playbook column; the playbooks CHECK
// constraint guarantees the field matching the source is set, so nil only
// means "the other source".
func playbookText(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// notFound collapses the two stores' not-found sentinels into the queue's.
func notFound(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, templates.ErrNotFound) || errors.Is(err, playbooks.ErrNotFound) {
		return fmt.Errorf("%w: %s", jobs.ErrNotFound, err)
	}
	return err
}
