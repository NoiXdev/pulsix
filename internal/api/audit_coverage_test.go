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

	// Task 12: sends a one-off message through whatever SMTP settings are
	// currently stored, to let an administrator confirm they work. It
	// persists nothing — PUT /admin/settings/smtp (audited) is the route
	// that actually changes stored configuration — so there is no object
	// mutation here to record, the same reasoning that keeps
	// POST /auth/totp/verify off the audit trail above.
	"POST /api/v1/admin/settings/smtp/test": "sends a one-off test message through the currently stored SMTP settings; persists nothing, so there is no object mutation to record",

	// Task 14: a read — it lists hosts matching a selector, exactly like
	// GET /orgs/{org}/hosts with a filter — that only uses POST because its
	// filter (an arbitrary selector expression) belongs in a request body,
	// not a query string. No object is mutated, so there is nothing for an
	// audit event to attribute.
	"POST /api/v1/orgs/{org}/hosts/query": "a read (lists hosts matching a selector) that uses POST only because the filter is a request body, not query params; persists nothing, so there is no object mutation to record",

	// v0.3b-1: the monitoring group editor's live match count — the same
	// selector resolution /hosts/query above performs, POST for the same
	// reason (the selector is a request body), and saving nothing at all:
	// the point of the route is to see what a selector means *before* a
	// group is written.
	"POST /api/v1/orgs/{org}/monitoring-groups/preview": "a read (resolves a selector to the hosts it matches) that uses POST only because the selector is a request body; persists nothing, so there is no object mutation to record",

	// Task 7: computes the next occurrences of a cron/simple timetable an
	// operator is still editing — like /hosts/query above, POST only
	// because the shape is a request body — and persists nothing.
	"POST /api/v1/orgs/{org}/schedules/preview": "read-only computation of the next occurrences; mutates nothing",

	// v0.3b-2 Task 10: the rule editor's validate-only route — the same
	// shape as /monitoring-groups/preview and /schedules/preview above: a
	// read (parses and type-checks a prospective rule, answering its byte
	// position and whether it fits the form subset) that uses POST only
	// because the rule is a request body, and persists nothing (a create
	// attempt right after a validate call still sees an empty list, pinned
	// by TestRuleRoutes/validate_never_persists). The editor calls this on
	// a debounce while an operator is still typing, so auditing it would
	// bury the three real rule mutations (create, update, delete — all
	// audited) under keystroke noise.
	"POST /api/v1/orgs/{org}/rules/validate": "a read (parses and type-checks a prospective rule) that uses POST only because the rule is a request body; persists nothing, and the editor calls it on a debounce, so auditing it would bury the real rule mutations under keystroke noise",

	// Task 10: the two public webhook receivers. Neither carries a session
	// or bearer principal (there is none — the trigger's own secret is the
	// credential, spec §3), so withAudit's actor resolution has nothing to
	// attribute the event to. The triggers service audits every triggered
	// run itself, directly, as it enqueues (Receive → auditRun in
	// receive.go): actor webhook:<trigger id>, with the source IP in Meta —
	// not through this middleware.
	"POST /api/v1/hooks/jobs/{id}":  "public webhook endpoint; no session or bearer principal exists to attribute an event to — the triggers service audits each triggered run itself as webhook:<id> with the source IP",
	"POST /api/v1/hooks/repos/{id}": "public webhook endpoint; no session or bearer principal exists to attribute an event to — the triggers service audits each triggered run itself as webhook:<id> with the source IP",
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
