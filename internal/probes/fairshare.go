package probes

import "github.com/google/uuid"

// fairShare decides, for one launch round, which organization may start
// another probe, so that one organization's slow targets (black holes
// under long timeouts) cannot hold every slot while another
// organization's probes wait (final review, Important #5).
//
// Every organization with synthetic checks is guaranteed a reserve:
// MaxConcurrent split evenly between those organizations (at least one
// slot each), but never more than it has checks. A probe within its
// organization's reserve may always start while a slot is free. A probe
// beyond it starts only when the free slots left afterwards still cover
// every other organization's unused reserve, so the slots stay in use
// when the others need few, and the others always find their reserve
// free. The reserves can only all be honoured while at most MaxConcurrent
// organizations have checks; beyond that every organization still gets
// a turn as slots free up, in due order.
//
// A slot that is already held when another organization first gets a
// check is not given back: that organization waits until probes end, at
// most one probe deadline.
type fairShare struct {
	free    int
	held    map[uuid.UUID]int
	reserve map[uuid.UUID]int
	// unused is the sum over all organizations of reserve - held, where
	// positive: slots some organization may claim without warning.
	unused int
}

// newFairShare sizes the reserves from the current targets and counts
// what is in flight (inFlight maps each running slot to its organization).
func newFairShare(maxConcurrent int, targets map[slot]target, inFlight map[slot]uuid.UUID) *fairShare {
	checksPerOrg := map[uuid.UUID]int{}
	for _, t := range targets {
		checksPerOrg[t.org]++
	}
	f := &fairShare{
		free:    maxConcurrent - len(inFlight),
		held:    map[uuid.UUID]int{},
		reserve: make(map[uuid.UUID]int, len(checksPerOrg)),
	}
	for _, org := range inFlight {
		f.held[org]++
	}
	if len(checksPerOrg) == 0 {
		return f
	}
	per := max(1, maxConcurrent/len(checksPerOrg))
	for org, n := range checksPerOrg {
		f.reserve[org] = min(n, per)
		f.unused += max(0, f.reserve[org]-f.held[org])
	}
	return f
}

// full reports whether no slot is free.
func (f *fairShare) full() bool { return f.free <= 0 }

// take reports whether org may start one more probe now and, if so,
// counts it.
func (f *fairShare) take(org uuid.UUID) bool {
	if f.full() {
		return false
	}
	if f.held[org] < f.reserve[org] {
		f.unused--
	} else if f.free-1 < f.unused {
		return false
	}
	f.held[org]++
	f.free--
	return true
}
