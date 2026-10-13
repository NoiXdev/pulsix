// Package oncall answers one question: which notification channel is on
// call at a given instant (spec docs/superpowers/specs/2026-10-13-v0.4c-
// notification-schedules-design.md). It stores a cycle rather than
// shifts — an ordered channel list, a period and an anchor — so the
// roster never runs out and needs no generator to refill it, and so the
// answer costs the same for any instant, past or future.
//
// A rotation cycles CHANNELS, not people. Pulsix has no notion of a
// person's own address, and inventing one here would commit the shape a
// later per-user inbox has to live with.
package oncall

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Sentinels the API maps to HTTP.
var (
	ErrInvalidInput     = errors.New("invalid input")
	ErrNotFound         = errors.New("not found")
	ErrNameTaken        = errors.New("name already in use")
	ErrRevisionMismatch = errors.New("revision mismatch")
)

// Input is a rotation as an operator supplies it.
type Input struct {
	Name       string
	PeriodDays int
	// Anchor is the FIRST HANDOVER itself, date and time together, not a
	// date with a separate handover-time beside it: two fields would be
	// one fact stored twice, and their first disagreement would have no
	// right answer.
	Anchor   time.Time
	Timezone string
	Channels []uuid.UUID // ordered; position in this slice is the cycle order
}

// Rotation is a stored rotation with its channel list attached.
type Rotation struct {
	ID, OrgID  uuid.UUID
	Name       string
	PeriodDays int
	Anchor     time.Time
	Timezone   string
	Channels   []uuid.UUID
	Revision   int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ExceptionInput is a stand-in as an operator supplies it.
type ExceptionInput struct {
	StartsAt, EndsAt time.Time
	ChannelID        uuid.UUID
	Note             string
}

// Exception is a stored stand-in: for this span, this channel instead of
// whatever the cycle says.
type Exception struct {
	ID, OrgID, RotationID, ChannelID uuid.UUID
	StartsAt, EndsAt                 time.Time
	Note                             string
	CreatedAt                        time.Time
}

// Validate rejects a rotation that cannot answer who is on call.
func Validate(in Input) error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: a rotation needs a name", ErrInvalidInput)
	}
	if in.PeriodDays <= 0 {
		return fmt.Errorf("%w: the period must be at least one day, got %d", ErrInvalidInput, in.PeriodDays)
	}
	if in.Anchor.IsZero() {
		return fmt.Errorf("%w: a rotation needs an anchor — the first handover", ErrInvalidInput)
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return fmt.Errorf("%w: unknown timezone %q", ErrInvalidInput, in.Timezone)
	}
	if len(in.Channels) == 0 {
		return fmt.Errorf("%w: a rotation needs at least one channel", ErrInvalidInput)
	}
	seen := make(map[uuid.UUID]struct{}, len(in.Channels))
	for _, c := range in.Channels {
		if _, dup := seen[c]; dup {
			return fmt.Errorf("%w: channel %v listed twice", ErrInvalidInput, c)
		}
		seen[c] = struct{}{}
	}
	return nil
}

// ValidateException rejects a span that matches no instant. Both a
// zero-length and an inverted span read in the GUI as a working stand-in
// while covering nothing, which is the failure worth refusing at the door.
func ValidateException(in ExceptionInput) error {
	if in.StartsAt.IsZero() || in.EndsAt.IsZero() {
		return fmt.Errorf("%w: a stand-in needs a start and an end", ErrInvalidInput)
	}
	if !in.StartsAt.Before(in.EndsAt) {
		return fmt.Errorf("%w: a stand-in must start before it ends", ErrInvalidInput)
	}
	if in.ChannelID == uuid.Nil {
		return fmt.Errorf("%w: a stand-in needs a channel", ErrInvalidInput)
	}
	return nil
}
