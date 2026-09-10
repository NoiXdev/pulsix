package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// eventDTO is the wire shape of one recorded audit event. OrgID/ActorID are
// omitted (via omitempty) rather than emitted as null when absent, matching
// how the rest of the API represents "there is no value here" (e.g.
// tokenDTO.ExpiresAt).
type eventDTO struct {
	ID         string    `json:"id"`
	OrgID      *string   `json:"org_id,omitempty"`
	ActorID    *string   `json:"actor_id,omitempty"`
	ActorKind  string    `json:"actor_kind"`
	Action     string    `json:"action"`
	ObjectType string    `json:"object_type"`
	ObjectID   string    `json:"object_id"`
	Summary    string    `json:"summary"`
	At         time.Time `json:"at"`
}

type activityListOutput struct {
	Body struct {
		Items      []eventDTO `json:"items"`
		NextCursor string     `json:"next_cursor"`
	}
}

type orgActivityListInput struct {
	Org string `path:"org"`
	// Actor, ObjectType, Since and Cursor are plain strings, not uuid.UUID /
	// time.Time / a dedicated cursor type: every other route in this
	// package that takes an id or a cursor-like opaque value in a path or
	// query parameter (orgTokenIDInput.ID, memberRoleInput.UserID) follows
	// the same pattern — huma validates the shape it can (minLength etc.),
	// the handler parses and rejects a malformed value with 400 itself,
	// exactly like uuid.Parse(in.UserID) elsewhere in this package.
	Actor      string `query:"actor"`
	ObjectType string `query:"object_type"`
	Since      string `query:"since"`
	Cursor     string `query:"cursor"`
	Limit      int    `query:"limit"`
}

type myActivityListInput struct {
	ObjectType string `query:"object_type"`
	Since      string `query:"since"`
	Cursor     string `query:"cursor"`
	Limit      int    `query:"limit"`
}

// registerAuditRoutes wires GET /orgs/{org}/activity: the org's own
// activity feed, gated on tenancy.PermAuditRead exactly like every other
// org-scoped route is gated on its own permission. It is mounted on the
// bearer-enabled *huma.Group in api.go (it calls requireOrg, so it consults
// auth.TokenScopeFromContext the same way the organization/member/token
// routes do). This route is a read, not a mutation, so it does not carry
// withAudit — reading the audit log is not itself an audited action.
func registerAuditRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/activity", func(ctx context.Context, in *orgActivityListInput) (*activityListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermAuditRead)
		if err != nil {
			return nil, err
		}
		filter, err := parseActivityFilter(in.Actor, in.ObjectType, in.Since)
		if err != nil {
			return nil, err
		}
		items, next, err := deps.Audit.List(ctx, scope.OrgID, filter, in.Cursor, in.Limit)
		if err != nil {
			return nil, mapAuditErr(ctx, err)
		}
		out := &activityListOutput{}
		out.Body.Items, out.Body.NextCursor = toEventDTOs(items), next
		return out, nil
	})
}

// registerMyActivityRoute wires GET /me/activity: the caller's own actions
// across every organization they belong to. Unlike registerAuditRoutes'
// route, this needs no per-org permission check — it can only ever return
// events the caller themselves is the actor of — so it gates on plain
// requireMFAComplete, the same bar every other non-org-scoped route in this
// package (registerOrgRoutes) clears. It is registered on the root API,
// not the bearer-enabled group, for the same reason registerOrgRoutes is:
// an API token is scoped to the one org it was minted for, not to
// everything its owner could see across every org, so this route accepts a
// session cookie only.
func registerMyActivityRoute(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/me/activity", func(ctx context.Context, in *myActivityListInput) (*activityListOutput, error) {
		p, err := requireMFAComplete(ctx)
		if err != nil {
			return nil, err
		}
		filter, err := parseActivityFilter("", in.ObjectType, in.Since)
		if err != nil {
			return nil, err
		}
		items, next, err := deps.Audit.ListForActor(ctx, p.UserID, filter, in.Cursor, in.Limit)
		if err != nil {
			return nil, mapAuditErr(ctx, err)
		}
		out := &activityListOutput{}
		out.Body.Items, out.Body.NextCursor = toEventDTOs(items), next
		return out, nil
	})
}

// parseActivityFilter turns the three optional, string-typed query
// parameters both activity routes accept into an audit.Filter, rejecting a
// malformed actor id or since timestamp with 400 rather than letting it
// silently match nothing (an empty actorStr means "no actor filter", not
// "match uuid.Nil").
func parseActivityFilter(actorStr, objectType, sinceStr string) (audit.Filter, error) {
	f := audit.Filter{ObjectType: objectType}
	if actorStr != "" {
		id, err := uuid.Parse(actorStr)
		if err != nil {
			return audit.Filter{}, huma.Error400BadRequest("invalid actor id")
		}
		f.ActorID = &id
	}
	if sinceStr != "" {
		t, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			return audit.Filter{}, huma.Error400BadRequest("invalid since: must be RFC 3339")
		}
		f.Since = &t
	}
	return f, nil
}

func toEventDTOs(events []audit.Event) []eventDTO {
	out := make([]eventDTO, len(events))
	for i, e := range events {
		out[i] = eventDTO{
			ID: e.ID.String(), OrgID: uuidPtrToStringPtr(e.OrgID), ActorID: uuidPtrToStringPtr(e.ActorID),
			ActorKind: string(e.ActorKind), Action: e.Action, ObjectType: e.ObjectType,
			ObjectID: e.ObjectID, Summary: e.Summary, At: e.At,
		}
	}
	return out
}

func uuidPtrToStringPtr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// mapAuditErr converts audit package errors to HTTP errors, the same shape
// mapTenancyErr and mapTokenErr give their own packages' sentinels:
// audit.ErrInvalidCursor is the one error a caller can actually cause (a
// tampered or malformed cursor value) and becomes 400; anything else is an
// infrastructure problem, logged and replaced with a generic 500.
func mapAuditErr(ctx context.Context, err error) error {
	if errors.Is(err, audit.ErrInvalidCursor) {
		return huma.Error400BadRequest("invalid cursor")
	}
	slog.ErrorContext(ctx, "unmapped audit error", "err", err)
	return huma.Error500InternalServerError("internal error")
}
