package checks

import (
	"fmt"
	"sort"
	"time"
)

// vanishedMemory is how long VanishedMetrics keeps a metric that is no
// longer reported, and an item that no longer reports a value: the
// manager's longest check interval (internal/checks.MaxInterval; a test
// keeps the two equal). From then on the tuple ages on the manager like
// any other (backlog 3.55).
const vanishedMemory = 24 * time.Hour

// VanishedMetrics remembers, per item, the metrics a plugin reported with
// a value (or a label), so that one which stops being stated while its
// item still reports does not strand on the manager with its last
// reading: temperature's limits, which a sensor may state one round and
// not the next, and smart's per-field metrics, whose JSON field can
// disappear or change type (backlog 3.55). Apply adds "<metric> is no
// longer reported" for each of them instead.
//
// A plugin keeps one per check key (it is a checks.StatefulPlugin), and
// the scheduler collects an instance from one goroutine only, so it holds
// no lock. The zero value is ready to use. The memory lives as long as the
// agent process: a restarted agent starts empty and does not flag what the
// previous one remembered.
type VanishedMetrics struct {
	items map[string]*vanishedItem
}

type vanishedItem struct {
	// lastValue is when the item last reported a value (or a label).
	lastValue time.Time
	// metrics maps each remembered metric to when it was last reported,
	// with a value or as an error observation.
	metrics map[string]time.Time
}

// Apply returns obs with an error observation appended for every
// remembered metric of an item that reports at least one value in obs but
// has no observation at all for that metric; an error observation for it
// counts as reported, since it already says what went wrong. An item with
// no value in obs (absent, or only errors) gets nothing added. A metric
// missing for vanishedMemory is forgotten and no longer flagged, and an
// item whose last value is that old is forgotten with all its metrics.
// The added observations come after obs's own, ordered by item and metric.
func (v *VanishedMetrics) Apply(now time.Time, obs []Observation) []Observation {
	if v.items == nil {
		v.items = make(map[string]*vanishedItem)
	}
	type round struct {
		hasValue bool
		present  map[string]bool
		values   []string
	}
	rounds := make(map[string]*round)
	for _, o := range obs {
		r := rounds[o.Item]
		if r == nil {
			r = &round{present: make(map[string]bool)}
			rounds[o.Item] = r
		}
		r.present[o.Metric] = true
		if o.Err == "" && (o.Value != nil || o.Label != nil) {
			r.hasValue = true
			r.values = append(r.values, o.Metric)
		}
	}

	var missing []Observation
	for item, r := range rounds {
		if !r.hasValue {
			continue
		}
		it := v.items[item]
		if it == nil {
			it = &vanishedItem{metrics: make(map[string]time.Time)}
			v.items[item] = it
		}
		for metric, seen := range it.metrics {
			switch {
			case r.present[metric]:
				it.metrics[metric] = now
			case now.Sub(seen) >= vanishedMemory:
				delete(it.metrics, metric)
			default:
				missing = append(missing, ErrObservation(item, metric, fmt.Errorf("%s is no longer reported", metric)))
			}
		}
		for _, metric := range r.values {
			it.metrics[metric] = now
		}
		it.lastValue = now
	}
	for item, it := range v.items {
		if now.Sub(it.lastValue) >= vanishedMemory {
			delete(v.items, item)
		}
	}

	sort.Slice(missing, func(i, j int) bool {
		if missing[i].Item != missing[j].Item {
			return missing[i].Item < missing[j].Item
		}
		return missing[i].Metric < missing[j].Metric
	})
	return append(obs, missing...)
}
