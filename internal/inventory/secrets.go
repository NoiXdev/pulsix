package inventory

import (
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// GroupAAD is the additional data group-scoped secrets are sealed with.
func GroupAAD(orgID, groupID uuid.UUID) []byte {
	return []byte("inventory-group:" + orgID.String() + ":" + groupID.String())
}

// MemberAAD is the additional data membership secrets are sealed with.
func MemberAAD(orgID, groupID, hostID uuid.UUID) []byte {
	return []byte("inventory-member:" + orgID.String() + ":" + groupID.String() + ":" + hostID.String())
}

// prepareVars turns a write into what is stored: plaintext secrets sealed
// with aad, keep markers replaced by the sealed value they point at in
// stored, and a client-supplied sealed value refused. It then validates the
// result strictly against fields. Every problem comes back as one
// *schema.ValidationError.
func (s *Service) prepareVars(aad []byte, fields []schema.Field, incoming, stored map[string]any) (map[string]any, error) {
	var problems []schema.Problem
	out, err := schema.MapSecrets(fields, incoming, func(path string, v any) (any, error) {
		if from, ok := schema.KeepFrom(v); ok {
			prev, found := schema.Lookup(stored, from)
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
				return nil, err
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
	if err := schema.Validate(fields, out); err != nil {
		return nil, err
	}
	return out, nil
}
