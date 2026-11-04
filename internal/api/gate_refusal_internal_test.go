package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

// teapotError is a huma.StatusError that is not a *huma.ErrorModel.
type teapotError struct{}

func (teapotError) Error() string  { return "short and stout" }
func (teapotError) GetStatus() int { return http.StatusTeapot }

// TestWriteGateRefusal: a gate's refusal keeps its status, and its detail
// when it has one, in the problem shape; only an error that carries no
// status at all answers 401.
func TestWriteGateRefusal(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantDetail string
	}{
		{"a huma problem", huma.Error403Forbidden(stepUpRequired), http.StatusForbidden, stepUpRequired},
		{"another status error", teapotError{}, http.StatusTeapot, "short and stout"},
		{"no status at all", errors.New("boom"), http.StatusUnauthorized, "login required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeGateRefusal(rec, tc.err)
			var problem huma.ErrorModel
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.wantStatus || problem.Status != tc.wantStatus || problem.Detail != tc.wantDetail {
				t.Fatalf("status %d, body %+v; want %d %q", rec.Code, problem, tc.wantStatus, tc.wantDetail)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("Content-Type %q", ct)
			}
		})
	}
}
