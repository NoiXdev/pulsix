package probes

import "github.com/google/uuid"

// fairShare decides, for one launch round, which organization may start
// another probe, so that one organization's slow targets (black holes
// under long timeouts) cannot keep another organization's due probes
// from starting (final review, Important #5).
//
// Only a busy organization, one with due or in-flight probes, has a
// reserve: MaxConcurrent split evenly between the busy organizations (at
// least one slot each), but never more than its due and in-flight probes
// together. An idle organization reserves nothing, so a lone busy one may
// use every slot. A probe within its organization's reserve starts while a
// slot is free; one beyond it starts only when the free slots left
// afterwards still cover every other busy organization's unused reserve.
//
// What this does not do: it never stops a running probe. An organization
// that falls due while another holds every slot (because it was idle when
// they were taken) gets the next slot that frees up, and a launch round
// runs on every completion, so the wait is bounded by the other's
// shortest running probe. The reserves only add up to at most
// MaxConcurrent while at most MaxConcurrent organizations are busy; with
// more, each still gets slots in due order as they free up, without a
// guaranteed share.
type fairShare struct {
	free    int
	held    map[uuid.UUID]int
	reserve map[uuid.UUID]int
	// unused is the sum over all organizations of reserve - held, where
	// positive: slots some organization may claim without warning.
	unused int
}

// newFairShare sizes the reserves from each organization's due probes
// (dueByOrg, not counting the in-flight ones) and what is in flight
// (inFlight maps each running slot to its organization).
func newFairShare(maxConcurrent int, dueByOrg map[uuid.UUID]int, inFlight map[slot]uuid.UUID) *fairShare {
	f := &fairShare{
		free:    maxConcurrent - len(inFlight),
		held:    map[uuid.UUID]int{},
		reserve: map[uuid.UUID]int{},
	}
	for _, org := range inFlight {
		f.held[org]++
	}
	demand := make(map[uuid.UUID]int, len(dueByOrg)+len(f.held))
	for org, n := range dueByOrg {
		demand[org] += n
	}
	for org, n := range f.held {
		demand[org] += n
	}
	if len(demand) == 0 {
		return f
	}
	per := max(1, maxConcurrent/len(demand))
	for org, n := range demand {
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
