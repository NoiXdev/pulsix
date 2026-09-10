package api

import (
	"context"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
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
	orgID     uuid.UUID
	userID    uuid.UUID
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
func setAuditScope(ctx context.Context, orgID, userID uuid.UUID) {
	if c, ok := auditCarrierFromContext(ctx); ok {
		c.haveScope, c.orgID, c.userID, c.kind = true, orgID, userID, actorKind(ctx)
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
		orgID := carrier.orgID
		if err := deps.Audit.Record(reqCtx, audit.Event{
			OrgID: &orgID, ActorID: &userID, ActorKind: carrier.kind,
			Action: ctx.Method() + " " + op.OperationID, ObjectType: objType,
			ObjectID: carrier.objectID, Summary: carrier.summary,
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
