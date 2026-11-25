package api

import (
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/rules"
)

// stillAsked drops the current values whose item d no longer asks the host
// for — a mount taken off a disk check's list, a unit taken off a service
// check's (backlog 3.28) — and those whose metric it no longer asks for,
// http.body_match once body_contains is removed (backlog 3.88), or not
// while the item reads what its current labels say (backlog 3.74: every
// zfs metric but zfs.state of a pool that reads EXPORTED or MISSING, back
// once it reads another state; checks.CurrentLabels over values). Nothing
// deletes a check_current row, deliberately (checks.Current's doc comment
// and migration 00044 both say why), so without this the dropped item sits
// on the host page for ever and is eventually badged stale — which reads
// as "the agent stopped reporting this" when the truth is "you stopped
// asking for it".
//
// A definition that bounds no items keeps every value: checks.AsksForItem
// carries that rule, including the disk check with no mounts list, which
// means every filesystem the host finds. Only the current view is
// filtered; GET /hosts/{id}/metrics still serves the dropped item's
// samples, because a graph of a mount dropped last week is a real graph.
//
// A metric not asked only because of its item's present state is kept,
// held (heldOn, checks.Definition.HeldOn; backlog 3.9b ruling): the
// evaluation holds its state and alert as they stood, so the page shows
// its last value and verdict, marked with the state it waits on, rather
// than hiding a verdict the host's roll-up still counts. A reading taken
// after the item's state is asked against no state at all
// (checks.ItemLabels.For), as the evaluation does.
func stillAsked(asksItem func(item string) bool, asksMetric func(labels map[string]string, metric string) bool,
	heldOn func(labels map[string]string, metric string) string, values []checks.Current) []askedValue {
	labels := checks.CurrentLabels(values)
	kept := make([]askedValue, 0, len(values))
	for _, v := range values {
		if !asksItem(v.Item) {
			continue
		}
		itemLabels := labels.For(v)
		if asksMetric(itemLabels, v.Metric) {
			kept = append(kept, askedValue{Current: v})
		} else if on := heldOn(itemLabels, v.Metric); on != "" {
			kept = append(kept, askedValue{Current: v, heldOn: on})
		}
	}
	return kept
}

// askedValue is a current value the check still asks for, or one held on
// its item's state, heldOn naming that state (stillAsked).
type askedValue struct {
	checks.Current
	heldOn string
}

// heldAsEvaluated keeps a held value only where the evaluation holds it
// (rules' held.go): a disabled check has no interval, so its values are
// unmeasured there — their alerts closed stale — and a state row marked
// unmeasured since its last judgement (backlog 4.42: the check was off, or
// the item off its list, while the item was in its state) holds nothing.
// Either is a value nobody asks for, so it is left out like one, rather
// than shown as a verdict that waits for the item while the roll-up and
// the alerts no longer count it.
func heldAsEvaluated(key string, enabled bool, values []askedValue, states map[rules.Tuple]rules.TupleState) []askedValue {
	kept := values[:0:0]
	for _, v := range values {
		if v.heldOn != "" {
			st, ok := states[rules.Tuple{CheckKey: key, Item: v.Item, Metric: v.Metric}]
			if !enabled || (ok && st.UnmeasuredSinceJudged()) {
				continue
			}
		}
		kept = append(kept, v)
	}
	return kept
}
