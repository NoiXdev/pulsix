package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// schemaProblems turns schema problems into a 422 whose details are
// located under location (body.extra_vars for overrides), one per problem.
func schemaProblems(ve *schema.ValidationError, location string) error {
	details := make([]error, len(ve.Problems))
	for i, p := range ve.Problems {
		loc := location
		if p.Path != "" {
			loc += "." + p.Path
		}
		details[i] = &huma.ErrorDetail{Location: loc, Message: p.Message}
	}
	return huma.Error422UnprocessableEntity("validation failed", details...)
}
