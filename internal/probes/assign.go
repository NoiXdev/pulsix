package probes

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"slices"

	"github.com/google/uuid"
)

// MaxAssignmentsPerAgent bounds the slots one agent probes (spec
// 2026-11-16-probe-vantages-design.md, "Assignment").
const MaxAssignmentsPerAgent = 200

// demand is one vantage slot's ask: per vantage group, the group's
// eligible agents (online, probe capable, existing hosts), and how many
// each group picks. The target host may be among them; assign skips it.
type demand struct {
	slot     slot
	groups   [][]uuid.UUID
	perGroup int
}

// rendezvousScore is an agent's weight for a slot: sha256(target host id
// bytes ‖ check key ‖ agent host id bytes). Higher wins.
func rendezvousScore(target uuid.UUID, key string, agent uuid.UUID) [sha256.Size]byte {
	buf := make([]byte, 0, 2*len(uuid.UUID{})+len(key))
	buf = append(buf, target[:]...)
	buf = append(buf, key...)
	buf = append(buf, agent[:]...)
	return sha256.Sum256(buf)
}

// assign picks every slot's agents by rendezvous hashing: per group, the
// perGroup eligible agents with the highest score, never the target
// itself; an agent picked by two groups of a slot probes it once and
// counts toward both. No agent gets more than limit slots: an agent at
// the limit is passed over for the next candidate by score. Slots are
// taken in a fixed order (target host id, then check key), so which slot
// the limit cuts short does not depend on the order of demands.
//
// It returns each slot's agents sorted by id, and how many slots the
// limit left with fewer agents than their groups would have given.
// Rendezvous hashing keeps the answer stable: an agent leaving changes
// only the slots it held (as long as no agent is at the limit).
func assign(demands []demand, limit int) (map[slot][]uuid.UUID, int) {
	ordered := slices.Clone(demands)
	slices.SortFunc(ordered, func(a, b demand) int {
		return cmp.Or(bytes.Compare(a.slot.host[:], b.slot.host[:]), cmp.Compare(a.slot.key, b.slot.key))
	})
	load := map[uuid.UUID]int{}
	out := make(map[slot][]uuid.UUID, len(ordered))
	short := 0
	for _, d := range ordered {
		chosen := map[uuid.UUID]bool{}
		capped := false
		for _, members := range d.groups {
			picked, passed := 0, false
			for _, c := range rank(d.slot, members) {
				if picked == d.perGroup {
					break
				}
				if !chosen[c] && load[c] >= limit {
					passed = true
					continue
				}
				if !chosen[c] {
					chosen[c] = true
					load[c]++
				}
				picked++
			}
			if passed && picked < d.perGroup {
				capped = true
			}
		}
		if capped {
			short++
		}
		agents := make([]uuid.UUID, 0, len(chosen))
		for a := range chosen {
			agents = append(agents, a)
		}
		slices.SortFunc(agents, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
		out[d.slot] = agents
	}
	return out, short
}

// rank is a group's candidates for s, highest score first: each member
// once, the target host left out.
func rank(s slot, members []uuid.UUID) []uuid.UUID {
	type scored struct {
		id    uuid.UUID
		score [sha256.Size]byte
	}
	seen := make(map[uuid.UUID]bool, len(members))
	cands := make([]scored, 0, len(members))
	for _, m := range members {
		if m == s.host || seen[m] {
			continue
		}
		seen[m] = true
		cands = append(cands, scored{id: m, score: rendezvousScore(s.host, s.key, m)})
	}
	slices.SortFunc(cands, func(a, b scored) int { return bytes.Compare(b.score[:], a.score[:]) })
	out := make([]uuid.UUID, len(cands))
	for i, c := range cands {
		out[i] = c.id
	}
	return out
}
