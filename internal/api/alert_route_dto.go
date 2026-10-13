package api

import (
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// AlertRouteDTO is one alertroutes.Route on the wire. Scope/ScopeID follow
// the same shape maintenanceWindowDTO already gives TargetKind/TargetID
// (maintenance_handlers.go): scope_id names the monitoring group or host
// the route targets and is present exactly when scope is not "org" —
// alertroutes.Route.ScopeID is itself a single *uuid.UUID, nil exactly
// then (route.go's own invariant), so one optional string reads more
// naturally over HTTP than, say, two separate target fields that are
// mostly empty.
//
// Severities and Events are the route's own selections, always present
// (never omitted) even on the per-host union view
// (registerAlertRouteRoutes' GET .../hosts/{id}/alert-routes): Service.
// ForHost deliberately ignores them when deciding whether a route covers a
// host at all (ForHost's own doc comment, backlog 4.1b), so the GUI needs
// them on the wire to show "only fires on crit" beside a route that still
// appears in the list.
type AlertRouteDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Scope   string `json:"scope" enum:"org,monitoring_group,host"`
	ScopeID string `json:"scope_id,omitempty" doc:"The monitoring group or host id this route targets; absent for scope org"`
	// Severities and Events deliberately carry no enum tag naming every
	// member: an unrecognized value stored by a future version must still
	// round-trip through this DTO rather than fail an OpenAPI consumer's
	// strict validation on a field this route never writes.
	Severities []string `json:"severities" doc:"Which alert severities this route fires on: warn, crit or unknown"`
	Events     []string `json:"events" doc:"Which alert lifecycle events this route fires on: opened, severity_raised, acknowledged, ack_voided, snoozed, unsnoozed or closed"`
	Channels   []string `json:"channels" doc:"Notification channel ids this route delivers to"`
	// Window, BreakthroughSeverities and RotationID are v0.4c's own
	// additions (design doc "API and permissions"): a route with no window
	// may deliver at any time, the v0.4b default these three fields extend.
	Window                 *AlertRouteWindowDTO `json:"window,omitempty" doc:"The route's delivery window; absent means it may deliver at any time"`
	BreakthroughSeverities []string             `json:"breakthrough_severities,omitempty" doc:"The subset of severities that crosses a closed window anyway; only meaningful alongside window"`
	RotationID             string               `json:"rotation_id,omitempty" doc:"An on-call rotation whose current channel replaces this route's fixed channel list; absent means channels is used as given"`
	Revision               int64                `json:"revision"`
	CreatedAt              time.Time            `json:"created_at"`
	UpdatedAt              time.Time            `json:"updated_at"`
}

// AlertRouteWindowDTO is a route's delivery window on the wire
// (alertroutes.Window): cron for the start instants, a duration in seconds
// to make each an interval, and a timezone.
type AlertRouteWindowDTO struct {
	Cron            string `json:"cron" doc:"A 5-field cron expression naming the window's start instants"`
	DurationSeconds int64  `json:"duration_seconds" doc:"Length of each occurrence, in seconds"`
	Timezone        string `json:"timezone" doc:"IANA timezone the cron is evaluated in"`
}

// toAlertRouteDTO flattens one alertroutes.Route onto the wire shape every
// alert-route route answers with.
func toAlertRouteDTO(r alertroutes.Route) AlertRouteDTO {
	dto := AlertRouteDTO{
		ID: r.ID.String(), Name: r.Name, Enabled: r.Enabled, Scope: string(r.Scope),
		Severities: make([]string, len(r.Severities)),
		Events:     make([]string, len(r.Events)),
		Channels:   make([]string, len(r.Channels)),
		Revision:   r.Revision, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.ScopeID != nil {
		dto.ScopeID = r.ScopeID.String()
	}
	for i, sev := range r.Severities {
		dto.Severities[i] = string(sev)
	}
	for i, ev := range r.Events {
		dto.Events[i] = string(ev)
	}
	for i, ch := range r.Channels {
		dto.Channels[i] = ch.String()
	}
	if r.Window != nil {
		dto.Window = &AlertRouteWindowDTO{
			Cron: r.Window.Cron, DurationSeconds: int64(r.Window.Duration / time.Second), Timezone: r.Window.Timezone,
		}
	}
	if len(r.Breakthrough) > 0 {
		dto.BreakthroughSeverities = make([]string, len(r.Breakthrough))
		for i, s := range r.Breakthrough {
			dto.BreakthroughSeverities[i] = string(s)
		}
	}
	if r.RotationID != nil {
		dto.RotationID = r.RotationID.String()
	}
	return dto
}

// AlertRouteBody is the create/update request body — one shape for both
// verbs, like MaintenanceWindowBody (maintenance_handlers.go): a PATCH
// here replaces the whole route, the same full-replace semantics
// host_handlers.go's PATCH /hosts/{id} and inventory_handlers.go's PATCH
// /inventory/groups/{id} already give their own resources (backlog 1.31's
// If-Match pattern governs the concurrency check, not whether the body is
// partial).
//
// Every field but Enabled carries omitempty, even though a create or
// update that leaves one of them out still fails — not at huma's schema
// layer, but at alertroutes.Validate ("a route needs a name"/"a route
// needs at least one channel", surfaced as 400 by mapAlertRouteErr). That
// is deliberate: without omitempty, huma's generated schema marks every
// such field required and refuses a request missing one with its own 422
// before the handler — including before requireIfMatch ever runs — which
// would turn a PATCH that omits If-Match AND resubmits only the field
// being changed into a 422 "missing name" rather than the 428 "missing
// If-Match" the caller actually needs to see (backlog 1.31's own
// precondition-first contract; TestAlertRouteUpdateRequiresIfMatch pins
// this ordering with exactly such a body).
type AlertRouteBody struct {
	Name       string   `json:"name,omitempty" minLength:"1" maxLength:"100"`
	Enabled    bool     `json:"enabled"`
	Scope      string   `json:"scope,omitempty" enum:"org,monitoring_group,host"`
	ScopeID    string   `json:"scope_id,omitempty" doc:"The monitoring group or host id this route targets; omit for scope org"`
	Severities []string `json:"severities,omitempty" doc:"warn, crit or unknown"`
	Events     []string `json:"events,omitempty" doc:"opened, severity_raised, acknowledged, ack_voided, snoozed, unsnoozed or closed"`
	Channels   []string `json:"channels,omitempty" doc:"Notification channel ids"`
	// Window, BreakthroughSeverities and RotationID are v0.4c's own
	// additions; all three are optional the same way the rest of this body
	// is (see the doc comment above) — omitting window leaves the route
	// free to deliver at any time, alertroutes.Validate's own default.
	Window                 *AlertRouteWindowDTO `json:"window,omitempty"`
	BreakthroughSeverities []string             `json:"breakthrough_severities,omitempty" doc:"Severities that cross a closed window anyway; needs window"`
	RotationID             string               `json:"rotation_id,omitempty" doc:"An on-call rotation to resolve the delivery channel from instead of channels"`
}

// input turns the wire body into alertroutes.Input. A malformed scope_id or
// channel id is the caller's own mistake (400) before this ever reaches
// alertroutes.Validate, which only ever sees well-formed ids and judges
// their meaning (unknown severity/event name, empty set, duplicate
// member).
func (b AlertRouteBody) input() (alertroutes.Input, error) {
	scopeID, err := optionalUUID(b.ScopeID, "scope id")
	if err != nil {
		return alertroutes.Input{}, err
	}
	severities := make([]rules.State, len(b.Severities))
	for i, s := range b.Severities {
		severities[i] = rules.State(s)
	}
	events := make([]alerts.EventKind, len(b.Events))
	for i, e := range b.Events {
		events[i] = alerts.EventKind(e)
	}
	channels := make([]uuid.UUID, len(b.Channels))
	for i, c := range b.Channels {
		id, err := parseUUID(c, "channel id")
		if err != nil {
			return alertroutes.Input{}, err
		}
		channels[i] = id
	}
	var window *alertroutes.Window
	if b.Window != nil {
		window = &alertroutes.Window{
			Cron: b.Window.Cron, Duration: time.Duration(b.Window.DurationSeconds) * time.Second, Timezone: b.Window.Timezone,
		}
	}
	breakthrough := make([]rules.State, len(b.BreakthroughSeverities))
	for i, s := range b.BreakthroughSeverities {
		breakthrough[i] = rules.State(s)
	}
	rotationID, err := optionalUUID(b.RotationID, "rotation id")
	if err != nil {
		return alertroutes.Input{}, err
	}
	return alertroutes.Input{
		Name: b.Name, Enabled: b.Enabled, Scope: alertroutes.Scope(b.Scope), ScopeID: scopeID,
		Severities: severities, Events: events, Channels: channels,
		Window: window, Breakthrough: breakthrough, RotationID: rotationID,
	}, nil
}
