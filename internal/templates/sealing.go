// Sealing and masking of a template's secret extra vars: what a reader
// gets back (masked), and what a write stores (sealed). Split out of
// service.go under backlog 2.68 (T3) — sealing_test.go already covered
// exactly this surface, so the seam was one the tests had drawn already.
package templates

import (
	"fmt"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// masked returns t with its extra vars masked for a reader: every sealed
// value, and every non-empty value under a secret-typed field of the
// template's current schema, replaced by the {"$secret": "set"} marker
// (backlog 2.21's direction — a string→secret schema change must not
// surface the old plaintext on reads). Values whose shape no longer fits
// their field are copied unchanged; validation reports those at write time.
func masked(t Template) Template {
	t.ExtraVars = maskExtraVars(t.ExtraVarsSchema, t.ExtraVars)
	return t
}

// maskExtraVars delegates to schema.MaskDeclared, the shared
// fields-aware masking backlog 2.21 introduced (also used by inventory
// group and membership reads): with no schema, fields is empty and
// MaskDeclared's declared-field pass has nothing to declare, so it
// degrades to exactly MaskVars's sealed-only behavior — the same fallback
// this function used to implement by hand.
func maskExtraVars(sc *schema.Schema, vars map[string]any) map[string]any {
	var fields []schema.Field
	if sc != nil {
		fields = sc.Fields
	}
	return schema.MaskDeclared(fields, vars)
}

// sealExtraVars is sealSecrets plus strict validation: what it adds is
// the schema.Validate pass over the result, sealed values included.
//
// It carries no second account of the sealing rules on purpose (backlog
// 2.68, T3). It used to, and the copy had already drifted — it still
// described backlog 2.47's legacy plaintext but not 2.52's empty-string
// and wrong-type cases, so the two comments disagreed about when a marker
// is a validation problem. sealSecrets is where those rules live.
func (s *Service) sealExtraVars(aad []byte, fields []schema.Field, incoming, stored map[string]any) (map[string]any, error) {
	out, err := s.sealSecrets(aad, fields, incoming, stored)
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(fields, out); err != nil {
		return nil, err
	}
	return out, nil
}

// sealSecrets prepares incoming extra vars for storage under fields: a
// plaintext value of a secret-typed field is sealed; the masked marker a
// read returned, or a keep marker, keeps the stored value at that path.
// What a marker keeps is a sealed envelope, or the legacy plaintext a
// template stored before sealing shipped (backlog 2.47, 2.52): the marker
// resolves to the stored string, which is sealed fresh with the row's AAD —
// the stored row keeps the plaintext until this save replaces it with the
// sealed envelope, so a legacy template saves cleanly and its secret is
// sealed from that save on. A marker without a stored value at all, one
// over an empty string (no secret stored — the same "nothing to hide" rule
// the read path applies), or one whose stored value is neither a string nor
// a sealed envelope (e.g. the number a string→secret schema change leaves
// behind) is a validation problem. A sealed envelope sent in directly is
// refused. It does not validate the result against the schema — callers
// that need strict validation run schema.Validate themselves.
func (s *Service) sealSecrets(aad []byte, fields []schema.Field, incoming, stored map[string]any) (map[string]any, error) {
	var problems []schema.Problem
	out, err := schema.MapSecrets(fields, incoming, func(path string, v any) (any, error) {
		from, keep := schema.KeepFrom(v)
		if !keep && isMaskMarker(v) {
			from, keep = path, true
		}
		if keep {
			prev, found := schema.Lookup(stored, from)
			if plain, isStr := prev.(string); isStr && plain != "" {
				// Legacy plaintext (backlog 2.47): seal it now with this
				// row's AAD, the same sealing a freshly entered plaintext
				// goes through.
				ct, err := s.box.SealAAD([]byte(plain), aad)
				if err != nil {
					return nil, fmt.Errorf("seal extra var %s: %w", path, err)
				}
				return schema.Sealed(ct), nil
			}
			if _, sealed := schema.SealedBytes(prev); !found || !sealed {
				problems = append(problems, schema.Problem{Path: path, Message: "the stored secret this refers to no longer exists; enter it again"})
				return nil, nil
			}
			return prev, nil
		}
		if _, ok := schema.SealedBytes(v); ok {
			problems = append(problems, schema.Problem{Path: path, Message: "sealed values cannot be written directly"})
			return nil, nil
		}
		if plain, ok := v.(string); ok && plain != "" {
			ct, err := s.box.SealAAD([]byte(plain), aad)
			if err != nil {
				return nil, fmt.Errorf("seal extra var %s: %w", path, err)
			}
			return schema.Sealed(ct), nil
		}
		return v, nil
	})
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, &schema.ValidationError{Problems: problems}
	}
	return out, nil
}

// isMaskMarker reports whether v is the {"$secret": "set"} marker a masked
// read returns.
func isMaskMarker(v any) bool {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return false
	}
	return equalMarker(m, schema.Mask())
}

func equalMarker(a, b map[string]any) bool {
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return len(a) == len(b)
}
