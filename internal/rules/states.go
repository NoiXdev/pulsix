// This file gives the state machine somewhere to put its conclusions: the
// four states a tuple can be in, the strict order the fleet roll-up needs
// over them, and the shapes and Store this package persists them through.
// Nothing here decides a state — Evaluate (Task 8) is the pure function
// that does, and EvalStore (Task 9) is what writes these tables; this file
// and states_store.go only store and read back what those later tasks
// produce.
package rules

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// State is one tuple's judged condition.
type State string

const (
	StateOK      State = "ok"
	StateWarn    State = "warn"
	StateCrit    State = "crit"
	StateUnknown State = "unknown"
)

// stateRank orders State for Worse: a higher number always outranks a
// lower one. unknown ranks below warn deliberately (the design's own
// wording): a known problem is more urgent than an unknown one, so one
// expired mount cannot mask a real crit elsewhere on the same host.
func stateRank(s State) int {
	switch s {
	case StateCrit:
		return 3
	case StateWarn:
		return 2
	case StateUnknown:
		return 1
	default: // StateOK
		return 0
	}
}

// Worse orders states for the roll-up: crit > warn > unknown > ok. A known
// problem outranks an unknown one, so one expired mount cannot mask a crit.
func Worse(a, b State) State {
	if stateRank(a) >= stateRank(b) {
		return a
	}
	return b
}

// TupleState is the current state of one (host, check_key, item, metric)
// tuple: what it is now (State, Since), what decided it (RuleID, the last
// observed value or label), and — when a for_duration rule is waiting for
// a deterioration to hold — the half-ripened Candidate it has not yet
// promoted to State. Candidate is the zero State ("") and CandidateSince is
// nil together, exactly like check_states' own CHECK ties the two columns:
// a candidate is either fully present or fully absent, never one without
// the other.
type TupleState struct {
	Tuple          Tuple
	State          State
	Since          time.Time
	RuleID         *uuid.UUID
	LastValue      *float64
	LastLabel      *string
	Candidate      State
	CandidateSince *time.Time
	Flapping       bool
	EvaluatedAt    time.Time
}

// Change is one row of check_state_changes: an actual transition, not a
// tick's outcome. From and To are always different — the schema's own
// CHECK (from_state <> to_state) — so a Change on this side always
// represents a real move.
type Change struct {
	At       time.Time
	Tuple    Tuple
	From, To State
	RuleID   *uuid.UUID
	Value    *float64
	Label    *string
}

// HostState is one host's roll-up: its worst tuple state, the counts a
// fleet card renders, and when that worst state started.
type HostState struct {
	HostID              uuid.UUID
	State               State
	Crit, Warn, Unknown int
	Since, EvaluatedAt  time.Time
}

// FleetFilter narrows HostStates. The zero FleetFilter matches every host
// in the organization.
//
//   - ZoneID, when set, matches hosts in that zone alone (hosts.zone_id).
//   - Tag, when non-empty, matches hosts carrying that tag: "key=value"
//     for an exact tag match, or a bare key (no "=") for "this key is set
//     to anything" — the same two conditions internal/hosts/selector calls
//     opEq and opExists, expressed directly here rather than through that
//     package's full grammar, since one tag filter needs neither "and"
//     composition nor "in" lists. A manual tag overriding a fact tag on
//     the same key is resolved the same way host_tags' own merged view
//     resolves it everywhere else (see internal/hosts.SQLStore.QueryHosts'
//     own doc comment for why that merge cannot happen inside the SQL
//     fragment alone).
//   - GroupID, when set, matches hosts the named monitoring group's
//     selector currently matches — the very same membership the rule
//     precedence resolver (Resolve, resolve.go) and the check pipeline
//     both already use, read through internal/hosts/selector against the
//     group's own stored selector text, never a second notion of
//     membership.
type FleetFilter struct {
	ZoneID  *uuid.UUID
	GroupID *uuid.UUID
	Tag     string
}

// StateStore is the read side of state storage (org-scoped). Nothing here
// writes: the evaluator's own store (EvalStore, Task 9) owns every write to
// check_states, check_state_changes and check_host_states, so a caller that
// only ever reads — the API layer — depends on this narrower interface
// instead of the write-capable one.
type StateStore interface {
	// StatesForHost returns hostID's current state per tuple, ordered by
	// (check_key, item, metric). A tuple never evaluated has no row at all
	// — this never invents an StateOK placeholder for one, the same way
	// check_current never invents a row for a metric that was never
	// sampled.
	StatesForHost(ctx context.Context, orgID, hostID uuid.UUID) ([]TupleState, error)
	// ChangeCounts returns, per tuple, how many times it changed at or
	// after since — the flapping detector's own input. A tuple with no
	// row in the result changed zero times in the window, not an error.
	ChangeCounts(ctx context.Context, orgID, hostID uuid.UUID, since time.Time) (map[Tuple]int, error)
	// HostStates returns the fleet's per-host roll-up matching f, one row
	// per host that has ever been evaluated. A host with no
	// check_host_states row (never evaluated) is not synthesized here —
	// see FleetFilter's own doc comment for what each field selects.
	HostStates(ctx context.Context, orgID uuid.UUID, f FleetFilter) ([]HostState, error)
}
