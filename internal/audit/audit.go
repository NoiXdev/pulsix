// Package audit records what happened in Pulsix — every successful
// mutating API call, and (from later tasks) system-generated events with no
// user behind them at all — and reads it back as the org activity feed.
package audit

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ActorKind says how the request that produced an Event was authenticated:
// a browser session, a bearer API token, nothing at all (a Pulsix-initiated
// action), or a schedule firing a run on its own timetable. The API
// middleware that records mutating calls never produces the system or
// schedule kind itself, since every route it wraps requires a resolved
// principal to reach the point where an event is recorded at all.
type ActorKind string

// The kinds of actor an Event can carry.
const (
	ActorKindUser     ActorKind = "user"
	ActorKindToken    ActorKind = "token"
	ActorKindSystem   ActorKind = "system"
	ActorKindSchedule ActorKind = "schedule"
)

// Event is one recorded audit entry. OrgID and ActorID are both pointers
// because either can be absent: OrgID for the handful of actions that are
// not scoped to an organization (creating one, instance-admin actions),
// ActorID for a system-generated event with no user behind it.
type Event struct {
	ID         uuid.UUID
	OrgID      *uuid.UUID
	ActorID    *uuid.UUID
	ActorKind  ActorKind
	Action     string
	ObjectType string
	ObjectID   string
	Summary    string
	Meta       map[string]any
	At         time.Time
}

// Filter narrows a List or ListForActor call. A zero Filter matches
// everything the org or actor scope alone would return.
type Filter struct {
	// ActorID narrows List (the org-scoped feed) to one actor's events. It
	// has no effect on ListForActor, whose actorID parameter already pins
	// the actor.
	ActorID    *uuid.UUID
	ObjectType string
	Since      *time.Time
}

// Store persists and retrieves audit events. There are two read paths
// because they serve genuinely different queries, not one query with an
// optional org filter: List always scopes to one organization (the
// GET /orgs/{org}/activity route, gated on tenancy.PermAuditRead) and can
// additionally be narrowed to one actor within it, while ListForActor scopes
// to one actor across every organization they belong to (GET /me/activity)
// and has no per-org access check to make, since it can only ever return
// the caller's own events. Folding both into a single "orgID
// *uuid.UUID" call would let a nil org id silently mean "every org", which
// is exactly the kind of ambiguity a security-sensitive query should never
// carry.
type Store interface {
	Insert(ctx context.Context, e Event) error
	List(ctx context.Context, orgID uuid.UUID, filter Filter, cursor string, limit int) ([]Event, string, error)
	ListForActor(ctx context.Context, actorID uuid.UUID, filter Filter, cursor string, limit int) ([]Event, string, error)
}

// ErrInvalidCursor is returned by DecodeCursor (and so, by extension, by any
// Store.List/ListForActor implementation that decodes its cursor argument
// through it) when cursor is not one this package produced.
var ErrInvalidCursor = errors.New("audit: invalid cursor")

// EncodeCursor and DecodeCursor are the one cursor format both Store
// implementations in this codebase use — the in-memory fake in
// audit_test.go and the SQL store in store_sql.go — so pagination logic is
// written and tested once rather than twice. The cursor is opaque to
// callers: it carries exactly the keyset (at, id) a stable
// ORDER BY at DESC, id DESC needs to resume after the last row of the
// previous page, including when several events share the same At (id, a
// UUIDv7, breaks the tie deterministically).
func EncodeCursor(at time.Time, id uuid.UUID) string {
	raw := strconv.FormatInt(at.UnixNano(), 10) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor reverses EncodeCursor. An empty cursor decodes to the zero
// time and uuid.Nil with no error — that is the "first page" case every
// caller in this package treats as "no lower bound yet", not a malformed
// cursor.
func DecodeCursor(cursor string) (time.Time, uuid.UUID, error) {
	if cursor == "" {
		return time.Time{}, uuid.Nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	nanoStr, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	nano, err := strconv.ParseInt(nanoStr, 10, 64)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	return time.Unix(0, nano), id, nil
}

// DefaultListLimit and MaxListLimit bound the page size List/ListForActor
// return: DefaultListLimit when the caller asks for no particular size
// (limit <= 0), MaxListLimit as a hard ceiling on however large a size they
// do ask for, so a caller can never force a query to scan and return an
// unbounded number of rows in one page.
const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

// clampLimit applies DefaultListLimit/MaxListLimit to a caller-supplied
// limit.
func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultListLimit
	case limit > MaxListLimit:
		return MaxListLimit
	default:
		return limit
	}
}

// Recorder is the API layer's entry point for writing audit events, and the
// only thing package api's audit middleware holds a reference to (see
// api.Deps.Audit). Like auth.Sessions and auth.Tokens, its clock is
// injected rather than calling time.Now directly, so tests can assert
// exactly what At a recorded event carries.
type Recorder struct {
	store Store
	now   func() time.Time
}

// NewRecorder builds a Recorder around store, using now for every Event's
// At and clampLimit's caller-visible behavior stays store-agnostic — the
// clock only affects writes.
func NewRecorder(store Store, now func() time.Time) *Recorder {
	return &Recorder{store: store, now: now}
}

// Record fills in ID (a fresh UUIDv7) and At (from the injected clock),
// then persists e. It fails closed on the one input a caller could get
// wrong in a way that would make the event meaningless: an empty Action.
// ActorKind is defaulted to ActorKindSystem when the caller leaves it
// unset, matching "no principal behind this" being the only case that
// legitimately omits it (see ActorKind's own doc comment) rather than
// silently persisting an empty string no code elsewhere recognizes.
func (r *Recorder) Record(ctx context.Context, e Event) error {
	if e.Action == "" {
		return errors.New("audit: Action is required")
	}
	e.ID = uuid.Must(uuid.NewV7())
	e.At = r.now()
	if e.ActorKind == "" {
		e.ActorKind = ActorKindSystem
	}
	return r.store.Insert(ctx, e)
}

// List returns orgID's activity feed, most recent first, narrowed by
// filter and paged by cursor. limit is clamped by clampLimit.
func (r *Recorder) List(ctx context.Context, orgID uuid.UUID, filter Filter, cursor string, limit int) ([]Event, string, error) {
	return r.store.List(ctx, orgID, filter, cursor, clampLimit(limit))
}

// ListForActor returns actorID's own events across every organization,
// most recent first, narrowed by filter and paged by cursor. limit is
// clamped by clampLimit.
func (r *Recorder) ListForActor(ctx context.Context, actorID uuid.UUID, filter Filter, cursor string, limit int) ([]Event, string, error) {
	return r.store.ListForActor(ctx, actorID, filter, cursor, clampLimit(limit))
}

// SystemEvent is the reduced shape a system-generated event arrives in:
// no user is behind it, so there is no actor id to pass, only the
// operation, what it touched and why.
type SystemEvent struct {
	Operation  string
	ObjectType string
	ObjectID   string
	// OrgID is nil for instance-level operations (the instance CA is one).
	OrgID   *uuid.UUID
	Summary string
}

// RecordSystem persists ev as a system event: ActorKind system, no actor
// id, the same ID/At stamping Record does. It keeps the existing Record
// API untouched — this is the path transport layers that act without a
// user (the gRPC worker endpoint) take.
func (r *Recorder) RecordSystem(ctx context.Context, ev SystemEvent) error {
	return r.Record(ctx, Event{
		Action:     ev.Operation,
		ObjectType: ev.ObjectType,
		ObjectID:   ev.ObjectID,
		OrgID:      ev.OrgID,
		Summary:    ev.Summary,
		ActorKind:  ActorKindSystem,
	})
}
