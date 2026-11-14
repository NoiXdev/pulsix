package snmp

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

// at is a sample taken d after t0 on a device that has been up for
// uptimeSecs seconds.
func at(d time.Duration, uptimeSecs float64) sample {
	return sample{at: t0.Add(d), uptime: uint32(uptimeSecs * 100), hasUptime: true}
}

func TestElapsed(t *testing.T) {
	prev := at(0, 1000)
	for _, tc := range []struct {
		name string
		prev *sample
		cur  sample
		want float64
		ok   bool
	}{
		{"first poll", nil, at(60*time.Second, 1060), 0, false},
		{"normal", &prev, at(60*time.Second, 1060), 60, true},
		{"device clock a bit slow", &prev, at(60*time.Second, 1058), 60, true},
		{"interval zero", &prev, at(0, 1000), 0, false},
		{"clock went backwards", &prev, at(-time.Second, 1001), 0, false},
		{"sysUpTime went backwards: reboot", &prev, at(60*time.Second, 30), 0, false},
		{"rebooted long before this poll", &prev, at(600*time.Second, 1001), 0, false},
		{"no uptime now", &prev, sample{at: t0.Add(time.Minute)}, 0, false},
		{"no uptime before", &sample{at: t0}, at(60*time.Second, 1060), 0, false},
	} {
		got, ok := elapsed(tc.prev, tc.cur)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: elapsed = %v, %v; want %v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPerSecond(t *testing.T) {
	c32 := func(v uint64) counter { return counter{value: v, width: 32} }
	c64 := func(v uint64) counter { return counter{value: v, width: 64} }
	for _, tc := range []struct {
		name      string
		prev, cur counter
		secs      float64
		want      float64
		ok        bool
	}{
		{"normal 32-bit", c32(1000), c32(7000), 60, 100, true},
		{"unchanged", c32(5), c32(5), 10, 0, true},
		{"32-bit wrap", c32(1<<32 - 600), c32(5400), 60, 100, true},
		{"64-bit", c64(1 << 40), c64(1<<40 + 6000), 60, 100, true},
		{"64-bit counter beyond 32 bits never wraps", c64(1<<40 + 10), c64(5), 60, 0, false},
		{"64-bit reset", c64(9000), c64(10), 60, 0, false},
		{"32-bit value out of range is a reset", counter{value: 1 << 33, width: 32}, c32(10), 60, 0, false},
		{"absent before", counter{}, c32(10), 60, 0, false},
		{"absent now", c32(10), counter{}, 60, 0, false},
		{"width changed", c32(10), c64(20), 60, 0, false},
		{"interval zero", c32(10), c32(20), 0, 0, false},
		{"interval negative", c32(10), c32(20), -1, 0, false},
	} {
		got, ok := perSecond(tc.prev, tc.cur, tc.secs)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: perSecond = %v, %v; want %v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestInterfaceRates(t *testing.T) {
	prev := ifCounters{
		hcIn: counter{1000, 64}, hcOut: counter{2000, 64},
		in: counter{100, 32}, out: counter{200, 32},
		inErr: counter{1, 32}, outErr: counter{1<<32 - 1, 32},
	}
	cur := ifCounters{
		hcIn: counter{1000 + 7500, 64}, hcOut: counter{2000 + 15000, 64},
		in: counter{100 + 1, 32}, out: counter{200 + 1, 32},
		inErr: counter{1 + 30, 32}, outErr: counter{29, 32}, // wrapped: +30
	}
	r := interfaceRates(prev, cur, 60)
	if !r.hasIn || r.inBps != 1000 || !r.hasOut || r.outBps != 2000 || !r.hasErrs || r.errs != 1 {
		t.Fatalf("rates from 64-bit octets: %+v", r)
	}

	// Without 64-bit octets on either side, the 32-bit ones count.
	prev.hcIn, cur.hcOut = counter{}, counter{}
	r = interfaceRates(prev, cur, 1)
	if !r.hasIn || r.inBps != 8 || !r.hasOut || r.outBps != 8 {
		t.Fatalf("rates from 32-bit octets: %+v", r)
	}

	// One error counter missing: no error rate.
	cur.outErr = counter{}
	if r = interfaceRates(prev, cur, 1); r.hasErrs {
		t.Fatalf("errors without ifOutErrors: %+v", r)
	}
}
