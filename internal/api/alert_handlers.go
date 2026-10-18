package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// The alert API (v0.4a design doc, "API and permissions"): six endpoints —
// list, get-with-timeline, and the four operator actions
// (acknowledge/unacknowledge, snooze/unsnooze). The host view the design
// calls for is a filter on List (?host=), not a seventh route. Reads and
// writes share checks.read/checks.write — the design doc's own ruling is
// that v0.4a introduces no new permission for acknowledging or snoozing,
// "an ack is arguably a lighter act ... that is a backlog question, not a
// reason to grow the permission model here".

// AlertDTO is one alerts.Alert plus its host's name and its
// alerts.Suppression on the wire — the shape alerts.View already carries,
// flattened rather than nested so the list and the detail route (which
// embeds this in alertDetailDTO) share one set of field names. Severity is
// never "ok" (migration 00049's own CHECK; alert.go's doc comment on Alert
// says why) — hence the enum.
type AlertDTO struct {
	ID     string `json:"id"`
	HostID string `json:"host_id"`
	// HostName is joined in by the store (ListAlerts/GetAlert's own SQL,
	// internal/alerts/store_sql.go) rather than left for the GUI to
	// resolve from host_id: the alerts list is the operator's front door,
	// and "which machine is this" is the first thing it has to answer.
	HostName string `json:"host_name"`
	Scope    string `json:"scope" enum:"tuple,host"`
	CheckKey string `json:"check_key,omitempty"`
	Item     string `json:"item,omitempty"`
	Metric   string `json:"metric,omitempty"`

	OpenedAt time.Time `json:"opened_at"`
	Severity string    `json:"severity" enum:"warn,crit,unknown"`
	Peak     string    `json:"peak_severity" enum:"warn,crit,unknown"`

	ClosedAt     *time.Time `json:"closed_at,omitempty"`
	ClosedReason string     `json:"closed_reason,omitempty" enum:"resolved,stale"`

	AckAt       *time.Time `json:"ack_at,omitempty"`
	AckBy       string     `json:"ack_by,omitempty"`
	AckSeverity string     `json:"ack_severity,omitempty" enum:"warn,crit,unknown"`
	AckNote     string     `json:"ack_note,omitempty"`

	SnoozedUntil      *time.Time `json:"snoozed_until,omitempty"`
	SnoozeUntilChange bool       `json:"snooze_until_change"`

	WasFlapping bool `json:"was_flapping"`

	// Muted/MuteReason/MutedUntil/MuteWindowID are alerts.Suppression,
	// Mute's own verdict (internal/alerts/mute.go) — v0.4a gives this no
	// effect beyond the label (service.go's own doc comment on View).
	Muted        bool       `json:"muted"`
	MuteReason   string     `json:"mute_reason,omitempty" enum:"maintenance,silence,snooze"`
	MutedUntil   *time.Time `json:"muted_until,omitempty" doc:"Absent for an unbounded snooze-until-change"`
	MuteWindowID string     `json:"mute_window_id,omitempty" doc:"Set only when mute_reason is maintenance or silence"`
}

// toAlertDTO flattens one alerts.Alert, its host's name and its
// alerts.Suppression onto the wire shape every one of the six routes
// answers with. hostName is a separate parameter rather than a field read
// off alerts.Alert itself: the lifecycle's own Alert (Reconcile/Execute)
// has no concept of a host name, only alerts.View/Detail do, once
// Service.List/Get attach it from the store's own join.
func toAlertDTO(a alerts.Alert, hostName string, sup alerts.Suppression) AlertDTO {
	dto := AlertDTO{
		ID: a.ID.String(), HostID: a.Subject.HostID.String(), HostName: hostName, Scope: string(a.Subject.Scope),
		CheckKey: a.Subject.CheckKey, Item: a.Subject.Item, Metric: a.Subject.Metric,
		OpenedAt: a.OpenedAt, Severity: string(a.Severity), Peak: string(a.Peak),
		ClosedAt: a.ClosedAt, ClosedReason: string(a.ClosedReason),
		AckAt: a.AckAt, AckSeverity: string(a.AckSeverity), AckNote: a.AckNote,
		SnoozedUntil: a.SnoozedUntil, SnoozeUntilChange: a.SnoozeUntilChange,
		WasFlapping: a.WasFlapping,
		Muted:       sup.Muted, MuteReason: string(sup.Reason), MutedUntil: sup.Until,
	}
	if a.AckBy != nil {
		dto.AckBy = a.AckBy.String()
	}
	if sup.WindowID != nil {
		dto.MuteWindowID = sup.WindowID.String()
	}
	return dto
}

// alertEventDTO is one alerts.Event (an alert_events row) on the wire.
// EscalationPosition and EscalationChannelIDs are set only for kind
// "escalated" (alerts.EventEscalated, v0.4d) — the ladder step that fired
// and the channels it reached, decoded from the row's own jsonb payload
// (alerts.EscalatedPayload). Every other kind leaves EscalationPosition at
// its zero value (omitted — a route's own validation never allows position
// 0, so there is nothing to lose): their own payload is the
// InsertAlertEvent default '{}', carrying nothing worth exposing (alerts.
// Event's own doc comment).
//
// EscalationChannelIDs deliberately carries NO omitempty, unlike every
// other optional field here: v0.4b already made "this route reaches
// nobody" an explicit, named fact rather than an empty list that reads the
// same as "no information" (ReachesNobody, AlertRouteCells.tsx) — a step
// that reached nobody is that same fact about one rung, and MUST stay
// distinguishable from "not an escalated event" / "payload failed to
// decode". omitempty would collapse `[]` (reached nobody) into the exact
// same wire absence as those two, since Go's own omitempty cannot tell an
// empty slice from a nil one. Without it, a non-escalated event's zero
// value (nil) serializes as `"escalation_channel_ids": null` — still
// absent in meaning, but never silently equal to the `[]` a reached-nobody
// rung must produce.
type alertEventDTO struct {
	ID                   string    `json:"id"`
	At                   time.Time `json:"at"`
	Kind                 string    `json:"kind" enum:"opened,severity_raised,acknowledged,ack_voided,snoozed,unsnoozed,closed,escalated"`
	ActorID              string    `json:"actor_id,omitempty"`
	EscalationPosition   int       `json:"escalation_position,omitempty" doc:"The ladder step that fired; only set for kind escalated"`
	EscalationChannelIDs []string  `json:"escalation_channel_ids" doc:"The channels this step reached: empty (not null) when the step reached nobody; null for every kind other than escalated, or if the payload could not be decoded"`
}

// alertStateChangeDTO is one alerts.StateChange (a check_state_changes row
// inside the alert's own lifetime) on the wire.
type alertStateChangeDTO struct {
	At        time.Time `json:"at"`
	FromState string    `json:"from_state" enum:"ok,warn,crit,unknown"`
	ToState   string    `json:"to_state" enum:"ok,warn,crit,unknown"`
}

// alertTimelineEntryDTO is one alerts.TimelineEntry: exactly one of Event
// or Change is set, named by Kind — mergeTimeline's own contract
// (internal/alerts/timeline.go).
type alertTimelineEntryDTO struct {
	At     time.Time            `json:"at"`
	Kind   string               `json:"kind" enum:"event,state_change"`
	Event  *alertEventDTO       `json:"event,omitempty"`
	Change *alertStateChangeDTO `json:"change,omitempty"`
}

// alertDetailDTO is AlertDTO plus its timeline and notification hold — GET
// /alerts/{id}'s own shape, matching alerts.Detail.
type alertDetailDTO struct {
	AlertDTO
	Timeline []alertTimelineEntryDTO `json:"timeline"`
	// NotificationHold answers "why did nothing arrive" (v0.4c Task 11,
	// "The hold indicator on an alert"): absent/"" when nothing is held,
	// "deferred" when every route wanting this alert is outside its
	// delivery window, "suppressed" when the host is in maintenance —
	// alerts.Detail.NotificationHold's own doc comment on why 'suppressed'
	// outranks 'deferred' when both are true of the same alert. Only on
	// the detail, not AlertDTO's own list shape: the list's job is "what
	// is open", not "why did nobody hear about it" — that question is
	// asked one alert at a time, on the page where its full story already
	// lives.
	NotificationHold string `json:"notification_hold,omitempty" enum:"deferred,suppressed"`
}

func toAlertDetailDTO(d alerts.Detail) alertDetailDTO {
	out := alertDetailDTO{
		AlertDTO:         toAlertDTO(d.Alert, d.HostName, d.Suppression),
		NotificationHold: d.NotificationHold,
	}
	out.Timeline = make([]alertTimelineEntryDTO, len(d.Timeline))
	for i, e := range d.Timeline {
		entry := alertTimelineEntryDTO{At: e.At, Kind: string(e.Kind)}
		if e.Event != nil {
			ev := alertEventDTO{ID: e.Event.ID.String(), At: e.Event.At, Kind: e.Event.Kind}
			if e.Event.ActorID != nil {
				ev.ActorID = e.Event.ActorID.String()
			}
			// Best-effort: a decode failure (payload '{}' on every other
			// kind, or a future kind added here with no payload shape yet)
			// leaves both fields at their zero value rather than failing
			// the whole timeline over one row.
			if e.Event.Kind == string(alerts.EventEscalated) {
				var p alerts.EscalatedPayload
				if err := json.Unmarshal(e.Event.Payload, &p); err == nil {
					ev.EscalationPosition = p.Position
					ev.EscalationChannelIDs = make([]string, len(p.Channels))
					for i, c := range p.Channels {
						ev.EscalationChannelIDs[i] = c.String()
					}
				}
			}
			entry.Event = &ev
		}
		if e.Change != nil {
			entry.Change = &alertStateChangeDTO{At: e.Change.At, FromState: string(e.Change.FromState), ToState: string(e.Change.ToState)}
		}
		out.Timeline[i] = entry
	}
	return out
}

type alertListInput struct {
	Org          string `path:"org"`
	State        string `query:"state" doc:"open or closed"`
	Severity     string `query:"severity" doc:"warn, crit or unknown"`
	Host         string `query:"host" doc:"Restrict to one host's alerts — the host page's own view"`
	Scope        string `query:"scope" doc:"tuple or host"`
	Acknowledged string `query:"acknowledged" doc:"true or false"`
	Muted        string `query:"muted" doc:"true or false"`
	Cursor       string `query:"cursor" doc:"Opaque cursor from a previous page's next_cursor"`
	Limit        int    `query:"limit" doc:"Page size; the server clamps it"`
}

type alertListOutput struct {
	Body struct {
		Alerts []AlertDTO `json:"alerts"`
		// NextCursor is empty on the last page, the same contract the
		// hosts list already gives. A page can be shorter than the limit
		// while this is still set — Service.List applies the `muted`
		// filter after the page is cut (see its own doc comment) — so a
		// client follows this until it comes back empty rather than until
		// a page looks short.
		NextCursor string `json:"next_cursor"`
	}
}

type alertIDInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type alertGetOutput struct {
	Body struct {
		Alert alertDetailDTO `json:"alert"`
	}
}

type alertAcknowledgeInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Note string `json:"note,omitempty" maxLength:"1000"`
	}
}

type alertSnoozeInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body struct {
		Until       *time.Time `json:"until,omitempty" doc:"Mute until this instant; mutually exclusive with until_change"`
		UntilChange bool       `json:"until_change,omitempty" doc:"Mute until the alert's severity next changes; mutually exclusive with until"`
	}
}

// registerAlertRoutes wires the six routes. Every route resolves its scope
// through requireOrg — reads at checks.read, writes (acknowledge, snooze
// and their reversals) at checks.write, same bar as the rest of the
// monitoring surface — and is mounted on the bearer-enabled *huma.Group in
// api.go alongside every other org-scoped resource.
func registerAlertRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/alerts", func(ctx context.Context, in *alertListInput) (*alertListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		f, err := parseAlertListFilter(in)
		if err != nil {
			return nil, err
		}
		list, next, err := deps.Alerts.List(ctx, scope.OrgID, f, in.Cursor, in.Limit)
		if err != nil {
			return nil, mapAlertsErr(ctx, err)
		}
		out := &alertListOutput{}
		out.Body.Alerts = make([]AlertDTO, len(list))
		for i, v := range list {
			out.Body.Alerts[i] = toAlertDTO(v.Alert, v.HostName, v.Suppression)
		}
		out.Body.NextCursor = next
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/alerts/{id}", func(ctx context.Context, in *alertIDInput) (*alertGetOutput, error) {
		scope, id, err := alertScope(ctx, deps, in.Org, in.ID, tenancy.PermChecksRead)
		if err != nil {
			return nil, err
		}
		d, err := deps.Alerts.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapAlertsErr(ctx, err)
		}
		out := &alertGetOutput{}
		out.Body.Alert = toAlertDetailDTO(d)
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/alerts/{id}/acknowledge", func(ctx context.Context, in *alertAcknowledgeInput) (*okOutput, error) {
		scope, id, err := alertScope(ctx, deps, in.Org, in.ID, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.Alerts.Acknowledge(ctx, scope.OrgID, id, scope.UserID, in.Body.Note); err != nil {
			return nil, mapAlertsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "acknowledged alert")
		return &okOutput{}, nil
	}, withAudit("acknowledgeAlert", "alert"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/alerts/{id}/acknowledge", func(ctx context.Context, in *alertIDInput) (*okOutput, error) {
		scope, id, err := alertScope(ctx, deps, in.Org, in.ID, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.Alerts.Unacknowledge(ctx, scope.OrgID, id, scope.UserID); err != nil {
			return nil, mapAlertsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "unacknowledged alert")
		return &okOutput{}, nil
	}, withAudit("unacknowledgeAlert", "alert"))

	huma.Post(hapi, "/api/v1/orgs/{org}/alerts/{id}/snooze", func(ctx context.Context, in *alertSnoozeInput) (*okOutput, error) {
		scope, id, err := alertScope(ctx, deps, in.Org, in.ID, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.Alerts.Snooze(ctx, scope.OrgID, id, scope.UserID, in.Body.Until, in.Body.UntilChange); err != nil {
			return nil, mapAlertsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "snoozed alert")
		return &okOutput{}, nil
	}, withAudit("snoozeAlert", "alert"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/alerts/{id}/snooze", func(ctx context.Context, in *alertIDInput) (*okOutput, error) {
		scope, id, err := alertScope(ctx, deps, in.Org, in.ID, tenancy.PermChecksWrite)
		if err != nil {
			return nil, err
		}
		if err := deps.Alerts.Unsnooze(ctx, scope.OrgID, id, scope.UserID); err != nil {
			return nil, mapAlertsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "unsnoozed alert")
		return &okOutput{}, nil
	}, withAudit("unsnoozeAlert", "alert"))
}

// alertScope resolves both the caller's authorization and the path's alert
// id in one call, the same shape maintenanceScope (maintenance_handlers.go)
// gives its own resource.
func alertScope(ctx context.Context, deps Deps, org, rawID string, perm tenancy.Permission) (Scope, uuid.UUID, error) {
	scope, err := requireOrg(ctx, deps, org, perm)
	if err != nil {
		return Scope{}, uuid.Nil, err
	}
	id, err := parseUUID(rawID, "alert id")
	return scope, id, err
}

// parseAlertListFilter turns the list route's string-typed query parameters
// into an alerts.Filter, rejecting a malformed value with 400 rather than
// letting it silently match nothing or everything — the same contract
// parseHostListFilter (host_handlers.go) gives hosts.ListFilter.
func parseAlertListFilter(in *alertListInput) (alerts.Filter, error) {
	var f alerts.Filter
	switch in.State {
	case "":
	case "open":
		open := true
		f.Open = &open
	case "closed":
		closed := false
		f.Open = &closed
	default:
		return alerts.Filter{}, huma.Error400BadRequest("invalid state: must be open or closed")
	}
	switch in.Severity {
	case "":
	case string(rules.StateWarn), string(rules.StateCrit), string(rules.StateUnknown):
		f.Severity = rules.State(in.Severity)
	default:
		return alerts.Filter{}, huma.Error400BadRequest("invalid severity: must be warn, crit or unknown")
	}
	host, err := optionalUUID(in.Host, "host")
	if err != nil {
		return alerts.Filter{}, err
	}
	f.HostID = host
	switch in.Scope {
	case "":
	case string(alerts.ScopeTuple), string(alerts.ScopeHost):
		f.Scope = alerts.Scope(in.Scope)
	default:
		return alerts.Filter{}, huma.Error400BadRequest("invalid scope: must be tuple or host")
	}
	if in.Acknowledged != "" {
		b, err := strconv.ParseBool(in.Acknowledged)
		if err != nil {
			return alerts.Filter{}, huma.Error400BadRequest("invalid acknowledged: must be true or false")
		}
		f.Acknowledged = &b
	}
	if in.Muted != "" {
		b, err := strconv.ParseBool(in.Muted)
		if err != nil {
			return alerts.Filter{}, huma.Error400BadRequest("invalid muted: must be true or false")
		}
		f.Muted = &b
	}
	return f, nil
}

// mapAlertsErr maps an internal/alerts error to its HTTP status, the same
// shape mapItemsErr and mapRulesErr each give their own domain's errors:
// ErrInvalidInput -> 422 naming the body (every ErrInvalidInput the
// service returns — the "mutually exclusive"/"requires either" snooze
// checks, the past-instant check, "cannot acknowledge a closed alert" — is
// about what the caller asked for, never about which alert id was named),
// ErrNotFound -> 404.
func mapAlertsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, alerts.ErrInvalidCursor):
		// A malformed ?cursor= is the caller's mistake, not the server's —
		// the same 400 mapHostsErr already gives hosts.ErrInvalidCursor.
		return huma.Error400BadRequest("invalid cursor")
	case errors.Is(err, alerts.ErrInvalidInput):
		msg := err.Error()
		return huma.Error422UnprocessableEntity(msg, &huma.ErrorDetail{Message: msg, Location: "body"})
	case errors.Is(err, alerts.ErrNotFound):
		return huma.Error404NotFound("not found")
	}
	slog.ErrorContext(ctx, "unmapped alerts error", "err", err)
	return huma.Error500InternalServerError("internal error")
}
