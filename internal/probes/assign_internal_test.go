package probes

import (
	"bytes"
	"crypto/sha256"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func ids(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.Must(uuid.NewV7())
	}
	return out
}

// byScore orders agents by their rendezvous score for (target, key),
// highest first: the reference the tests check assign against.
func byScore(target uuid.UUID, key string, agents []uuid.UUID) []uuid.UUID {
	out := slices.Clone(agents)
	slices.SortFunc(out, func(a, b uuid.UUID) int {
		sa, sb := rendezvousScore(target, key, a), rendezvousScore(target, key, b)
		return -bytes.Compare(sa[:], sb[:])
	})
	return out
}

func sortedIDs(in []uuid.UUID) []uuid.UUID {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}

func TestRendezvousScoreIsSHA256OfTargetKeyAgent(t *testing.T) {
	target, agent := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	var buf []byte
	buf = append(buf, target[:]...)
	buf = append(buf, "web"...)
	buf = append(buf, agent[:]...)
	if got, want := rendezvousScore(target, "web", agent), sha256.Sum256(buf); got != want {
		t.Fatalf("score = %x, want %x", got, want)
	}
}

func TestAssignPicksHighestScores(t *testing.T) {
	target := uuid.Must(uuid.NewV7())
	agents := ids(8)
	s := slot{host: target, key: "web"}
	got, short := assign([]demand{{slot: s, groups: [][]uuid.UUID{agents}, perGroup: 3}}, MaxAssignmentsPerAgent)
	want := sortedIDs(byScore(target, "web", agents)[:3])
	if !slices.Equal(got[s], want) || short != 0 {
		t.Fatalf("picked %v (short %d), want the top three by score %v", got[s], short, want)
	}
}

func TestAssignExcludesTheTarget(t *testing.T) {
	agents := ids(4)
	target := agents[0]
	s := slot{host: target, key: "web"}
	got, _ := assign([]demand{{slot: s, groups: [][]uuid.UUID{agents}, perGroup: 10}}, MaxAssignmentsPerAgent)
	if slices.Contains(got[s], target) {
		t.Fatalf("the target host %s probes itself: %v", target, got[s])
	}
	if len(got[s]) != 3 {
		t.Fatalf("picked %v, want the three other agents", got[s])
	}
}

// TestAssignLeavingAgentMovesOnlyItsSlots is Review Focus 2: an agent
// going offline changes only the slots it held; every other slot keeps
// exactly its agents.
func TestAssignLeavingAgentMovesOnlyItsSlots(t *testing.T) {
	agents := ids(10)
	targets := ids(40)
	demands := func(candidates []uuid.UUID) []demand {
		var out []demand
		for _, tg := range targets {
			for _, key := range []string{"web", "ping"} {
				out = append(out, demand{slot: slot{host: tg, key: key}, groups: [][]uuid.UUID{candidates}, perGroup: 3})
			}
		}
		return out
	}
	before, _ := assign(demands(agents), MaxAssignmentsPerAgent)
	gone := agents[4]
	after, _ := assign(demands(slices.Delete(slices.Clone(agents), 4, 5)), MaxAssignmentsPerAgent)
	moved := 0
	for s, was := range before {
		now := after[s]
		if slices.Contains(was, gone) {
			moved++
			kept := slices.DeleteFunc(slices.Clone(was), func(a uuid.UUID) bool { return a == gone })
			for _, a := range kept {
				if !slices.Contains(now, a) {
					t.Fatalf("slot %v lost %s too when only %s left", s, a, gone)
				}
			}
			if len(now) != 3 {
				t.Fatalf("slot %v has %d agents after the move, want 3", s, len(now))
			}
			continue
		}
		if !slices.Equal(was, now) {
			t.Fatalf("slot %v moved from %v to %v though %s never held it", s, was, now, gone)
		}
	}
	if moved == 0 {
		t.Fatal("the leaving agent held no slot; the test proves nothing")
	}
}

func TestAssignTwoGroupsSharingAnAgentCountItOnce(t *testing.T) {
	target := uuid.Must(uuid.NewV7())
	shared := uuid.Must(uuid.NewV7())
	a, b := ids(1), ids(1)
	s := slot{host: target, key: "web"}
	got, _ := assign([]demand{{
		slot: s, groups: [][]uuid.UUID{{shared, a[0]}, {shared, b[0]}}, perGroup: 2,
	}}, MaxAssignmentsPerAgent)
	want := sortedIDs([]uuid.UUID{shared, a[0], b[0]})
	if !slices.Equal(got[s], want) {
		t.Fatalf("picked %v, want each agent once %v", got[s], want)
	}
}

func TestAssignDuplicateMembersCountOnce(t *testing.T) {
	target := uuid.Must(uuid.NewV7())
	agents := ids(2)
	s := slot{host: target, key: "web"}
	got, _ := assign([]demand{{slot: s, groups: [][]uuid.UUID{{agents[0], agents[0], agents[1]}}, perGroup: 3}}, MaxAssignmentsPerAgent)
	if !slices.Equal(got[s], sortedIDs(agents)) {
		t.Fatalf("picked %v, want both agents once", got[s])
	}
}

func TestAssignCapPassesToTheNextCandidate(t *testing.T) {
	agents := ids(2)
	targets := sortedIDs(ids(5))
	var ds []demand
	for _, tg := range targets {
		ds = append(ds, demand{slot: slot{host: tg, key: "web"}, groups: [][]uuid.UUID{agents}, perGroup: 1})
	}
	// Cap 2 with two agents: all five slots ask one each; the first four
	// fit (two per agent), the fifth finds both full and goes short.
	got, short := assign(ds, 2)
	load := map[uuid.UUID]int{}
	for _, as := range got {
		for _, a := range as {
			load[a]++
		}
	}
	for a, n := range load {
		if n > 2 {
			t.Fatalf("agent %s got %d assignments over the cap of 2", a, n)
		}
	}
	if total := load[agents[0]] + load[agents[1]]; total != 4 {
		t.Fatalf("assigned %d slots, want 4 under the cap", total)
	}
	if short != 1 {
		t.Fatalf("short = %d, want 1", short)
	}
	// Processing order is target id then key, so the last target is the
	// short one whatever order the demands came in.
	if n := len(got[slot{host: targets[4], key: "web"}]); n != 0 {
		t.Fatalf("the last slot in order got %d agents, want 0", n)
	}
	reversed := slices.Clone(ds)
	slices.Reverse(reversed)
	again, _ := assign(reversed, 2)
	for s, as := range got {
		if !slices.Equal(as, again[s]) {
			t.Fatalf("slot %v: %v, then %v with the demands reversed", s, as, again[s])
		}
	}
}

func TestAssignDefaultCapIs200(t *testing.T) {
	if MaxAssignmentsPerAgent != 200 {
		t.Fatalf("MaxAssignmentsPerAgent = %d, want 200", MaxAssignmentsPerAgent)
	}
	agent := uuid.Must(uuid.NewV7())
	var ds []demand
	for _, tg := range ids(201) {
		ds = append(ds, demand{slot: slot{host: tg, key: "web"}, groups: [][]uuid.UUID{{agent}}, perGroup: 1})
	}
	got, short := assign(ds, MaxAssignmentsPerAgent)
	n := 0
	for _, as := range got {
		n += len(as)
	}
	if n != 200 || short != 1 {
		t.Fatalf("assigned %d (short %d), want 200 and one short", n, short)
	}
}

func TestAssignNoCandidatesIsNotShort(t *testing.T) {
	s := slot{host: uuid.Must(uuid.NewV7()), key: "web"}
	got, short := assign([]demand{{slot: s, groups: [][]uuid.UUID{nil, ids(1)}, perGroup: 3}}, MaxAssignmentsPerAgent)
	if len(got[s]) != 1 || short != 0 {
		t.Fatalf("picked %v (short %d), want the only candidate and no shortfall from the cap", got[s], short)
	}
}
