// Mute answers the one question this file exists for: is an alert muted
// right now, and why (design doc "Suppression"). There are three ways to
// reach a mute — a snooze on the alert itself, an operator silence, and a
// maintenance window, the last two both expressed as a maintenance.Window
// with a different Reason — and the design deliberately refuses to let
// each grow its own answer. Mute is the single place all of them are
// compared: no database access, no clock of its own, pure like
// rules.Resolve and items.Decide before it.
//
// The store (a later task) answers "which windows cover this host or
// subject right now", group resolution included — that belongs in SQL.
// Mute only decides, from a's own snooze fields and the occurrences it was
// handed, which of the ones that actually cover now wins.
package alerts

import (
	"bytes"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/maintenance"
)

// MuteReason is why Mute reports an alert as muted. Declaration order is
// the tie-break order the design calls for when two mutes end at the same
// instant — do not reorder these.
type MuteReason string

const (
	MuteMaintenance MuteReason = "maintenance"
	MuteSilence     MuteReason = "silence"
	MuteSnooze      MuteReason = "snooze"
)

// Suppression is Mute's verdict. Until is nil only for an unbounded
// snooze-until-change — every window-backed mute is bounded in time, since
// maintenance.Occurrence itself only ever carries values, not pointers, for
// Start and End. WindowID is nil for a snooze, since a snooze names no
// window.
type Suppression struct {
	Muted    bool
	Reason   MuteReason
	Until    *time.Time
	WindowID *uuid.UUID
}

// muteReasonRank orders MuteReason for the tie-break: the order the const
// block above declares them in, maintenance first. An explicit switch with
// no default: MuteReason is this package's own closed enum, so a value
// reaching here that is none of the three is a bug in Mute itself, not
// something a caller can trigger — and it must panic loudly rather than
// silently rank as maintenance.
func muteReasonRank(r MuteReason) int {
	switch r {
	case MuteMaintenance:
		return 0
	case MuteSilence:
		return 1
	case MuteSnooze:
		return 2
	}
	panic(fmt.Sprintf("alerts: unknown MuteReason %q", r))
}

// muteReasonForWindow maps a maintenance.Reason onto the MuteReason Mute
// reports. maintenance.Reason is a plain string, not a closed enum — the
// type system does not stop a hand-edited row, or a schema this code has
// not caught up with, from carrying a third value — so an unrecognised
// reason reports ok == false and Mute excludes that window entirely,
// rather than ever reading it back as "maintenance" or taking the whole
// call down over one bad row: the same shape items.Decide's own applies
// gives a value its CHECK constraint does not permit
// (internal/items/decide.go).
func muteReasonForWindow(r maintenance.Reason) (reason MuteReason, ok bool) {
	switch r {
	case maintenance.ReasonMaintenance:
		return MuteMaintenance, true
	case maintenance.ReasonSilence:
		return MuteSilence, true
	default:
		return "", false // not a value the schema's CHECK constraint permits; guarded rather than trusted
	}
}

// beats is Mute's own strict total order over applicable Suppressions:
// unbounded (Until == nil) always wins, since it still holds after every
// bounded mute has expired; otherwise the one that ends later wins, since
// "muted until X" is the fact an operator acts on; a tie goes to the
// earlier-declared MuteReason; and finally — for two windows sharing both
// a reason and an end, which only two distinct windows can do — to the
// smaller WindowID, byte for byte. Window ids are unique, so that last
// comparison never ties between two distinct windows; the single possible
// snooze candidate never reaches it, since nothing else shares its reason.
// No two distinct candidates compare equal under this order, so folding it
// over a slice never depends on the order the slice is visited in.
func (s Suppression) beats(other Suppression) bool {
	if (s.Until == nil) != (other.Until == nil) {
		return s.Until == nil
	}
	if s.Until != nil && !s.Until.Equal(*other.Until) {
		return s.Until.After(*other.Until)
	}
	if s.Reason != other.Reason {
		return muteReasonRank(s.Reason) < muteReasonRank(other.Reason)
	}
	var a, b uuid.UUID
	if s.WindowID != nil {
		a = *s.WindowID
	}
	if other.WindowID != nil {
		b = *other.WindowID
	}
	return bytes.Compare(a[:], b[:]) < 0
}

// covers reports whether the occurrence's [Start, End) covers the instant
// now — the same half-open convention maintenance.ActiveAt already uses,
// so a window that ends exactly at now is not active at now.
func covers(w maintenance.Occurrence, now time.Time) bool {
	return !now.Before(w.Start) && now.Before(w.End)
}

// Mute picks the winning mute among a's own snooze and every window in
// windows that actually covers now, with beats as the order — never
// sort.Slice over it, and never a decision that would change if the
// caller listed windows (or a's two snooze fields) in a different order.
func Mute(a Alert, windows []maintenance.Occurrence, now time.Time) Suppression {
	var winner Suppression
	have := false

	consider := func(c Suppression) {
		if !have || c.beats(winner) {
			winner = c
			have = true
		}
	}

	if a.SnoozedUntil != nil && a.SnoozedUntil.After(now) {
		until := *a.SnoozedUntil
		consider(Suppression{Muted: true, Reason: MuteSnooze, Until: &until})
	}
	if a.SnoozeUntilChange {
		consider(Suppression{Muted: true, Reason: MuteSnooze})
	}

	for _, w := range windows {
		if !covers(w, now) {
			continue
		}
		reason, ok := muteReasonForWindow(w.Reason)
		if !ok {
			continue
		}
		end := w.End
		id := w.WindowID
		consider(Suppression{Muted: true, Reason: reason, Until: &end, WindowID: &id})
	}

	return winner
}
