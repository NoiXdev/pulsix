package api

import (
	"context"
	"log/slog"
	"net/netip"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
)

// auditCarrierKey is the context key auditMiddleware uses to hand an
// operation's mutable auditCarrier down to requireOrg and the handler.
type auditCarrierKey struct{}

// auditCarrier is a mutable record auditMiddleware places into the request
// context, by pointer, *before* calling next — this ordering is the whole
// mechanism, and is worth spelling out because it is not the obvious way to
// pass data from a handler back up to a wrapping middleware.
//
// huma.Register's handlers are plain Go functions of shape
// func(context.Context, *I) (*O, error) — they never see a huma.Context,
// only the request's context.Context. That context is immutable:
// context.WithValue always returns a *new* context carrying the extra
// value, and never modifies the context.Context the caller (here,
// auditMiddleware) is still holding. So if a handler tried to "return" its
// object id and summary by stuffing them into the context it was given,
// there is no way for that new, derived context to ever become visible to
// auditMiddleware after next(ctx) returns — auditMiddleware's own ctx
// variable still points at whatever it was before the call. This is true
// regardless of huma's own machinery; it is just how context.Context works.
//
// What *is* visible after next returns is not a new context value, but
// mutation through a pointer *already* reachable from a context value set
// before next was called. auditMiddleware installs a fresh *auditCarrier
// into the context via huma.WithValue before invoking next; requireOrg
// (authz.go) and setAuditObject (below) retrieve that exact pointer with
// context.Value — which, however many context.WithValue layers a handler
// derives on top, always returns the same pointer the middleware put there
// — and mutate its fields in place. auditMiddleware, still holding that
// same pointer in its own local carrier variable, reads the mutated fields
// back once next returns.
//
// This is verified end-to-end — against the actual installed huma v2.39.1,
// not by inspecting its source and assuming — by
// TestAuditRecordsRealMutation in audit_handlers_test.go: it drives a real
// PATCH /orgs/{org} request through the whole stack and asserts the
// recorded event carries the org and actor requireOrg resolved and the
// object id and summary the handler set, exactly the round trip this
// comment describes.
type auditCarrier struct {
	haveScope bool
	// orgID is a pointer, not a uuid.UUID, so the carrier can express "this
	// event has no organization" as a real nil rather than the
	// indistinguishable-from-a-real-id zero value uuid.UUID{} would be.
	// Task 11 fix round 1, Finding 1: before this change there was no way
	// to record an event for an instance-level action (deactivating a
	// user) at all — the carrier could only ever hold a concrete org id,
	// and auditMiddleware took the address of whatever local copy it had,
	// which for an unset carrier would silently become a pointer to
	// uuid.Nil instead of no organization. audit_events.org_id is nullable
	// precisely so events like this can be recorded (see its migration's
	// own comment) — see setAuditScope and registerAdminRoutes
	// (member_handlers.go) for how POST /admin/users/{id}/deactivate now
	// uses this.
	orgID  *uuid.UUID
	userID uuid.UUID
	// kind is captured by setAuditScope, at the same point it captures
	// userID, rather than recomputed later from the context auditMiddleware
	// itself holds. That context was captured *before* next ran (it has to
	// be, to install the carrier pointer at all — see this type's own
	// comment above) and so predates bearerAuthMiddleware, which runs
	// *inside* next and is what actually attaches a bearer token's
	// auth.Principal (with TokenID set) to the context requireOrg sees.
	// Recomputing the kind from auditMiddleware's own, earlier context
	// would silently read "no principal yet" for every bearer-token
	// request and misattribute it as ActorKindSystem.
	kind     audit.ActorKind
	objectID string
	summary  string
}

func withAuditCarrier(ctx context.Context) (context.Context, *auditCarrier) {
	c := &auditCarrier{}
	return context.WithValue(ctx, auditCarrierKey{}, c), c
}

func auditCarrierFromContext(ctx context.Context) (*auditCarrier, bool) {
	c, ok := ctx.Value(auditCarrierKey{}).(*auditCarrier)
	return c, ok
}

// setAuditScope records the resolved (org, user) on the request's audit
// carrier, if the operation has one (only operations registered with
// Metadata["audit"] = true get a carrier at all — see auditMiddleware).
// requireOrg calls this itself once it resolves Scope, so every org-scoped
// mutating handler's event carries the real org and actor with no further
// action required from the handler — a handler cannot forget this part
// even if it tries to, since it happens inside requireOrg, which every
// org-scoped handler already calls to authorize the request in the first
// place.
//
// orgID is nil for the instance-level actions that have no organization to
// scope an event to (POST /admin/users/{id}/deactivate, via
// registerAdminRoutes in member_handlers.go) — every other caller passes
// the address of a real, already-resolved org id (requireOrg, and org
// creation in org_handlers.go).
//
// kind is always derived from ctx via actorKind, which is correct for
// every caller except one: POST /invites/accept has no auth.Principal in
// its context at all (it is intentionally public), so it uses
// setAuditScopeAs instead to name ActorKindUser explicitly rather than
// have actorKind silently report ActorKindSystem.
func setAuditScope(ctx context.Context, orgID *uuid.UUID, userID uuid.UUID) {
	setAuditScopeAs(ctx, orgID, userID, actorKind(ctx))
}

// setAuditScopeAs is setAuditScope with the actor kind supplied explicitly
// instead of derived from ctx via actorKind. It exists for
// registerInviteAcceptRoute (member_handlers.go): that route runs with no
// auth.Principal of its own (an invitee proves identity by possessing the
// invite token, not a session or bearer credential), so actorKind(ctx)
// would either report ActorKindSystem (no principal at all — wrong, since
// the invited user themselves squarely performed this action) or, worse,
// pick up an unrelated principal from a stale session cookie the caller's
// browser happens to still be carrying (CookieMiddleware runs on every
// route regardless of whether the route uses it). Naming the kind
// explicitly sidesteps both.
func setAuditScopeAs(ctx context.Context, orgID *uuid.UUID, userID uuid.UUID, kind audit.ActorKind) {
	if c, ok := auditCarrierFromContext(ctx); ok {
		c.haveScope, c.orgID, c.userID, c.kind = true, orgID, userID, kind
	}
}

// setAuditObject records the mutated object and a human-readable summary on
// the request's audit carrier. A handler registered with
// Metadata["audit"] = true calls this once it knows what it changed, right
// before returning success. A handler that forgets to call it still
// produces an event — the recording itself is never skipped, see
// auditMiddleware — just with an empty object id and summary, which is
// visible immediately (in the activity feed itself, or in code review)
// rather than silently missing.
func setAuditObject(ctx context.Context, objectID, summary string) {
	if c, ok := auditCarrierFromContext(ctx); ok {
		c.objectID, c.summary = objectID, summary
	}
}

// recordAuditFailure records an audit event directly through deps.Audit,
// bypassing auditMiddleware's own success-only gate (ctx.Status() < 300 —
// see its comment). It exists for the rare handler whose underlying mutation
// can partially commit before the request as a whole fails: ordinarily "the
// request failed" and "nothing changed" are the same fact, and the
// middleware's gate is right to treat them that way, but a handler that
// knows otherwise — its own write already committed — needs its own way to
// say so, since the gate itself cannot tell the difference between "nothing
// happened" and "something happened, then something else failed".
//
// orgID and userID are passed explicitly rather than read off the request's
// audit carrier (setAuditScope's normal path): a handler reaching for this
// already has its resolved Scope in hand from requireOrg, and the carrier's
// own fields are about to be read by a middleware pass that will throw this
// event's summary away regardless (same gate, same reason).
func recordAuditFailure(ctx context.Context, deps Deps, orgID, userID uuid.UUID, action, objectType, objectID, summary string) {
	if err := deps.Audit.Record(db.WithOrgScope(ctx, orgID), audit.Event{
		OrgID: &orgID, ActorID: &userID, ActorKind: actorKind(ctx),
		Action: action, ObjectType: objectType, ObjectID: objectID, Summary: summary,
	}); err != nil {
		slog.ErrorContext(ctx, "audit: failed to record event", "err", err, "action", action)
	}
}

// actorKind reports how the current request was authenticated, for an
// event's ActorKind: "token" for a bearer API token (auth.Principal.TokenID
// is set exactly when auth.Tokens.Resolve produced the principal — see its
// own doc comment), "user" for a browser session, "system" when no
// principal is present at all. auditMiddleware never actually produces
// "system" itself, since every operation it records requires requireOrg to
// have succeeded, which requires a principal — that value is reserved for
// later tasks recording system-generated events directly through
// audit.Recorder, bypassing this middleware entirely.
func actorKind(ctx context.Context) audit.ActorKind {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return audit.ActorKindSystem
	}
	if p.TokenID != nil {
		return audit.ActorKindToken
	}
	return audit.ActorKindUser
}

// withAudit is an operation modifier (the variadic func(*huma.Operation)
// parameter every huma.Get/Post/Patch/Delete call accepts) that marks an
// operation as one auditMiddleware should record on success, and names the
// kind of object it acts on for the recorded event's ObjectType.
//
// It also sets the operation's OperationID explicitly, rather than leaving
// it to huma's own auto-generated one (a kebab-case slug of the method and
// path, e.g. "patch-api-v1-orgs-by-org" — accurate, but not what anyone
// reading an activity feed wants to see). Every route this task instruments
// was already left at huma's default, so giving audited routes an explicit,
// readable id here does not change behavior for any existing caller (the
// OpenAPI document simply gains a nicer operationId for these operations)
// and makes Action ("<method> <operationID>", set by auditMiddleware)
// legible: "PATCH updateOrganization" instead of
// "PATCH patch-api-v1-orgs-by-org".
func withAudit(operationID, objectType string) func(o *huma.Operation) {
	return func(o *huma.Operation) {
		o.OperationID = operationID
		o.Metadata["audit"] = true
		o.Metadata["auditObject"] = objectType
	}
}

// auditMiddleware wraps every operation whose Metadata sets "audit" = true
// (see withAudit) and, once the request completes, records one audit.Event
// iff it succeeded (status < 300) and actually reached a point that
// resolved a scope (setAuditScope — normally via requireOrg). An operation
// that failed before that point — a bad path, an unauthenticated caller, an
// org the caller cannot access — has nothing to attribute an event to and
// so produces none, matching "only mutating calls that succeeded are
// recorded".
//
// It is mounted once, globally, on the root API in registerRoutes (not
// per-group): huma.Group.Middlewares returns its parent API's middlewares
// plus its own (see huma/v2's group.go), so a single
// hapi.UseMiddleware(auditMiddleware(deps)) registered before any operation
// is added covers every operation registered afterwards, on the root API or
// any group built on top of it, with no risk of a future group forgetting
// to mount it — the same reasoning api.go's own comment gives for why
// bearerAuthMiddleware, by contrast, deliberately is scoped to one group.
func auditMiddleware(deps Deps) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		mutating, _ := op.Metadata["audit"].(bool)
		if !mutating {
			next(ctx)
			return
		}

		reqCtx, carrier := withAuditCarrier(ctx.Context())
		next(huma.WithContext(ctx, reqCtx))

		if ctx.Status() >= 300 || !carrier.haveScope {
			return
		}
		objType, _ := op.Metadata["auditObject"].(string)
		userID := carrier.userID
		meta := map[string]any{}
		if ip := clientAddr(deps, ctx); ip.IsValid() {
			meta["source_ip"] = ip.String()
		}
		// An event that belongs to an organization is written inside that
		// organization's database scope, so row-level security checks the
		// insert like every other org-scoped write. reqCtx itself predates
		// orgScopeMiddleware (this middleware wraps the whole operation), so
		// the scope is bound here from the organization requireOrg — or the
		// handler itself — attributed the event to. Instance-level events
		// (nil org) stay unscoped.
		recordCtx := reqCtx
		if carrier.orgID != nil {
			recordCtx = db.WithOrgScope(reqCtx, *carrier.orgID)
		}
		if err := deps.Audit.Record(recordCtx, audit.Event{
			// carrier.orgID is passed through as-is (nil for an
			// instance-level action, a real address otherwise) rather than
			// copied into a local and re-addressed — see auditCarrier's own
			// doc comment on why that used to be able to silently turn "no
			// organization" into "organization uuid.Nil".
			OrgID: carrier.orgID, ActorID: &userID, ActorKind: carrier.kind,
			Action: ctx.Method() + " " + op.OperationID, ObjectType: objType,
			ObjectID: carrier.objectID, Summary: carrier.summary, Meta: meta,
		}); err != nil {
			// Recording failures must not break the request the user
			// already received a successful response for — the mutation
			// itself already happened and was already reported to the
			// caller — but they must not vanish either: logged at ERROR so
			// an operator sees a gap forming in the audit trail, the same
			// tradeoff mapTenancyErr/mapAuthErr make for an unmapped
			// service error, just without a client-facing status code to
			// also set since there is no response left to change.
			slog.ErrorContext(reqCtx, "audit: failed to record event", "err", err, "action", ctx.Method()+" "+op.OperationID)
		}
	}
}

// clientAddr resolves the request's client address through the trusted
// proxy list (internal/clientip). huma exposes headers one callback at a
// time, so X-Forwarded-For lines are collected in order first.
func clientAddr(deps Deps, ctx huma.Context) netip.Addr {
	var xff []string
	ctx.EachHeader(func(name, value string) {
		if strings.EqualFold(name, "X-Forwarded-For") {
			xff = append(xff, value)
		}
	})
	return deps.ClientIP.Resolve(ctx.RemoteAddr(), xff)
}
