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
// Residual gap: entries are keyed by method + path, which is the only
// stable handle the router exposes at this enumeration point (see
// registeredMutatingOps below — it walks huma's own OpenAPI path table,
// not anything more specific to a particular handler function). If a route
// were ever removed and a *different* route later registered at the same
// method and path, the stale allowlist entry would silently cover the new
// route too, and TestAuditAllowlistHasNoStaleEntries cannot catch that
// case — from its point of view the key still resolves to a real,
// unaudited operation, so nothing looks wrong. This has not happened and
// is not expected to (paths are not reused casually in this codebase), but
// it is a real, understood limitation of a method+path-keyed list, not an
// airtight guarantee — a reviewer adding a route at a path that used to
// mean something else should search this file explicitly rather than
// trust the test suite to catch it.
//
// Task 11 fix round 1 allowlisted all eleven Task 5-8 authentication /
// own-account credential routes as one group, reasoning that none of them
// have an organization to scope an event to. Task 11 fix round 2's
// re-review judged that grouping too coarse: "no organization" is true of
// all eleven, but that alone doesn't mean none of them belong in the
// account-security trail GET /me/activity exists to surface — four of
// them are real account-security state changes an owner needs to see
// after a compromise, and now record instance-level events (org_id = nil,
// the same mechanism POST /admin/users/{id}/deactivate uses) instead of
// being allowlisted: POST /auth/totp/confirm, DELETE /auth/totp, POST
// /auth/passkeys/register/finish, DELETE /auth/passkeys/{id} — see
// mfa_handlers.go and passkey_handlers.go for their withAudit calls. The
// remaining seven below stay allowlisted, each for a reason specific to
// that route, not the "no organization" blanket reason alone.
var auditAllowlist = map[string]string{
	// Routine, high-volume authentication traffic: neither carries an
	// organization or a mutated object worth recording, and every session
	// currently signed in is already visible in the session list — a
	// GET /me/sessions-style view, not this activity trail. Recording
	// every login/logout/step-up here would flood the account-security
	// trail this task cares about with noise no reader is looking for.
	"POST /api/v1/auth/login":       "routine, high-volume session start; no organization or object, and the session list already covers who is signed in",
	"POST /api/v1/auth/logout":      "routine, high-volume session end; no organization or object, and the session list already covers who is signed in",
	"POST /api/v1/auth/totp/verify": "routine, high-volume session step-up (a second-factor check on an already-existing session, not a change to the factor itself); no organization or object, and the session list already covers who is signed in",

	// Ceremony *begin* steps commit no credential: BeginEnrol/BeginRegistration
	// only hand the browser a challenge and stash server-side ceremony
	// state. Nothing has actually changed about the account's second
	// factors until the matching *finish* step succeeds — and that finish
	// step is exactly what now records the event (POST /auth/totp/confirm,
	// POST /auth/passkeys/register/finish). Recording the begin step too
	// would produce a misleading "started but maybe never happened" entry
	// for every abandoned ceremony (browser closed, user declined the
	// platform prompt, etc.).
	"POST /api/v1/auth/totp/enrol":              "ceremony begin step; commits no credential — nothing has happened until POST /auth/totp/confirm succeeds, which is what records the event",
	"POST /api/v1/auth/passkeys/register/begin": "ceremony begin step; commits no credential — nothing has happened until POST /auth/passkeys/register/finish succeeds, which is what records the event",

	// The passkey equivalent of a login: no auth.Principal exists yet when
	// these run (that's the entire point of a passwordless,
	// discoverable-credential login — see registerPasskeyRoutes's own
	// gating comment), so there is no actor to attribute an event to any
	// more than there is for POST /auth/login itself, which is allowlisted
	// for the same underlying reason above.
	"POST /api/v1/auth/passkeys/login/begin":  "passkey login ceremony begin; no organization, and no actor is established until the ceremony completes",
	"POST /api/v1/auth/passkeys/login/finish": "passkey login ceremony finish; the passkey equivalent of POST /auth/login — no organization, and completing it only ever establishes a session, the same routine event login already is",
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
