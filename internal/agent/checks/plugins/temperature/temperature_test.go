// package temperature: the tests run against a fixture provider, never the
// machine's real sensors.
package temperature

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/shirou/gopsutil/v4/sensors"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

type fixtureSensors struct {
	stats []sensors.TemperatureStat
	err   error
}

func (f fixtureSensors) Temperatures(context.Context) ([]sensors.TemperatureStat, error) {
	return f.stats, f.err
}

func collect(t *testing.T, f fixtureSensors, params map[string]any) ([]checks.Observation, error) {
	t.Helper()
	return temperaturePlugin{Sensors: f}.Collect(context.Background(), params)
}

func TestName(t *testing.T) {
	if got := New().Name(); got != "temperature" {
		t.Fatalf("Name() = %q", got)
	}
}

func TestAllSensorsWithLimits(t *testing.T) {
	obs, err := collect(t, fixtureSensors{stats: []sensors.TemperatureStat{
		{SensorKey: "coretemp_core_0", Temperature: 54.5, High: 100, Critical: 105},
		{SensorKey: "nvme_composite", Temperature: 0, High: 0, Critical: 0},
	}}, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs) != 5 {
		t.Fatalf("got %d observations, want 5: %+v", len(obs), obs)
	}
	sensorCount(t, obs, 2)
	if o := findObs(t, obs, "coretemp_core_0", "temp.celsius"); o.Value == nil || *o.Value != 54.5 {
		t.Errorf("temp.celsius = %v", o.Value)
	}
	if o := findObs(t, obs, "coretemp_core_0", "temp.high_celsius"); o.Value == nil || *o.Value != 100 {
		t.Errorf("high = %v", o.Value)
	}
	if o := findObs(t, obs, "coretemp_core_0", "temp.critical_celsius"); o.Value == nil || *o.Value != 105 {
		t.Errorf("critical = %v", o.Value)
	}
	// A reading of 0 is still a reading; zero limits mean "not stated".
	if o := findObs(t, obs, "nvme_composite", "temp.celsius"); o.Value == nil || *o.Value != 0 {
		t.Errorf("zero reading = %v", o.Value)
	}
	if hasObs(obs, "nvme_composite", "temp.high_celsius") || hasObs(obs, "nvme_composite", "temp.critical_celsius") {
		t.Errorf("zero limits must be absent: %+v", obs)
	}
}

func TestOnlyOneLimit(t *testing.T) {
	obs, err := collect(t, fixtureSensors{stats: []sensors.TemperatureStat{{SensorKey: "a", Temperature: 40, Critical: 90}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hasObs(obs, "a", "temp.high_celsius") || !hasObs(obs, "a", "temp.critical_celsius") {
		t.Errorf("unexpected: %+v", obs)
	}
}

func TestSensorsFilter(t *testing.T) {
	f := fixtureSensors{stats: []sensors.TemperatureStat{
		{SensorKey: "a", Temperature: 1}, {SensorKey: "b", Temperature: 2}, {SensorKey: "c", Temperature: 3},
	}}
	for name, params := range map[string]map[string]any{
		"strings": {"sensors": []string{"b", "c", "b"}},
		"any":     {"sensors": []any{"b", "c", "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			obs, err := collect(t, f, params)
			if err != nil {
				t.Fatal(err)
			}
			if len(obs) != 3 || hasObs(obs, "a", "temp.celsius") {
				t.Fatalf("got %+v", obs)
			}
			sensorCount(t, obs, 2)
		})
	}
	// Empty list means all.
	obs, _ := collect(t, f, map[string]any{"sensors": []string{}})
	if len(obs) != 4 {
		t.Errorf("empty filter: %+v", obs)
	}
	sensorCount(t, obs, 3)
}

func TestFilterMatchesNothing(t *testing.T) {
	obs, err := collect(t, fixtureSensors{stats: []sensors.TemperatureStat{{SensorKey: "a", Temperature: 1}}},
		map[string]any{"sensors": []string{"zzz"}})
	if err == nil || !strings.Contains(err.Error(), "no temperature sensors found") {
		t.Fatalf("err = %v", err)
	}
	o := findObs(t, obs, "", "temp.sensors")
	if o.Err == "" || len(obs) != 1 {
		t.Errorf("want error observation: %+v", o)
	}
}

func TestNoSensors(t *testing.T) {
	for name, f := range map[string]fixtureSensors{
		"empty":    {},
		"err only": {err: errors.New("no hwmon")},
	} {
		t.Run(name, func(t *testing.T) {
			obs, err := collect(t, f, nil)
			if err == nil || !strings.Contains(err.Error(), "no temperature sensors found") {
				t.Fatalf("err = %v", err)
			}
			if len(obs) != 1 || obs[0].Item != "" || obs[0].Metric != "temp.sensors" || obs[0].Err == "" {
				t.Fatalf("obs = %+v", obs)
			}
		})
	}
}

func TestPartialResultsWithWarning(t *testing.T) {
	obs, err := collect(t, fixtureSensors{
		stats: []sensors.TemperatureStat{{SensorKey: "a", Temperature: 30}},
		err:   errors.New("hwmon1: permission denied"),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("warning must surface: %v", err)
	}
	if o := findObs(t, obs, "a", "temp.celsius"); o.Value == nil || *o.Value != 30 {
		t.Errorf("reading must stay authoritative: %+v", o)
	}
}

func TestDuplicateKeysAreDisambiguated(t *testing.T) {
	f := fixtureSensors{stats: []sensors.TemperatureStat{
		{SensorKey: "coretemp_core_0", Temperature: 50},
		{SensorKey: "coretemp_core_0", Temperature: 60},
		{SensorKey: "coretemp_core_0", Temperature: 70},
		{SensorKey: "coretemp_core_0#2", Temperature: 80}, // collides with a generated name
	}}
	obs, err := collect(t, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"coretemp_core_0": 50, "coretemp_core_0#2": 60, "coretemp_core_0#3": 70}
	for item, v := range want {
		if o := findObs(t, obs, item, "temp.celsius"); *o.Value != v {
			t.Errorf("%s = %v, want %v", item, *o.Value, v)
		}
	}
	items := map[string]bool{}
	for _, o := range obs {
		if o.Metric == "temp.celsius" {
			if items[o.Item] {
				t.Errorf("collision on %q", o.Item)
			}
			items[o.Item] = true
		}
	}
	if len(items) != 4 {
		t.Errorf("want 4 distinct items, got %v", items)
	}
	// The filter matches the disambiguated names.
	obs, _ = collect(t, f, map[string]any{"sensors": []string{"coretemp_core_0#2"}})
	if len(obs) != 2 || *findObs(t, obs, "coretemp_core_0#2", "temp.celsius").Value != 60 {
		t.Errorf("filter on disambiguated name: %+v", obs)
	}
}

func TestUnreportableAndNonFiniteAreSkippedAndNamed(t *testing.T) {
	obs, err := collect(t, fixtureSensors{stats: []sensors.TemperatureStat{
		{SensorKey: "ok", Temperature: 20},
		{SensorKey: "bad\nname", Temperature: 20},
		{SensorKey: "nan", Temperature: math.NaN()},
		{SensorKey: "inf", Temperature: math.Inf(1)},
		{SensorKey: "badhigh", Temperature: 20, High: math.Inf(1)},
		{SensorKey: "", Temperature: 20},
	}}, nil)
	if err == nil {
		t.Fatal("skipped sensors must be named in an advisory error")
	}
	for _, name := range []string{`"bad\nname"`, `"nan"`, `"inf"`} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
	if !hasObs(obs, "ok", "temp.celsius") {
		t.Errorf("ok sensor missing: %+v", obs)
	}
	for _, o := range obs {
		if o.Item == "bad\nname" || o.Item == "nan" || o.Item == "inf" || (o.Item == "" && o.Metric != "temp.sensors") {
			t.Errorf("unreportable item leaked: %+v", o)
		}
		if o.Value != nil && (math.IsNaN(*o.Value) || math.IsInf(*o.Value, 0)) {
			t.Errorf("non-finite value leaked: %+v", o)
		}
	}
	if o := findObs(t, obs, "badhigh", "temp.celsius"); o.Value == nil || *o.Value != 20 {
		t.Errorf("finite reading kept: %+v", o)
	}
	if hasObs(obs, "badhigh", "temp.high_celsius") {
		t.Errorf("non-finite limit must be dropped")
	}
	sensorCount(t, obs, 2)
}

func TestOnlyUnreportableSensorsIsNoSensors(t *testing.T) {
	obs, err := collect(t, fixtureSensors{stats: []sensors.TemperatureStat{{SensorKey: "nan", Temperature: math.NaN()}}}, nil)
	if err == nil || !hasObs(obs, "", "temp.sensors") || hasObs(obs, "", "temp.celsius") {
		t.Fatalf("obs=%+v err=%v", obs, err)
	}
}

func TestBadParams(t *testing.T) {
	_, err := collect(t, fixtureSensors{}, map[string]any{"sensors": "x"})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (temperaturePlugin{Sensors: fixtureSensors{}}).Collect(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// TestSensorCountReplacesTheError: temp.sensors on item "" carries the
// number of sensors reported on every good run and the "no temperature
// sensors found" error otherwise, so the error is replaced by the next
// good run instead of standing on a metric that is never sent again.
func TestSensorCountReplacesTheError(t *testing.T) {
	obs, err := collect(t, fixtureSensors{}, nil)
	if err == nil {
		t.Fatal("want error")
	}
	bad := findObs(t, obs, "", "temp.sensors")
	obs, err = collect(t, fixtureSensors{stats: []sensors.TemperatureStat{{SensorKey: "a", Temperature: 40}}, err: errors.New("one unreadable")}, nil)
	if err == nil {
		t.Fatal("want the warning as an advisory error")
	}
	good := findObs(t, obs, "", "temp.sensors")
	if bad.Item != good.Item || bad.Metric != good.Metric || bad.Err == "" || good.Value == nil || *good.Value != 1 {
		t.Fatalf("bad = %+v, good = %+v", bad, good)
	}
}
