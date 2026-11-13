package smart

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// testClock is a hand-set clock for the vanished-metric memory.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

var smartT0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

const (
	withTemp    = `{"smart_support":{"available":true},"smart_status":{"passed":true},"temperature":{"current":30}}`
	withoutTemp = `{"smart_support":{"available":true},"smart_status":{"passed":true}}`
	tempAsText  = `{"smart_support":{"available":true},"smart_status":{"passed":true},"temperature":{"current":"hot"}}`
)

// round collects once with p, smartctl answering /dev/sda with r.
func round(t *testing.T, p *smartPlugin, r reply) []checks.Observation {
	t.Helper()
	f := newFakeRunner(t, map[string]reply{"/dev/sda": r})
	p.smartctl = func() (runner, error) { return f, nil }
	obs, _ := p.Collect(context.Background(), map[string]any{"devices": []string{"/dev/sda"}})
	return obs
}

// TestSmartFieldThatStopsBeingReported (backlog 3.55): a field a disk
// stated and no longer states (or states with another type) while the
// disk still reports gets "<metric> is no longer reported" each round,
// until it has been missing for 24 h; a round in which the disk fails or
// sleeps adds nothing.
func TestSmartFieldThatStopsBeingReported(t *testing.T) {
	clock := &testClock{t: smartT0}
	p := &smartPlugin{now: clock.now}
	const vanished = "smart.temperature_celsius is no longer reported"

	if obs := round(t, p, reply{stdout: []byte(withTemp)}); value(t, obs, "/dev/sda", "smart.temperature_celsius") != 30 {
		t.Fatalf("first round: %+v", obs)
	}
	for i, r := range []reply{{stdout: []byte(withoutTemp)}, {stdout: []byte(tempAsText)}} {
		clock.t = clock.t.Add(time.Hour)
		obs := round(t, p, r)
		if o := findObs(t, obs, "/dev/sda", "smart.temperature_celsius"); o.Err != vanished {
			t.Fatalf("round %d: temperature = %+v, want the error %q", i, o, vanished)
		}
		if value(t, obs, "/dev/sda", "smart.passed") != 1 {
			t.Fatalf("round %d: smart.passed missing", i)
		}
		encodesAsCheckResult(t, findObs(t, obs, "/dev/sda", "smart.temperature_celsius"))
	}

	clock.t = clock.t.Add(time.Hour)
	onlyErr(t, round(t, p, reply{stdout: fixture(t, "open-failed.json"), exit: 2}), "/dev/sda", "could not open")
	clock.t = clock.t.Add(time.Hour)
	if got := itemObs(round(t, p, reply{stdout: fixture(t, "standby.json")}), "/dev/sda"); len(got) != 0 {
		t.Fatalf("asleep: %+v, want nothing", got)
	}

	clock.t = smartT0.Add(24 * time.Hour)
	if obs := round(t, p, reply{stdout: []byte(withoutTemp)}); len(itemObs(obs, "/dev/sda")) != 1 {
		t.Fatalf("after 24 h: %+v, want only smart.passed", itemObs(obs, "/dev/sda"))
	}

	clock.t = clock.t.Add(time.Hour)
	round(t, p, reply{stdout: []byte(withTemp)})
	clock.t = clock.t.Add(time.Hour)
	if got := round(t, p, reply{stdout: []byte(withTemp)}); len(itemObs(got, "/dev/sda")) != 2 {
		t.Fatalf("back: %+v, want smart.passed and the temperature", itemObs(got, "/dev/sda"))
	}
}

// TestSmartInstancesKeepTheirOwnMemory: two check keys collect with two
// instances, and one's remembered fields never flag on the other.
func TestSmartInstancesKeepTheirOwnMemory(t *testing.T) {
	clock := &testClock{t: smartT0}
	proto, ok := New().(checks.StatefulPlugin)
	if !ok {
		t.Fatalf("New() = %T, want a checks.StatefulPlugin", New())
	}
	a, b := proto.NewInstance().(*smartPlugin), proto.NewInstance().(*smartPlugin)
	if a.smartctl == nil || a.now == nil {
		t.Fatalf("NewInstance lost the prototype's settings: %+v", a)
	}
	a.now, b.now = clock.now, clock.now

	round(t, a, reply{stdout: []byte(withTemp)})
	clock.t = clock.t.Add(time.Minute)
	if got := itemObs(round(t, b, reply{stdout: []byte(withoutTemp)}), "/dev/sda"); len(got) != 1 {
		t.Fatalf("b: %+v, want only smart.passed", got)
	}
	if o := findObs(t, round(t, a, reply{stdout: []byte(withoutTemp)}), "/dev/sda", "smart.temperature_celsius"); !strings.Contains(o.Err, "no longer reported") {
		t.Fatalf("a: temperature = %+v, want the error", o)
	}
}

// TestSmartNoDataTextNeedsAnAnswerWithoutData (backlog 3.55): "no device
// with SMART data" is raised only when a device answered without SMART
// data and none sleeps. A round in which every device failed reports the
// count: the failures are on the devices' own items.
func TestSmartNoDataTextNeedsAnAnswerWithoutData(t *testing.T) {
	failed := reply{stdout: fixture(t, "open-failed.json"), exit: 2}
	noData := reply{stdout: fixture(t, "virtual-disk.json")}
	for _, tc := range []struct {
		desc    string
		replies map[string]reply
		noData  bool
	}{
		{"every device failed", map[string]reply{"/dev/sda": failed, "/dev/sdb": failed}, false},
		{"one failed, one without data", map[string]reply{"/dev/sda": failed, "/dev/sdb": noData}, true},
		{"every device without data", map[string]reply{"/dev/sda": noData, "/dev/sdb": noData}, true},
		{"one without data, one asleep", map[string]reply{"/dev/sda": noData, "/dev/sdb": {stdout: fixture(t, "standby.json")}}, false},
		{"one failed, one asleep", map[string]reply{"/dev/sda": failed, "/dev/sdb": {stdout: fixture(t, "standby.json")}}, false},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			obs, _ := plugin(newFakeRunner(t, tc.replies)).Collect(context.Background(), map[string]any{"devices": []string{"/dev/sda", "/dev/sdb"}})
			if tc.noData {
				hostErr(t, obs, "no device with SMART data")
				return
			}
			deviceCount(t, obs, 2)
		})
	}
}
