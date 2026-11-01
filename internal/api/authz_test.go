package api_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/auth"
)

// This file is Finding 4's fix (Task 10 fix round 1): there was no
// internal/api/authz_test.go at all, and none of the three pre-existing
// bearer tests (token_bearer_test.go) exercised requireOrg's own
// tenancy.Lower call — deleting that line from authz.go would have left
// the whole suite green. That gap is exactly why Finding 1 (the role-cap
// bypass in ChangeRole/RemoveMember) went unnoticed at review time: the
// mechanism that computes the capped role was itself untested end to end.

// TestBearerTokenRoleCapRejectsInsufficientRole proves requireOrg's
// tenancy.Lower call actually narrows what a bearer token can do: an org
// owner's viewer-scoped token must get 403 on a route whose floor
// (PermMembersManage, admin) sits above viewer, even though the same
// owner's cookie session would pass it easily.
func TestBearerTokenRoleCapRejectsInsufficientRole(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("authz-cap-owner@example.test", "correct horse battery")
	org := s.SeedOrg("authz-cap-org", owner)

	plain, _, err := s.Deps.Tokens.Create(context.Background(), owner, org, "ci", "viewer", nil)
	if err != nil {
		t.Fatalf("Create token: %v", err)
	}

	resp := bearerRequest(t, s, http.MethodGet, "/api/v1/orgs/authz-cap-org/members", plain)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer-scoped token on a PermMembersManage route: status %d, want 403, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

// fixedRoleTokenStore is a minimal auth.TokenStore fake that resolves any
// bearer credential to a single, mutable Token. It exists only for
// TestBearerTokenUnknownRoleIsForbidden below: org_memberships.role and
// api_tokens.role are both the same Postgres enum with exactly four
// labels, so no real INSERT through the actual database — not even a
// direct one bypassing sqlc — can ever produce a token whose role isn't
// one of "owner"/"admin"/"operator"/"viewer". Swapping in this fake for
// just the bearer-resolution step is what lets the test drive an
// unrecognized role through the *real* requireOrg/tenancy.Lower/
// tenancy.Allows wiring over HTTP, rather than only asserting the same
// property in isolation the way roles_test.go's TestRoleMatrix already
// does for Allows(Role("bogus"), ...).
type fixedRoleTokenStore struct{ tok *auth.Token }

func (f *fixedRoleTokenStore) Insert(context.Context, auth.Token, []byte) error { return nil }

func (f *fixedRoleTokenStore) GetByHash(context.Context, []byte, time.Time) (auth.Token, bool, error) {
	if f.tok == nil {
		return auth.Token{}, false, auth.ErrNotFound
	}
	return *f.tok, false, nil
}

func (f *fixedRoleTokenStore) Usable(context.Context, uuid.UUID, time.Time) (bool, error) {
	return f.tok != nil, nil
}

func (f *fixedRoleTokenStore) ListForOrg(context.Context, uuid.UUID) ([]auth.Token, error) {
	return nil, nil
}
func (f *fixedRoleTokenStore) Revoke(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (f *fixedRoleTokenStore) TouchLastUsed(context.Context, uuid.UUID, time.Time) error {
	return nil
}

// TestBearerTokenUnknownRoleIsForbidden proves tenancy.Lower and
// tenancy.Allows fail closed for a token whose role is not one of the four
// known roles, or is empty — rank has no entry for either, so it ranks
// below even viewer (see roles.go's Lower/Allows doc comments) and must
// never be treated as more permissive than "no access".
func TestBearerTokenUnknownRoleIsForbidden(t *testing.T) {
	for _, role := range []string{"superuser", ""} {
		t.Run(fmt.Sprintf("role=%q", role), func(t *testing.T) {
			fake := &fixedRoleTokenStore{}
			s := apitest.New(t, func(s *apitest.Server) {
				s.Deps.Tokens = auth.NewTokens(fake, time.Now)
			})
			owner := s.SeedUser("authz-unknown-role-owner@example.test", "correct horse battery")
			org := s.SeedOrg("authz-unknown-role-org", owner)
			fake.tok = &auth.Token{ID: uuid.Must(uuid.NewV7()), UserID: owner, OrgID: org, Name: "ci", Role: role}

			// A well-formed credential (correct "pxt_" prefix and valid
			// base64) is required to reach GetByHash at all — its actual
			// bytes don't matter, since the fake ignores the hash and
			// always returns fake.tok.
			cred := "pxt_" + base64.RawURLEncoding.EncodeToString([]byte("does-not-matter"))

			// GET /orgs/{org} only requires PermHostsRead (the lowest
			// floor, viewer) — even that must be refused for an
			// unrecognized role, proving the cap fails closed rather than
			// merely "closed enough to stop admin-level routes".
			resp := bearerRequest(t, s, http.MethodGet, "/api/v1/orgs/authz-unknown-role-org", cred)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("token role=%q on the lowest-floor route: status %d, want 403, body %s", role, resp.StatusCode, mustReadBody(t, resp))
			}
		})
	}
}
