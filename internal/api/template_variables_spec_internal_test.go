package api

import (
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
)

// TestReviewNotSentIsNeverNullInTheSpec pins what the generated client
// types rely on: review.not_sent is a list the mapper always sends (toReviewDTO
// turns nil into []), so the OpenAPI document must not offer null for it.
func TestReviewNotSentIsNeverNullInTheSpec(t *testing.T) {
	hapi := humachi.New(chi.NewRouter(), huma.DefaultConfig("Pulsix API (spec test)", "0.0.0"))
	registerRoutes(hapi, Deps{})

	review := hapi.OpenAPI().Components.Schemas.Map()["TemplateVariablesReviewDTO"]
	if review == nil {
		t.Fatal("TemplateVariablesReviewDTO is not in the spec")
	}
	notSent := review.Properties["not_sent"]
	if notSent == nil {
		t.Fatal("not_sent is not a property of TemplateVariablesReviewDTO")
	}
	if notSent.Type != "array" {
		t.Fatalf("not_sent type = %q, want array", notSent.Type)
	}
	if notSent.Nullable {
		t.Fatal("not_sent is nullable in the spec, want a list that is never null")
	}
}
