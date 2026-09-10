package api

import (
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
)

// auditAllowlist is every mutating (POST/PUT/PATCH/DELETE) operation,
// keyed "METHOD /path", that deliberately does not carry withAudit's
// Metadata["audit"] = true — each with a comment on why it genuinely must
// not record an audit.Event. TestEveryMutatingRouteIsAuditedOrAllowlisted
// (below) enumerates every mutating operation actually registered and
// fails the build for any that is neither audited nor listed here, so
// adding a new mutating route without an explicit decision either way is
// no longer possible.
//
// Task 11 fix round 1, Finding 2: the original design let a handler simply
// forget to opt in — no event, no log line, no signal. This allowlist is
// the safety net: silence is now only possible with a reviewed, committed
// reason.
//
// Every entry below is a Task 5-8 (authentication / own-account credential
// management) route, not a Task 9-11 (organization / instance) resource
// mutation. They fall into two groups:
//
//   - /auth/login, /auth/logout end or begin a session. There is no
//     organization or object being changed for an audit event to describe,
//     and — unlike a deliberate instance-admin action such as deactivating
//     a user — a login/logout happens on essentially every visit, so
//     recording it here would flood the org/instance activity trail this
//     task built with routine traffic no admin reading that feed is
//     looking for.
//   - /auth/totp/*, /auth/passkeys/* enrol, confirm, verify, disable,
//     register and delete a *second factor on the caller's own account*.
//     They carry no organization (these routes exist entirely outside the
//     org-scoped subtree), and predate this task by several tasks (5-8).
//     A credential-security audit trail is a real thing worth having, but
//     it is a different, self-scoped design question (does a step-up
//     verify belong in an org activity feed at all? what does "the org"
//     even mean for an account with memberships in several?) that neither
//     this task's brief nor the review that flagged Findings 1-3 raised.
//     Left here deliberately rather than wired under time pressure.
var auditAllowlist = map[string]string{
	"POST /api/v1/auth/login":  "authenticates a session; no organization or object to attribute an event to, and recording every login would flood the activity trail with routine traffic (see file-level comment above)",
	"POST /api/v1/auth/logout": "ends a session; same reasoning as login",

	"POST /api/v1/auth/totp/enrol":               "own-account MFA setup (Task 7), no organization; see file-level comment above",
	"POST /api/v1/auth/totp/confirm":             "own-account MFA setup (Task 7), no organization; see file-level comment above",
	"POST /api/v1/auth/totp/verify":              "own-account MFA step-up (Task 7), no organization; see file-level comment above",
	"DELETE /api/v1/auth/totp":                   "own-account MFA removal (Task 7), no organization; see file-level comment above",
	"POST /api/v1/auth/passkeys/register/begin":  "own-account passkey setup (Task 8), no organization; see file-level comment above",
	"POST /api/v1/auth/passkeys/register/finish": "own-account passkey setup (Task 8), no organization; see file-level comment above",
	"DELETE /api/v1/auth/passkeys/{id}":          "own-account passkey removal (Task 8), no organization; see file-level comment above",
	"POST /api/v1/auth/passkeys/login/begin":     "unauthenticated passkey login ceremony (Task 8), no organization or actor yet; see file-level comment above",
	"POST /api/v1/auth/passkeys/login/finish":    "unauthenticated passkey login ceremony (Task 8), no organization; see file-level comment above",
}

// registeredMutatingOps builds the full route table the same way api.New
// does (registerRoutes against a real humachi API) and returns every
// operation whose HTTP method can mutate state, so both tests below only
// ever look at routes that are actually registered in production, not a
// hand-maintained list that could drift from it. Deps{} (all nils) is
// enough: registerRoutes only registers closures over deps, it never calls
// a method on it at registration time.
func registeredMutatingOps(t *testing.T) map[string]*huma.Operation {
	t.Helper()
	hapi := humachi.New(chi.NewRouter(), huma.DefaultConfig("Pulsix API (coverage test)", "0.0.0"))
	registerRoutes(hapi, Deps{})

	mutatingMethods := map[string]func(*huma.PathItem) *huma.Operation{
		http.MethodPost:   func(p *huma.PathItem) *huma.Operation { return p.Post },
		http.MethodPut:    func(p *huma.PathItem) *huma.Operation { return p.Put },
		http.MethodPatch:  func(p *huma.PathItem) *huma.Operation { return p.Patch },
		http.MethodDelete: func(p *huma.PathItem) *huma.Operation { return p.Delete },
	}

	ops := map[string]*huma.Operation{}
	for path, item := range hapi.OpenAPI().Paths {
		for method, get := range mutatingMethods {
			if op := get(item); op != nil {
				ops[method+" "+path] = op
			}
		}
	}
	return ops
}

// TestEveryMutatingRouteIsAuditedOrAllowlisted is Finding 2's safety net: it
// enumerates every registered POST/PUT/PATCH/DELETE operation and requires
// each one to either carry withAudit's Metadata["audit"] = true or appear
// in auditAllowlist with a reason. A future task that adds a mutating route
// and forgets to call withAudit now fails this test — loudly, at build
// time — instead of silently recording nothing.
func TestEveryMutatingRouteIsAuditedOrAllowlisted(t *testing.T) {
	for key, op := range registeredMutatingOps(t) {
		mutating, _ := op.Metadata["audit"].(bool)
		if mutating {
			continue
		}
		if _, ok := auditAllowlist[key]; ok {
			continue
		}
		t.Errorf("%s is a mutating route that neither records an audit event "+
			"(withAudit(...) on its registration) nor is on auditAllowlist "+
			"(audit_coverage_test.go) with a reason it must not — one or the "+
			"other is required for every mutating route", key)
	}
}

// TestAuditAllowlistHasNoStaleEntries is the other half of Finding 2's
// safety net: an allowlist entry for a route that was removed, renamed, or
// switched to withAudit itself would silently stop meaning anything, and a
// stale entry could quietly cover for a genuinely new, unaudited route that
// happens to reuse an old path. Every entry must match a currently
// registered, still-unaudited mutating route.
func TestAuditAllowlistHasNoStaleEntries(t *testing.T) {
	ops := registeredMutatingOps(t)
	for key := range auditAllowlist {
		op, ok := ops[key]
		if !ok {
			t.Errorf("auditAllowlist entry %q does not match any registered mutating route anymore; remove it", key)
			continue
		}
		if mutating, _ := op.Metadata["audit"].(bool); mutating {
			t.Errorf("auditAllowlist entry %q is now audited (withAudit is set); remove it from the allowlist", key)
		}
	}
}
