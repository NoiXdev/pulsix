package api

import (
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/oncall"
)

// OncallRotationDTO is one oncall.Rotation on the wire: an ordered channel
// cycle, a period and an anchor (the first handover) — the shape Validate
// (rotation.go) needs to answer who is on call at any instant.
type OncallRotationDTO struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	PeriodDays int       `json:"period_days"`
	Anchor     time.Time `json:"anchor" doc:"The first handover; date and time together"`
	Timezone   string    `json:"timezone"`
	// Channels deliberately carries only ids, unlike OncallShiftDTO's own
	// ChannelName: this is the rotation's own configuration screen, which
	// already has the organization's channel list loaded to build the
	// picker this field feeds, so it can resolve a name against it the same
	// way the host picker already resolves host names — the roster strip
	// Roster backs (see OncallShiftDTO) has no such list to resolve against.
	Channels  []string  `json:"channels" doc:"Notification channel ids, in cycle order"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// toOncallRotationDTO flattens one oncall.Rotation onto the wire shape every
// rotation route answers with.
func toOncallRotationDTO(r oncall.Rotation) OncallRotationDTO {
	dto := OncallRotationDTO{
		ID: r.ID.String(), Name: r.Name, PeriodDays: r.PeriodDays, Anchor: r.Anchor, Timezone: r.Timezone,
		Channels: make([]string, len(r.Channels)),
		Revision: r.Revision, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	for i, ch := range r.Channels {
		dto.Channels[i] = ch.String()
	}
	return dto
}

// OncallRotationBody is the create/update request body — one shape for both
// verbs, the same full-replace semantics AlertRouteBody's own doc comment
// describes for its sibling resource: a PATCH here replaces the whole
// rotation, including its channel cycle.
type OncallRotationBody struct {
	Name       string    `json:"name,omitempty" minLength:"1" maxLength:"100"`
	PeriodDays int       `json:"period_days,omitempty"`
	Anchor     time.Time `json:"anchor,omitempty" doc:"The first handover; date and time together"`
	Timezone   string    `json:"timezone,omitempty"`
	Channels   []string  `json:"channels,omitempty" doc:"Notification channel ids, in cycle order"`
}

// input turns the wire body into oncall.Input. A malformed channel id is the
// caller's own mistake (400) before this ever reaches oncall.Validate, which
// only ever sees well-formed ids.
func (b OncallRotationBody) input() (oncall.Input, error) {
	channels := make([]uuid.UUID, len(b.Channels))
	for i, c := range b.Channels {
		id, err := parseUUID(c, "channel id")
		if err != nil {
			return oncall.Input{}, err
		}
		channels[i] = id
	}
	return oncall.Input{
		Name: b.Name, PeriodDays: b.PeriodDays, Anchor: b.Anchor, Timezone: b.Timezone, Channels: channels,
	}, nil
}

// OncallShiftDTO is one segment of a rotation's roster
// (oncall.RosterShift). ChannelName comes along with the row from
// oncall.Service.Roster's own join (RotationStore.ChannelNames, resolved in
// the store, not here) — the same reasoning alert_handlers.go's own
// AlertDTO.HostName doc comment gives for the alert list's host name: the
// roster strip this endpoint backs has to print who is on call for an
// arbitrary shift, and resolving a name per segment client-side from a
// paginated channel list cannot answer that. This handler only flattens
// the already-joined row onto the wire, the same way toAlertRouteDTO does
// for its own resource.
type OncallShiftDTO struct {
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	ChannelID   string    `json:"channel_id"`
	ChannelName string    `json:"channel_name"`
	StandIn     bool      `json:"stand_in" doc:"True when this segment came from an exception rather than the cycle itself"`
}

// toOncallShiftDTO shapes one oncall.RosterShift.
func toOncallShiftDTO(s oncall.RosterShift) OncallShiftDTO {
	return OncallShiftDTO{
		StartsAt: s.StartsAt, EndsAt: s.EndsAt, ChannelID: s.ChannelID.String(), ChannelName: s.ChannelName, StandIn: s.StandIn,
	}
}

// OncallExceptionDTO is one oncall.Exception on the wire: a stand-in — for
// this span, this channel instead of whatever the cycle says.
type OncallExceptionDTO struct {
	ID         string    `json:"id"`
	RotationID string    `json:"rotation_id"`
	StartsAt   time.Time `json:"starts_at"`
	EndsAt     time.Time `json:"ends_at"`
	ChannelID  string    `json:"channel_id"`
	Note       string    `json:"note,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// toOncallExceptionDTO flattens one oncall.Exception onto the wire shape
// every exception route answers with.
func toOncallExceptionDTO(e oncall.Exception) OncallExceptionDTO {
	return OncallExceptionDTO{
		ID: e.ID.String(), RotationID: e.RotationID.String(), StartsAt: e.StartsAt, EndsAt: e.EndsAt,
		ChannelID: e.ChannelID.String(), Note: e.Note, CreatedAt: e.CreatedAt,
	}
}

// OncallExceptionBody is the create request body. Exceptions are
// create/delete only (no update) — ValidateException's own rejection of a
// zero-length or inverted span is the only shape-check a stand-in ever
// needs, and there is no revision to replace: covering a different span or
// channel is a new stand-in, the old one deleted (the same immutable-row
// posture audit events already take, with none of an audit event's
// write-once reasons behind it here — it is simply what the design doc's
// own API section asks for).
type OncallExceptionBody struct {
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	ChannelID string    `json:"channel_id"`
	Note      string    `json:"note,omitempty" maxLength:"1000"`
}

// input turns the wire body into oncall.ExceptionInput. A malformed channel
// id is the caller's own mistake (400) before this ever reaches
// oncall.ValidateException.
func (b OncallExceptionBody) input() (oncall.ExceptionInput, error) {
	channelID, err := parseUUID(b.ChannelID, "channel id")
	if err != nil {
		return oncall.ExceptionInput{}, err
	}
	return oncall.ExceptionInput{StartsAt: b.StartsAt, EndsAt: b.EndsAt, ChannelID: channelID, Note: b.Note}, nil
}
