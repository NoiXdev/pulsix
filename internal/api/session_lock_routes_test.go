package api_test

import (
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// lockedAllowed is every operation a locked cookie session may still reach:
// who am I (the SPA learns it is locked), sign out, and unlock.
var lockedAllowed = map[string]bool{
	"GET /api/v1/auth/me":           true,
	"POST /api/v1/auth/logout":      true,
	"POST /api/v1/auth/lock/unlock": true,
}

// TestLockedSessionRouteWalk calls every registered operation with a locked,
// MFA-complete cookie session of an instance admin who owns the
// organization in the path. Every operation must refuse it with 403
// session_locked, except the allow-list above and the routes that take no
// session at all (skippedForMustChange: sign-in, setup, the public
// password routes, webhooks, health). Accepting an invite is skipped too:
// its token is the credential, an unknown one answers 404 first, and the
// lock refusal for a real one is TestLockedSessionCannotAcceptInvite's.
// The request is well-formed for each
// operation (see schemaSampler), so a refusal comes from the gate and not
// from input validation. The 9 sample routes of
// TestLockedSessionIsRefusedEverywhere stay as the readable short list;
// this walk catches any route added later that forgets the lock.
func TestLockedSessionRouteWalk(t *testing.T) {
	s := apitest.New(t)
	uid := lockAdmin(t, s, "lockwalk@example.test")
	s.SeedOrg("lockwalk", uid)
	c, _ := lockedSession(t, s, uid)

	ops, gen := walkOperations(t)
	seenAllowed := map[string]bool{}
	for _, o := range ops {
		key := o.method + " " + o.path
		if skippedForMustChange(o.path) {
			continue
		}
		target, body := gen.request(o, "lockwalk")
		client := c
		if lockedAllowed[key] {
			// A fresh locked session each, so signing out (or a wrong PIN)
			// cannot spoil the walk.
			client, _ = lockedSession(t, s, uid)
			seenAllowed[key] = true
		}
		resp := requestJSON(t, client, o.method, s.HTTP.URL+target, body)
		detail := problemDetail(t, resp)
		if lockedAllowed[key] {
			if resp.StatusCode == http.StatusForbidden && detail == sessionLocked {
				t.Errorf("%s: an allowed route refused the locked session", key)
			}
			continue
		}
		if resp.StatusCode != http.StatusForbidden || detail != sessionLocked {
			t.Errorf("%s (%s): status %d detail %q, want 403 %q", key, target, resp.StatusCode, detail, sessionLocked)
		}
	}
	for key := range lockedAllowed {
		if !seenAllowed[key] {
			t.Errorf("allow-list entry %s is not a registered operation", key)
		}
	}

	// The routes outside the OpenAPI document that take a session: the
	// backup download, the API explorer, its spec and the per-type schemas.
	for _, path := range []string{
		"/api/v1/admin/backups/pulsix-20261104-120000.dump/download",
		"/api/explorer",
		"/api/openapi.json",
		"/api/schemas/HostDTO.json",
	} {
		resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+path, nil)
		if detail := problemDetail(t, resp); resp.StatusCode != http.StatusForbidden || detail != sessionLocked {
			t.Errorf("GET %s: status %d detail %q, want 403 %q", path, resp.StatusCode, detail, sessionLocked)
		}
	}
}
