// Package schedules runs job templates on a timetable (spec 2026-09-18
// §2): cron or simple mode in an IANA time zone, overlap and catch-up
// guards, pause, per-schedule overrides, and a ticker that decides every
// due occurrence exactly once across managers (FOR UPDATE SKIP LOCKED).
package schedules

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/templates"
)

// Sentinels the API maps to HTTP.
var (
	ErrNotFound         = errors.New("not found")
	ErrInvalidInput     = errors.New("invalid input")
	ErrNameTaken        = errors.New("name already in use")
	ErrRevisionMismatch = errors.New("revision mismatch")
)

// Mode is how the timetable was entered.
type Mode string

const (
	ModeCron   Mode = "cron"
	ModeSimple Mode = "simple"
)

// Overlap is the guard for a due run while a run of the template is active.
type Overlap string

const (
	OverlapSkip  Overlap = "skip"
	OverlapQueue Overlap = "queue"
)

// CatchUp lets one late run happen after downtime, inside Window.
type CatchUp struct {
	Enabled bool   `json:"enabled"`
	Window  string `json:"window"`
}

// Guards is the schedules.guards jsonb. v0.2d adds a maintenance guard;
// unknown keys never reach the database because the API decodes into
// this struct.
type Guards struct {
	Overlap Overlap `json:"overlap"`
	CatchUp CatchUp `json:"catch_up"`
}

// Outcome is what the ticker (or run now) decided for a due occurrence.
type Outcome string

const (
	OutcomeTriggered      Outcome = "triggered"
	OutcomeSkippedOverlap Outcome = "skipped_overlap"
	OutcomeMissed         Outcome = "missed"
	OutcomeFailed         Outcome = "failed"
)

// Schedule is one stored schedule. Overrides are in their storage form:
// secret values sealed with AAD(OrgID, ID).
type Schedule struct {
	ID, OrgID, TemplateID uuid.UUID
	Name                  string
	Mode                  Mode
	Cron                  string
	Simple                *cronexpr.Simple
	Timezone              string
	Guards                Guards
	Overrides             templates.Overrides
	Paused                bool
	NextRunAt, LastRunAt  *time.Time
	CreatedBy             *uuid.UUID
	CreatedAt, UpdatedAt  time.Time
	Revision              int64
}

// Input is what an operator submits for create and update.
type Input struct {
	TemplateID uuid.UUID
	Name       string
	Mode       Mode
	Cron       string
	Simple     *cronexpr.Simple
	Timezone   string
	Guards     Guards
	Overrides  templates.Overrides
}

// Run is one recorded decision.
type Run struct {
	ID, OrgID, ScheduleID uuid.UUID
	DueAt, DecidedAt      time.Time
	Outcome               Outcome
	Reason                string
	JobGroupID            *uuid.UUID
}

// PreviewInput and Preview back the editor's "next 5 occurrences".
type PreviewInput struct {
	Mode     Mode
	Cron     string
	Simple   *cronexpr.Simple
	Timezone string
}

type Preview struct {
	Cron        string
	Description string
	Next        []time.Time
}

// AAD is the associated data a schedule's secret override values are
// sealed with.
func AAD(orgID, id uuid.UUID) []byte {
	return []byte("schedule:" + orgID.String() + ":" + id.String())
}
