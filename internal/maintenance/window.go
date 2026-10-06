// Package maintenance holds maintenance windows (spec 2026-09-18 §6): a
// host, an inventory group or the whole organization is in maintenance
// either once (starts_at to ends_at) or on a recurring timetable (a cron
// expression in a time zone, each occurrence lasting a duration). v0.2d
// uses them for the schedules' maintenance guard and the calendar; v0.4
// adds monitoring-group targets and alert silencing.
package maintenance

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Sentinels the API maps to HTTP.
var (
	ErrNotFound         = errors.New("not found")
	ErrInvalidInput     = errors.New("invalid input")
	ErrNameTaken        = errors.New("name already in use")
	ErrRevisionMismatch = errors.New("revision mismatch")
)

// TargetKind is what a window covers.
type TargetKind string

const (
	TargetHost            TargetKind = "host"
	TargetGroup           TargetKind = "inventory_group"
	TargetMonitoringGroup TargetKind = "monitoring_group"
	TargetOrg             TargetKind = "org"
)

// Reason is why a window exists: a plain maintenance window, or the face
// v0.4a gives alert silencing — a one-off window with internal visibility
// and Reason = ReasonSilence, reusing targets, overlap handling, RLS, API
// and GUI instead of standing up a parallel subsystem.
type Reason string

const (
	ReasonMaintenance Reason = "maintenance"
	ReasonSilence     Reason = "silence"
)

// Window is one stored maintenance window. A one-off window has StartsAt
// and EndsAt; a recurring one has Cron, Duration and Timezone.
type Window struct {
	ID, OrgID  uuid.UUID
	Name       string
	TargetKind TargetKind
	// TargetID is the host, inventory group or monitoring group; nil for
	// the organization.
	TargetID *uuid.UUID
	// TargetName is the host's or group's name, read with the window.
	TargetName           string
	StartsAt, EndsAt     *time.Time
	Cron                 string
	Duration             time.Duration
	Timezone             string
	Note                 string
	Reason               Reason
	CreatedBy            *uuid.UUID
	CreatedAt, UpdatedAt time.Time
	Revision             int64
}

// Recurring reports whether the window repeats on a timetable.
func (w Window) Recurring() bool { return w.Cron != "" }

// DurationText is Duration without trailing zero units ("1h30m", "2h").
func (w Window) DurationText() string {
	if w.Duration == 0 {
		return ""
	}
	s := w.Duration.String()
	s = strings.TrimSuffix(s, "0s")
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// Input is what an operator submits for create and update. Duration is a
// Go-style duration such as "2h" or "90m".
type Input struct {
	Name             string
	TargetKind       TargetKind
	TargetID         *uuid.UUID
	StartsAt, EndsAt *time.Time
	Cron             string
	Duration         string
	Timezone         string
	Note             string
	// Reason is empty for a plain maintenance window; normalize defaults
	// it to ReasonMaintenance.
	Reason Reason
}

// Occurrence is one concrete stretch of a window, for the calendar.
type Occurrence struct {
	WindowID   uuid.UUID
	Name       string
	TargetKind TargetKind
	TargetID   *uuid.UUID
	TargetName string
	Reason     Reason
	Start, End time.Time
}
