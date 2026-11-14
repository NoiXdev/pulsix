package checks

import "sort"

// Local checks are the set the host defines in its own agent.yaml, run
// next to the manager's configuration (backlog 3.5a). The scheduler keeps
// both desired sets and runs their union; a key both sets carry is the
// manager's — the local definition is shadowed and does not run.

// desiredCheck is one entry of the union reconcileLocked installs.
type desiredCheck struct {
	spec  Spec
	local bool
}

// ApplyLocal installs specs as the host's local set, next to the
// manager's. The same rule as Apply holds: a local check that is
// unchanged keeps its timer and phase, a changed one restarts, one that is
// gone stops; the manager's checks are never touched. A local key the
// manager's set also carries does not run and is returned as shadowed,
// sorted. Version and Apply's unrunnable rule are unaffected: they are
// about the manager's set only.
//
// The scheduler owns specs after the call, Params maps included, as Apply
// owns its configuration: callers pass fresh specs on every call
// (secrets.Set.LocalChecks returns deep copies each time).
//
// After Run has returned it does nothing, for the reason Apply gives.
func (s *Scheduler) ApplyLocal(specs []Spec) (shadowed []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return nil
	}
	s.localSpecs = append([]Spec(nil), specs...)
	s.reconcileLocked()
	return s.shadowedLocked()
}

// Shadowed reports the local keys currently not running because the
// manager's set carries the same key, sorted. Apply can change it in
// either direction, so the heartbeat asks every time.
func (s *Scheduler) Shadowed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shadowedLocked()
}

func (s *Scheduler) shadowedLocked() []string {
	manager := s.managerKeysLocked()
	var shadowed []string
	seen := make(map[string]struct{}, len(s.localSpecs))
	for _, spec := range s.localSpecs {
		if _, dup := seen[spec.Key]; dup {
			continue
		}
		seen[spec.Key] = struct{}{}
		if _, ok := manager[spec.Key]; ok {
			shadowed = append(shadowed, spec.Key)
		}
	}
	sort.Strings(shadowed)
	return shadowed
}

func (s *Scheduler) managerKeysLocked() map[string]struct{} {
	keys := make(map[string]struct{}, len(s.managerSpecs))
	for _, spec := range s.managerSpecs {
		keys[spec.Key] = struct{}{}
	}
	return keys
}

// desiredLocked is the union to run: the manager's specs, then the local
// specs whose key the manager's set does not carry. Order is the order of
// first appearance (tickers are created in that order); a key repeated
// within one set takes its last definition. Called with s.mu held.
func (s *Scheduler) desiredLocked() []desiredCheck {
	var desired []desiredCheck
	index := make(map[string]int, len(s.managerSpecs)+len(s.localSpecs))
	add := func(spec Spec, local bool) {
		if i, ok := index[spec.Key]; ok {
			desired[i] = desiredCheck{spec: spec, local: local}
			return
		}
		index[spec.Key] = len(desired)
		desired = append(desired, desiredCheck{spec: spec, local: local})
	}
	for _, spec := range s.managerSpecs {
		add(spec, false)
	}
	manager := s.managerKeysLocked()
	for _, spec := range s.localSpecs {
		if _, ok := manager[spec.Key]; ok {
			continue // shadowed: the manager's check runs
		}
		add(spec, true)
	}
	return desired
}

// reconcileLocked brings s.checks in line with the desired union. A key
// that is gone stops; a new key starts; a key whose definition changed —
// or that moved between the manager's set and the local one — restarts;
// an unchanged key keeps its timer and therefore its phase. Called with
// s.mu held.
func (s *Scheduler) reconcileLocked() {
	desired := s.desiredLocked()
	seen := make(map[string]struct{}, len(desired))
	for _, d := range desired {
		key := d.spec.Key
		seen[key] = struct{}{}
		if existing, ok := s.checks[key]; ok {
			if existing.local == d.local && specsEqual(existing.spec, d.spec) {
				continue // unchanged: the running timer keeps its phase
			}
			s.stopLocked(key)
		}
		s.startLocked(d.spec, d.local)
	}
	for key := range s.checks {
		if _, ok := seen[key]; !ok {
			s.stopLocked(key)
		}
	}
}
