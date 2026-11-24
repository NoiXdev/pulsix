package rules

import "time"

// Vanished reports whether a current reading taken at `at` has vanished
// (backlog 3.88): the newest reading of its own (host, check key, item),
// itemNewest, is more than three of the check's intervals later — strictly
// more, so a reading exactly three intervals behind is still current.
//
// It answers a different question from staleness. Stale is a reading
// measured against the clock: nothing has arrived for three intervals.
// Vanished is a reading measured against its own item: the check keeps
// running and reporting on the item, just no longer this metric — a
// timing metric a down http probe does not report, a metric a parameter
// switched on and a removed parameter switched off again, or one a
// changed host simply stopped having. Its check_current row stays until
// the metric expiry forgets it (the item expiry period behind its item,
// checks.Service.ForgetExpiredMetrics) or someone forgets it on the host
// page, so without this the row turns stale, the rules turn it unknown and
// the host stays unknown for that whole period, with a fresh sibling
// keeping the item itself from expiring.
//
// Only an error-free reading (a value or a label) counts as its item's
// newest. An error proves the collection ran, not that the item's other
// metrics stopped: a SMART disk smartctl cannot open reports one error on
// smart.passed and nothing else, and its crit reallocated-sector count
// must stay judged — stale, so unknown, its alert open — not vanish and
// close as stale. An error row is itself judged against that newest like
// any other row: a fresh one is never vanished, an old one behind a fresh
// error-free sibling is. An item with nothing but errors has no newest and
// nothing in it vanishes. A whole item that went quiet together has no
// newer reading to trail and is never vanished either — it is stale, and
// unknown, which is the honest answer for a check that stopped running.
// A reading that is its item's newest is never vanished; neither is one
// without a positive interval to measure against.
//
// It is the one implementation of the rule: rule loading (EvalSQLStore's
// Load, through ItemNewest) takes a vanished tuple out of the evaluation
// the way it takes out an item its check no longer asks for, and the host
// page (GET /hosts/{id}/checks) reports it as `vanished`. Rule loading
// alone holds the verdict while the item may be mid-replay
// (ItemNewest.Replaying, withoutVanished): that hold exists to keep alerts
// from closing and reopening, and the page has no alert to protect.
func Vanished(at, itemNewest time.Time, interval time.Duration) bool {
	return interval > 0 && itemNewest.Sub(at) > vanishedFactor*interval
}

// vanishedFactor is how many of a check's intervals a reading may trail its
// item's newest one: the same three staleness allows against the clock,
// for the same reason — one late or partial flush is normal.
const vanishedFactor = stalenessFactor

// ItemNewest is the newest error-free reading per (check key, item) of one
// host's current values, the input Vanished measures a reading against.
type ItemNewest map[ItemKey]time.Time

// Observe records a reading of t taken at `at` that carried the error
// errText. An error reading is not recorded: only a value or a label
// proves the check still reports the item (Vanished's doc comment).
func (n ItemNewest) Observe(t Tuple, at time.Time, errText string) {
	if errText != "" {
		return
	}
	k := itemKeyOf(t)
	if newest, ok := n[k]; !ok || at.After(newest) {
		n[k] = at
	}
}

// Vanished reports whether t's reading taken at `at` has vanished, against
// the newest error-free reading observed for t's item. An item never
// observed without an error has nothing newer, so its reading is not
// vanished.
func (n ItemNewest) Vanished(t Tuple, at time.Time, interval time.Duration) bool {
	newest, ok := n[itemKeyOf(t)]
	return ok && Vanished(at, newest, interval)
}

// Replaying reports whether t's item may still be in the middle of a replay
// (backlog 3.118): its newest error-free reading predates since, the start
// of the host's agent's current online period (HostPresence.
// AgentOnlineSince). An agent back from a gap replays its buffer before it
// sends anything live, in chunks of 200 results, and one collection of an
// item can be split across two chunks. Until the item's first reading of
// the online period arrives, every reading of it the manager holds was
// collected before the agent came back, so the rest of that collection may
// still be in flight — and a metric that trails a sibling from the first
// chunk is not evidence that it vanished.
//
// The online period's start is what the server already records for this:
// the first touch after a gap longer than the agents list's online window
// (agents.StaleAfter) restarts it, and the stream's own opening touch comes
// before the replay. The comparison crosses the agent's clock and the
// database's, like staleness does: an agent clock running behind keeps an
// item replaying until its readings pass the period's start, one running
// ahead by more than the ingest's two-minute plausibility bound has its
// readings refused anyway.
//
// A zero since (no agent, or an unknown start) never replays; neither does
// an item never observed without an error, which has no newest reading.
func (n ItemNewest) Replaying(t Tuple, since time.Time) bool {
	if since.IsZero() {
		return false
	}
	newest, ok := n[itemKeyOf(t)]
	return ok && newest.Before(since)
}

// withoutVanished applies the vanished rule (backlog 3.88) to tuples, in
// place. A vanished tuple — its item reported again since, but not this
// metric, for more than three intervals — leaves the evaluation like an
// item its check no longer asks for: interval zero, so no state, no part in
// the roll-up, and Evaluate reports it as unmeasured, which closes its open
// alerts as stale.
//
// A tuple that would vanish while its item is replaying (backlog 3.118,
// ItemNewest.Replaying: the item has not reported since the agent's online
// period began at onlineSince) is held instead: left out of the snapshot
// altogether, so nothing judges it, nothing reports it unmeasured, its
// stored state and any alert on it stay exactly as they are, and it has no
// part in the roll-up or its siblings' inputs for this pass. That is the
// difference from simply not vanishing it: judged, a client's metric that
// had vanished before the gap would be measured from the online period's
// start (staleAt) and so read its frozen value as fresh — a crit reopening
// for the length of the replay. The item's first reading of the period
// ends the hold and the tuple is judged, vanished or not, as always.
// Tuples the rule leaves alone are untouched; the result shares tuples'
// backing array.
func withoutVanished(tuples []TupleSnapshot, newest ItemNewest, onlineSince time.Time) []TupleSnapshot {
	kept := tuples[:0]
	for _, ts := range tuples {
		if newest.Vanished(ts.Tuple, ts.At, ts.Interval) {
			if newest.Replaying(ts.Tuple, onlineSince) {
				continue
			}
			ts.Interval = 0
		}
		kept = append(kept, ts)
	}
	return kept
}
