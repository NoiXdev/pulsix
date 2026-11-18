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
// page (GET /hosts/{id}/checks) reports it as `vanished`.
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
