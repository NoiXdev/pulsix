package temperature

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/sensors"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// testClock is a hand-set clock for the vanished-metric memory.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

var tempT0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

// TestLimitThatStopsBeingStated (backlog 3.55): a limit a sensor stated
// and no longer states while the sensor still reads gets "<metric> is no
// longer reported" each round, until it has been missing for 24 h; a round
// in which the sensor is not read adds nothing.
func TestLimitThatStopsBeingStated(t *testing.T) {
	clock := &testClock{t: tempT0}
	f := &fixtureSensors{stats: []sensors.TemperatureStat{{SensorKey: "a", Temperature: 40, High: 80, Critical: 90}}}
	p := &temperaturePlugin{Sensors: f, now: clock.now}
	collect := func() []checks.Observation {
		t.Helper()
		obs, _ := p.Collect(context.Background(), nil)
		return obs
	}
	const vanished = "temp.high_celsius is no longer reported"

	collect()
	f.stats = []sensors.TemperatureStat{{SensorKey: "a", Temperature: 41, Critical: 90}}
	for _, after := range []time.Duration{time.Minute, 2 * time.Minute} {
		clock.t = tempT0.Add(after)
		obs := collect()
		if o := findObs(t, obs, "a", "temp.high_celsius"); o.Err != vanished {
			t.Fatalf("after %s: high = %+v, want the error %q", after, o, vanished)
		}
		if len(obs) != 4 { // celsius, critical, the error, temp.sensors
			t.Fatalf("after %s: %+v", after, obs)
		}
		sensorCount(t, obs, 1)
	}

	// The sensor is not read: nothing about it, and no error added.
	f.stats, f.err = nil, errors.New("hwmon unreadable")
	clock.t = tempT0.Add(time.Hour)
	if obs := collect(); hasObs(obs, "a", "temp.high_celsius") {
		t.Fatalf("unread sensor: %+v", obs)
	}
	f.stats, f.err = []sensors.TemperatureStat{{SensorKey: "a", Temperature: 41, Critical: 90}, {SensorKey: "b", Temperature: 30}}, nil
	clock.t = tempT0.Add(2 * time.Hour)
	if o := findObs(t, collect(), "a", "temp.high_celsius"); o.Err != vanished {
		t.Fatalf("read again: high = %+v, want the error %q", o, vanished)
	}

	clock.t = tempT0.Add(24 * time.Hour)
	if obs := collect(); hasObs(obs, "a", "temp.high_celsius") {
		t.Fatalf("after 24 h: %+v, want the limit forgotten", obs)
	}
}

// TestInstancesKeepTheirOwnMemory: two check keys (say, one filtered to a
// sensor) collect with two instances, and one's remembered limits never
// flag on the other.
func TestInstancesKeepTheirOwnMemory(t *testing.T) {
	clock := &testClock{t: tempT0}
	f := &fixtureSensors{stats: []sensors.TemperatureStat{{SensorKey: "a", Temperature: 40, High: 80}}}
	proto, ok := New().(checks.StatefulPlugin)
	if !ok {
		t.Fatalf("New() = %T, want a checks.StatefulPlugin", New())
	}
	inst := func() *temperaturePlugin {
		p, ok := proto.NewInstance().(*temperaturePlugin)
		if !ok || p.Sensors == nil || p.now == nil {
			t.Fatalf("NewInstance() = %#v, want the prototype's settings", p)
		}
		p.Sensors, p.now = f, clock.now
		return p
	}
	a, b := inst(), inst()

	if _, err := a.Collect(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	f.stats = []sensors.TemperatureStat{{SensorKey: "a", Temperature: 40}}
	clock.t = tempT0.Add(time.Minute)
	if obs, _ := b.Collect(context.Background(), nil); hasObs(obs, "a", "temp.high_celsius") {
		t.Fatalf("b: %+v, want no error from a's memory", obs)
	}
	if obs, _ := a.Collect(context.Background(), nil); !hasObs(obs, "a", "temp.high_celsius") {
		t.Fatalf("a: %+v, want the error", obs)
	}
}
