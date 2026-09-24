// The facts service half of the agents service (v0.3a plan, Task 8): the
// implementation of agentapi's FactsSink port. The port is a narrow
// interface declared over in package agentapi — this package satisfies it
// implicitly (agentapi depends on agents, never the reverse) and the app
// wiring passes the service itself as Deps.Facts.
//
// One push does two things in ONE transaction: the reported map merges
// into hosts.facts (jsonb object merge, right side wins), and the three
// automatic tags (os, arch, virt) upsert with source='fact' beneath the
// operator's manual tags. Both halves live in the same transaction so a
// half-applied push (facts merged, tags refused or vice versa) can never
// be observed.

package agents

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// The facts contract's bounds (the mirror of the collector-side
// normalization in internal/agent/facts). A push over them is REJECTED
// with ErrInvalidInput — never silently clipped: the agent's collector
// already normalizes, so a violation is a lying agent, and lying input
// gets a refusal, not a repair.
const (
	// MaxFactsKeys bounds one push; an agent reporting more is broken
	// (or probing).
	MaxFactsKeys = 64
	// MaxFactsValueLen caps every value, measured in BYTES (len on the
	// string, not runes) — the bound exists to keep the stored document
	// small, and bytes are what it costs. The store keeps values verbatim
	// up to it.
	MaxFactsValueLen = 512
)

// factTagKeys are the keys a facts push derives automatic tags from
// (plan Decision: os, arch, virt with source='fact'; every other reported
// key is stored but never tagged).
var factTagKeys = []string{"os", "arch", "virt"}

// RecordFacts merges agent-reported facts into the host's facts and
// re-applies the automatic tags. It implements the agentapi.FactsSink
// port (recorded in Task 7 as the stream's facts target).
//
// Bounds are validated BEFORE the store sees anything: more than
// MaxFactsKeys keys, a value longer than MaxFactsValueLen or an empty key
// is ErrInvalidInput. An empty (or nil) map is a silent no-op — nothing
// to merge, nothing to tag — and short-circuits before the store, so it
// never even resolves the host.
//
// Merge semantics (the store's contract, documented on
// Store.RecordFacts): reported keys REPLACE, unreported keys are KEPT.
// The agent sends the complete set each push, so an unreported key is a
// stale fact from an older agent version — keeping it is the simplest
// honest behavior (deleting would silently drop facts a rolling upgrade
// briefly stopped reporting).
//
// Tag precedence: a manual tag of the same key always wins — the guarded
// upsert writes nothing for that key while the manual row exists. When
// the operator removes the manual tag, the next facts push naturally
// re-applies the fact (the agent pushes on its 6 h cadence and at start).
// A host that does not exist in orgID (or belongs to another
// organization) is ErrNotFound.
func (s *Service) RecordFacts(ctx context.Context, orgID, hostID uuid.UUID, facts map[string]string) error {
	if len(facts) > MaxFactsKeys {
		return fmt.Errorf("%w: at most %d facts per push, got %d", ErrInvalidInput, MaxFactsKeys, len(facts))
	}
	for k, v := range facts {
		if k == "" {
			return fmt.Errorf("%w: empty fact key", ErrInvalidInput)
		}
		if len(v) > MaxFactsValueLen {
			return fmt.Errorf("%w: value for key %q exceeds %d characters", ErrInvalidInput, k, MaxFactsValueLen)
		}
	}
	if len(facts) == 0 {
		return nil
	}
	return s.store.RecordFacts(ctx, orgID, hostID, facts)
}
