// Per-run overrides of a template's limit and top-level extra vars: the
// one validation path schedules (stored, secrets sealed) and webhook
// triggers (mapped from a payload, secrets refused) share, so an override
// passes exactly the checks a template save applies (spec 2026-09-18 §2,
// §3): the limit rules, the connection-security denylist (2.36) and — with
// a schema — strict validation of the merged vars.
package templates

import (
	"context"
	"fmt"
	"maps"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// Overrides replaces a template's limit and top-level extra vars for one
// run. A nil Limit keeps the template's; an extra var replaces the
// template's value of the same top-level name.
type Overrides struct {
	Limit     *string        `json:"limit,omitempty"`
	ExtraVars map[string]any `json:"extra_vars,omitempty"`
}

// Empty reports whether the overrides change nothing.
func (o Overrides) Empty() bool { return o.Limit == nil && len(o.ExtraVars) == 0 }

// OverrideOptions selects the treatment of secret-typed values.
type OverrideOptions struct {
	// SealAAD, when set, seals plaintext secret values under this AAD —
	// the storage form a schedule keeps.
	SealAAD []byte
	// Stored are the previously stored override vars; keep and mask
	// markers resolve against them (never against the template).
	Stored map[string]any
	// RefuseSecrets makes any secret-typed value a validation problem
	// (webhook payload mapping).
	RefuseSecrets bool
}

// ValidateOverrides checks in against the template and returns it in the
// form the caller stores or enqueues: secrets sealed (SealAAD), refused
// (RefuseSecrets) or passed through as plaintext (neither — the reveal
// path, whose result goes straight into a sealed job payload).
func (s *Service) ValidateOverrides(ctx context.Context, orgID, templateID uuid.UUID, in Overrides, opt OverrideOptions) (Overrides, error) {
	t, err := s.store.Get(ctx, orgID, templateID)
	if err != nil {
		return Overrides{}, err
	}
	out := Overrides{Limit: in.Limit}
	if in.Limit != nil {
		if err := validateLimit(*in.Limit); err != nil {
			return Overrides{}, err
		}
	}
	if len(in.ExtraVars) == 0 {
		return out, nil
	}
	if names := ansiblevars.ForbiddenIn(in.ExtraVars); len(names) > 0 {
		return Overrides{}, forbiddenProblems(names)
	}
	vars := maps.Clone(in.ExtraVars)
	if t.ExtraVarsSchema == nil {
		out.ExtraVars = vars
		return out, nil
	}
	fields := t.ExtraVarsSchema.Fields
	switch {
	case opt.RefuseSecrets:
		if problems := secretProblems(fields, vars); len(problems) > 0 {
			return Overrides{}, &schema.ValidationError{Problems: problems}
		}
	case opt.SealAAD != nil:
		if vars, err = s.sealSecrets(opt.SealAAD, fields, vars, opt.Stored); err != nil {
			return Overrides{}, err
		}
	}
	merged := maps.Clone(t.ExtraVars)
	if merged == nil {
		merged = map[string]any{}
	}
	maps.Copy(merged, vars)
	if err := schema.Validate(fields, merged); err != nil {
		return Overrides{}, err
	}
	out.ExtraVars = vars
	return out, nil
}

// RevealOverrides opens stored overrides (sealed under aad) and validates
// the plaintext against the template as it is now. The result carries
// plaintext secrets and must only ever travel into a sealed job payload.
func (s *Service) RevealOverrides(ctx context.Context, orgID, templateID uuid.UUID, stored Overrides, aad []byte) (Overrides, error) {
	t, err := s.store.Get(ctx, orgID, templateID)
	if err != nil {
		return Overrides{}, err
	}
	vars := stored.ExtraVars
	if t.ExtraVarsSchema != nil && len(vars) > 0 {
		vars, err = schema.MapSecrets(t.ExtraVarsSchema.Fields, vars, func(path string, v any) (any, error) {
			ct, ok := schema.SealedBytes(v)
			if !ok {
				return v, nil
			}
			plain, err := s.box.OpenAAD(ct, aad)
			if err != nil {
				return nil, fmt.Errorf("open override %s: %w", path, err)
			}
			return string(plain), nil
		})
		if err != nil {
			return Overrides{}, err
		}
	}
	return s.ValidateOverrides(ctx, orgID, templateID, Overrides{Limit: stored.Limit, ExtraVars: vars}, OverrideOptions{})
}

// SecretFields returns the top-level field names that are, or contain, a
// secret: webhook mappings may not target them (spec §3).
func SecretFields(sc *schema.Schema) map[string]bool {
	out := map[string]bool{}
	if sc == nil {
		return out
	}
	for _, f := range sc.Fields {
		if containsSecret(f) {
			out[f.Name] = true
		}
	}
	return out
}

func containsSecret(f schema.Field) bool {
	if f.Type == schema.TypeSecret {
		return true
	}
	if f.Item != nil && containsSecret(*f.Item) {
		return true
	}
	for _, sub := range f.Fields {
		if containsSecret(sub) {
			return true
		}
	}
	return false
}

func secretProblems(fields []schema.Field, vars map[string]any) []schema.Problem {
	var problems []schema.Problem
	_, _ = schema.MapSecrets(fields, vars, func(path string, v any) (any, error) {
		problems = append(problems, schema.Problem{Path: path, Message: "secret fields cannot be set from a webhook payload"})
		return v, nil
	})
	return problems
}
