package items

import (
	"bytes"
	"time"

	"github.com/google/uuid"
)

// Scope is where a decision applies: the whole organization, or one host.
type Scope string

const (
	ScopeOrg  Scope = "org"
	ScopeHost Scope = "host"
)

// MatchKind is how a decision's Pattern is applied to an item name.
type MatchKind string

const (
	MatchExact MatchKind = "exact"
	MatchGlob  MatchKind = "glob"
)

// Verdict is the Go name for the column the spec and the migration call
// `decision` — the column keeps its own name, because "decision" is the
// word an operator uses, while `Decision` is already taken in Go by the row
// itself. Do not rename either to match the other.
type Verdict string

const (
	Ignore Verdict = "ignore"
	Watch  Verdict = "watch"
)

// Decision is one operator override of how an item's measurements are
// judged: check_item_decisions, the spec's "what a person decided" (as
// opposed to check_items, what the machine found). Decisions and items are
// deliberately decoupled — a decision may name an item that does not exist
// yet, and survives one that disappears — so Decision carries no reference
// to any item row.
type Decision struct {
	ID, OrgID uuid.UUID
	Scope     Scope
	// HostID is nil for Scope == ScopeOrg; set, and compared against a
	// Target's own HostID, for Scope == ScopeHost.
	HostID               *uuid.UUID
	CheckKey             string
	MatchKind            MatchKind
	Pattern              string
	Verdict              Verdict
	Note                 string
	CreatedBy            *uuid.UUID
	CreatedAt, UpdatedAt time.Time
}

// Target names one item a decision can be about: one check, on one host,
// reporting one item name.
type Target struct {
	HostID   uuid.UUID
	CheckKey string
	Item     string
}

// Resolution is what Decide concluded, and the decision that concluded it.
type Resolution struct {
	Verdict Verdict
	// Decision is nil when no decision matched — the default, unoverruled
	// case — and otherwise the one Decide picked.
	Decision *Decision
}

// Ignored is the question the evaluator actually asks: whether this item's
// measurements should be judged this tick.
func (r Resolution) Ignored() bool {
	return r.Verdict == Ignore
}

// scopeRank orders Scope for the precedence table host > org: a higher
// number always outranks a lower one.
func scopeRank(s Scope) int {
	if s == ScopeHost {
		return 1
	}
	return 0
}

// matchKindRank orders MatchKind for the precedence table exact > glob,
// within one scope.
func matchKindRank(m MatchKind) int {
	if m == MatchExact {
		return 1
	}
	return 0
}

// beats is a strict total order over Decision — host beats org, then (within
// one scope) exact beats glob, then the older CreatedAt, then the smaller
// id byte for byte — the same order rules.Resolve's own candidate.beats
// implements and for the same reason: a winner that depended on slice or
// map iteration order would flip between ticks, and the interface could
// never explain itself. No two distinct decisions ever compare equal under
// this order — CreatedAt ties still resolve on ID, which is unique — so a
// maximum computed over it never depends on the order its candidates are
// visited in.
func (d Decision) beats(other Decision) bool {
	if r, o := scopeRank(d.Scope), scopeRank(other.Scope); r != o {
		return r > o
	}
	if r, o := matchKindRank(d.MatchKind), matchKindRank(other.MatchKind); r != o {
		return r > o
	}
	if !d.CreatedAt.Equal(other.CreatedAt) {
		return d.CreatedAt.Before(other.CreatedAt)
	}
	return bytes.Compare(d.ID[:], other.ID[:]) < 0
}

// applies reports whether d is even a candidate for t: its CheckKey matches,
// and — for a ScopeHost decision — its HostID is t's own. A ScopeOrg
// decision applies to every host. Pattern matching is checked separately,
// since it is the same cost either way but a distinct concern (applicability
// vs. specificity).
func (d Decision) applies(t Target) bool {
	if d.CheckKey != t.CheckKey {
		return false
	}
	switch d.Scope {
	case ScopeHost:
		return d.HostID != nil && *d.HostID == t.HostID
	case ScopeOrg:
		return true
	default:
		return false // not a value the schema's CHECK constraint permits; guarded rather than trusted
	}
}

// Decide picks the decision governing t. Host beats org; within a scope,
// exact beats glob; a remaining tie goes to the older CreatedAt, then the
// smaller ID byte for byte — the same strict total order rules.Resolve
// implements, for the same reason. With no match the verdict is Watch and
// Decision is nil — watch as a stored value only ever matters as an
// overrule, since watching is already what happens by default.
//
// Decide filters by CheckKey and by scope applicability (ScopeHost
// decisions only when HostID equals t's) before ranking, and never mutates
// ds — it is called from inside the evaluation path, where the same slice
// is reused across every item of a host.
func Decide(ds []Decision, t Target) Resolution {
	var winner *Decision
	for i := range ds {
		d := ds[i]
		if !d.applies(t) || !Match(d.Pattern, t.Item) {
			continue
		}
		if winner == nil || d.beats(*winner) {
			w := d
			winner = &w
		}
	}
	if winner == nil {
		return Resolution{Verdict: Watch}
	}
	return Resolution{Verdict: winner.Verdict, Decision: winner}
}
