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
	// MaxFactsKeyLen bounds a key's length; together with the [a-z0-9_]
	// alphabet (validFactKey) it is the collector's key contract
	// (internal/agent/facts MaxKeyLen).
	MaxFactsKeyLen = 32
	// MaxStoredFacts caps the keys one host keeps across pushes: the
	// store merges a push into the stored facts only while the result
	// stays at or under it, and otherwise replaces the stored set with
	// the push (UpsertHostFacts). Twice MaxFactsKeys leaves room for the
	// stale keys a rolling agent upgrade leaves behind.
	MaxStoredFacts = 128
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
// MaxFactsKeys keys, a value longer than MaxFactsValueLen, an empty key or
// a key outside the collector's contract (more than MaxFactsKeyLen
// characters, or any character outside [a-z0-9_]) is ErrInvalidInput. An
// empty (or nil) map is a silent no-op — nothing to merge, nothing to
// tag — and short-circuits before the store, so it never even resolves
// the host.
//
// Merge semantics (the store's contract, documented on
// Store.RecordFacts): reported keys REPLACE, unreported keys are KEPT.
// The agent sends the complete set each push, so an unreported key is a
// stale fact from an older agent version — keeping it is the simplest
// honest behavior (deleting would silently drop facts a rolling upgrade
// briefly stopped reporting). Kept only up to MaxStoredFacts keys: a push
// whose merge would exceed it replaces the stored set instead, so keys
// never accumulate without bound across pushes (backlog 7.56).
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
		if !validFactKey(k) {
			// The key is not echoed: unchecked, it is bounded only by the
			// gRPC message limit.
			return fmt.Errorf("%w: a fact key (%d bytes) is not 1 to %d characters of [a-z0-9_]", ErrInvalidInput, len(k), MaxFactsKeyLen)
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

// validFactKey reports whether key is 1..MaxFactsKeyLen characters of
// [a-z0-9_] — the shape the collector's normalization guarantees. Bytes
// are checked one by one, so any non-ASCII rune fails.
func validFactKey(key string) bool {
	if key == "" || len(key) > MaxFactsKeyLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}
