package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/shared"
)

// TestMapReposErrNeverNamesAnOrganization pins the structural half of the
// guarantee internal/api/shared_handlers_test.go's
// TestOrgScopedRepoRoutesNeverLeakAnotherOrgsIdentity exercises end to end
// over real HTTP: mapReposErr — the error mapper every organization-scoped
// repository route goes through — must have NO case at all for
// shared.Service's richer errors, not even a careful, slug-resolving one,
// because mapReposErr is reachable by a member of any organization, unlike
// mapSharedErr (shared_handlers.go), which only an instance-admin-gated
// caller can reach.
//
// This is a direct unit call rather than another HTTP round trip, because
// today internal/repos (service.go, store_sql.go) imports nothing from
// internal/shared — verified by reading both files — so no real request can
// carry a shared.* error through mapReposErr to drive this red through the
// full stack. What CAN be driven red is the mapping function itself, with a
// hand-built error: the brief's "write the test first and watch it fail
// against a deliberately naive mapping" was done against this exact test,
// with mapReposErr temporarily given the case a future, well-intentioned
// change might add —
//
//	case errors.Is(err, shared.ErrNameTaken), errors.Is(err, shared.ErrInUse):
//		return huma.Error409Conflict(err.Error())
//
// — which looks harmless (shared.ErrNameTaken's own Error() carries no
// organization) but is exactly how this leaks for *shared.InUseError: its
// Error() method (internal/shared/definition.go) embeds the raw OrgID
// ("organization %s: %s still reference..."). With that case added, this
// test failed with
//
//	mapReposErr(...InUseError...) message "organization <uuid>: a job
//	trigger still reference this definition's projection" names
//	organization <uuid> — mapReposErr must never be able to do this
//
// confirming the naive case leaks an organization identifier through a
// route any organization's member can reach. Removing the case again (the
// real, current shape of mapReposErr — repo_handlers.go) makes this test
// pass: mapReposErr falls through its switch to the generic 500 path for
// both of shared.Service's richer error types, which is the only shape that
// closes this off for good rather than relying on nobody adding the case.
func TestMapReposErrNeverNamesAnOrganization(t *testing.T) {
	orgB := uuid.Must(uuid.NewV7())
	cases := []struct {
		name string
		err  error
	}{
		{"NameCollisionError", &shared.NameCollisionError{Name: "ansible-base", Orgs: []uuid.UUID{orgB}}},
		{"InUseError", &shared.InUseError{OrgID: orgB, Reasons: []string{"a job trigger"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapReposErr(context.Background(), tc.err)
			var se huma.StatusError
			if !errors.As(got, &se) {
				t.Fatalf("mapReposErr(%v) = %v, want a huma.StatusError", tc.err, got)
			}
			if se.GetStatus() != http.StatusInternalServerError {
				t.Errorf("mapReposErr(%v) status = %d, want %d (mapReposErr must not recognise a shared.* error at all)",
					tc.err, se.GetStatus(), http.StatusInternalServerError)
			}
			if msg := se.Error(); strings.Contains(msg, orgB.String()) {
				t.Errorf("mapReposErr(%v) message %q names organization %s — reachable by any organization's member",
					tc.err, msg, orgB)
			}
		})
	}
}
