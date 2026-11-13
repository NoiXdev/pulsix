// package checks: VanishedMetrics' unit tests (vanished.go).
package checks

import (
	"errors"
	"reflect"
	"testing"
	"time"

	managerchecks "github.com/noixdev/pulsix/internal/checks"
)

var vanishedT0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func val(item, metric string, v float64) Observation { return ValueObservation(item, metric, v) }

func errObs(item, metric, text string) Observation {
	return ErrObservation(item, metric, errors.New(text))
}

// added is what Apply appended to the round it was handed.
func added(t *testing.T, round, got []Observation) []Observation {
	t.Helper()
	if len(got) < len(round) || !reflect.DeepEqual(got[:len(round)], round) {
		t.Fatalf("Apply changed the round's own observations: got %+v, round %+v", got, round)
	}
	return got[len(round):]
}

func TestVanishedMemoryIsTheManagersMaxInterval(t *testing.T) {
	if vanishedMemory != managerchecks.MaxInterval {
		t.Fatalf("vanishedMemory = %s, the manager's MaxInterval is %s", vanishedMemory, managerchecks.MaxInterval)
	}
}

// A metric reported with a value and then missing while its item still
// reports a value gets one error observation per round, until it has been
// missing for 24 h; then it is forgotten.
func TestVanishedMetricReportedWhileMissing(t *testing.T) {
	var v VanishedMetrics
	first := []Observation{val("a", "m.value", 40), val("a", "m.limit", 90), val("", "m.count", 1)}
	if got := added(t, first, v.Apply(vanishedT0, first)); len(got) != 0 {
		t.Fatalf("first round added %+v, want nothing", got)
	}

	round := []Observation{val("a", "m.value", 41), val("", "m.count", 1)}
	want := []Observation{errObs("a", "m.limit", "m.limit is no longer reported")}
	for _, after := range []time.Duration{time.Minute, 2 * time.Minute, vanishedMemory - time.Second} {
		if got := added(t, round, v.Apply(vanishedT0.Add(after), round)); !reflect.DeepEqual(got, want) {
			t.Fatalf("after %s: added %+v, want %+v", after, got, want)
		}
	}
	for _, after := range []time.Duration{vanishedMemory, vanishedMemory + time.Minute} {
		if got := added(t, round, v.Apply(vanishedT0.Add(after), round)); len(got) != 0 {
			t.Fatalf("after %s: added %+v, want nothing (forgotten after 24 h)", after, got)
		}
	}
}

// A round in which the item reports nothing, or only errors, adds nothing
// for it, and does not count as the metric being back either.
func TestVanishedItemWithoutValuesIsLeftAlone(t *testing.T) {
	var v VanishedMetrics
	v.Apply(vanishedT0, []Observation{val("a", "m.value", 40), val("a", "m.limit", 90), val("b", "m.value", 1)})

	for i, round := range [][]Observation{
		{val("b", "m.value", 1)},                // a absent
		{errObs("a", "m.value", "read failed")}, // a only errors
		{errObs("a", "m.value", "x"), val("b", "m.value", 1)},
		nil, // nothing at all
	} {
		if got := added(t, round, v.Apply(vanishedT0.Add(time.Duration(i+1)*time.Minute), round)); len(got) != 0 {
			t.Fatalf("round %d added %+v, want nothing", i, got)
		}
	}
	round := []Observation{val("a", "m.value", 40)}
	want := []Observation{errObs("a", "m.limit", "m.limit is no longer reported")}
	if got := added(t, round, v.Apply(vanishedT0.Add(time.Hour), round)); !reflect.DeepEqual(got, want) {
		t.Fatalf("added %+v, want %+v", got, want)
	}
}

// A metric that arrives as an error observation is not missing: the
// plugin already reports what went wrong with it.
func TestVanishedErrorObservationIsNotMissing(t *testing.T) {
	var v VanishedMetrics
	v.Apply(vanishedT0, []Observation{val("a", "m.value", 40), val("a", "m.limit", 90)})
	round := []Observation{val("a", "m.value", 40), errObs("a", "m.limit", "limit unreadable")}
	for _, after := range []time.Duration{time.Minute, vanishedMemory + time.Hour} {
		if got := added(t, round, v.Apply(vanishedT0.Add(after), round)); len(got) != 0 {
			t.Fatalf("after %s: added %+v, want nothing", after, got)
		}
	}
	// It stays remembered while it is reported as an error: missing later,
	// it is flagged.
	later := []Observation{val("a", "m.value", 40)}
	want := []Observation{errObs("a", "m.limit", "m.limit is no longer reported")}
	if got := added(t, later, v.Apply(vanishedT0.Add(vanishedMemory+2*time.Hour), later)); !reflect.DeepEqual(got, want) {
		t.Fatalf("added %+v, want %+v", got, want)
	}
}

// A metric that comes back stops the error; one that goes again is
// flagged again.
func TestVanishedMetricBack(t *testing.T) {
	var v VanishedMetrics
	full := []Observation{val("a", "m.value", 40), val("a", "m.limit", 90)}
	short := []Observation{val("a", "m.value", 40)}
	v.Apply(vanishedT0, full)
	if got := added(t, short, v.Apply(vanishedT0.Add(time.Minute), short)); len(got) != 1 {
		t.Fatalf("missing: added %+v, want one error", got)
	}
	if got := added(t, full, v.Apply(vanishedT0.Add(2*time.Minute), full)); len(got) != 0 {
		t.Fatalf("back: added %+v, want nothing", got)
	}
	if got := added(t, short, v.Apply(vanishedT0.Add(3*time.Minute), short)); len(got) != 1 {
		t.Fatalf("missing again: added %+v, want one error", got)
	}
}

// An item whose last value is 24 h old is forgotten with everything it
// had reported, so items that disappear do not grow the memory.
func TestVanishedItemGoneIsDropped(t *testing.T) {
	var v VanishedMetrics
	v.Apply(vanishedT0, []Observation{val("a", "m.value", 40), val("a", "m.limit", 90), val("b", "m.value", 1)})
	v.Apply(vanishedT0.Add(time.Hour), []Observation{errObs("a", "m.value", "x"), val("b", "m.value", 1)})
	v.Apply(vanishedT0.Add(vanishedMemory), []Observation{val("b", "m.value", 1)})
	if _, ok := v.items["a"]; ok {
		t.Fatalf("item a still remembered after 24 h without a value: %+v", v.items)
	}
	if _, ok := v.items["b"]; !ok {
		t.Fatal("item b forgotten although it reports")
	}
	// Back after that, a starts over: its old limit is not flagged.
	round := []Observation{val("a", "m.value", 40)}
	if got := added(t, round, v.Apply(vanishedT0.Add(vanishedMemory+time.Minute), round)); len(got) != 0 {
		t.Fatalf("added %+v, want nothing for an item that starts over", got)
	}
	// A forgotten metric leaves the memory too.
	v.Apply(vanishedT0.Add(3*vanishedMemory), []Observation{val("b", "m.value", 1)})
	if len(v.items) != 1 || len(v.items["b"].metrics) != 1 {
		t.Fatalf("memory = %+v, want only b's one metric", v.items)
	}
}

// Several missing metrics on several items are each flagged once, in a
// stable order; labels count as values.
func TestVanishedSeveralMetricsAndLabels(t *testing.T) {
	var v VanishedMetrics
	v.Apply(vanishedT0, []Observation{
		val("b", "m.z", 1), val("b", "m.a", 2), LabelObservation("b", "m.state", "ok"),
		val("a", "m.y", 3), val("a", "m.x", 4),
	})
	round := []Observation{LabelObservation("b", "m.state", "ok"), val("a", "m.x", 4)}
	want := []Observation{
		errObs("a", "m.y", "m.y is no longer reported"),
		errObs("b", "m.a", "m.a is no longer reported"),
		errObs("b", "m.z", "m.z is no longer reported"),
	}
	if got := added(t, round, v.Apply(vanishedT0.Add(time.Minute), round)); !reflect.DeepEqual(got, want) {
		t.Fatalf("added %+v, want %+v", got, want)
	}
}
