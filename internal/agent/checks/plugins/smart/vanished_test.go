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
// until it has been missing for 24 h; a round in which the disk fails adds
// nothing, and one in which it sleeps reports only its power mode.
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
	if got := itemObs(round(t, p, reply{stdout: fixture(t, "standby.json")}), "/dev/sda"); len(got) != 1 || got[0].Metric != "smart.power_mode" {
		t.Fatalf("asleep: %+v, want only the power mode", got)
	}

	clock.t = smartT0.Add(24 * time.Hour)
	if obs := round(t, p, reply{stdout: []byte(withoutTemp)}); len(itemObs(obs, "/dev/sda")) != 2 {
		t.Fatalf("after 24 h: %+v, want only smart.passed and the power mode", itemObs(obs, "/dev/sda"))
	}

	clock.t = clock.t.Add(time.Hour)
	round(t, p, reply{stdout: []byte(withTemp)})
	clock.t = clock.t.Add(time.Hour)
	if got := round(t, p, reply{stdout: []byte(withTemp)}); len(itemObs(got, "/dev/sda")) != 3 {
		t.Fatalf("back: %+v, want smart.passed, the temperature and the power mode", itemObs(got, "/dev/sda"))
	}
}

// TestSmartSleepFlagsNothing (backlog 3.9b): a round in which the disk
// sleeps reports smart.power_mode standby and nothing else, and that label
// is not the disk "still reporting" — none of its remembered fields is
// flagged as no longer reported, however long it sleeps. The sleep also
// does not refresh the memory: a disk that wakes within 24 h of its last
// readings is held to the fields it stated then, one that slept longer
// starts afresh, and either reports its readings as usual on waking.
func TestSmartSleepFlagsNothing(t *testing.T) {
	standby := reply{stdout: fixture(t, "standby.json")}
	for _, tc := range []struct {
		name      string
		sleep     time.Duration
		wake      string
		wantFlags bool
	}{
		{"short sleep, same fields", 2 * time.Hour, withTemp, false},
		{"long sleep, same fields", 30 * time.Hour, withTemp, false},
		{"week-long sleep, same fields", 7 * 24 * time.Hour, withTemp, false},
		{"short sleep, a field gone", 2 * time.Hour, withoutTemp, true},
		{"long sleep, a field gone", 30 * time.Hour, withoutTemp, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &testClock{t: smartT0}
			p := &smartPlugin{now: clock.now}
			if obs := round(t, p, reply{stdout: []byte(withTemp)}); label(t, obs, "/dev/sda", "smart.power_mode") != "active" {
				t.Fatalf("first round: %+v", obs)
			}
			for clock.t.Sub(smartT0) < tc.sleep {
				clock.t = clock.t.Add(time.Hour)
				got := itemObs(round(t, p, standby), "/dev/sda")
				if len(got) != 1 || got[0].Metric != "smart.power_mode" || got[0].Label == nil || *got[0].Label != "standby" {
					t.Fatalf("asleep at +%s: %+v, want only smart.power_mode standby", clock.t.Sub(smartT0), got)
				}
			}
			clock.t = clock.t.Add(time.Hour)
			obs := round(t, p, reply{stdout: []byte(tc.wake)})
			if label(t, obs, "/dev/sda", "smart.power_mode") != "active" || value(t, obs, "/dev/sda", "smart.passed") != 1 {
				t.Fatalf("awake: %+v, want the readings and active", obs)
			}
			var flags []string
			for _, o := range itemObs(obs, "/dev/sda") {
				if strings.Contains(o.Err, "no longer reported") {
					flags = append(flags, o.Metric)
				}
			}
			if want := tc.wantFlags; (len(flags) > 0) != want || (want && (len(flags) != 1 || flags[0] != "smart.temperature_celsius")) {
				t.Fatalf("awake: flagged %q, want the temperature flagged = %v", flags, want)
			}
		})
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
	if got := itemObs(round(t, b, reply{stdout: []byte(withoutTemp)}), "/dev/sda"); len(got) != 2 {
		t.Fatalf("b: %+v, want only smart.passed and the power mode", got)
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
