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
		wrapped   bool
	}{
		{"normal 32-bit", c32(1000), c32(7000), 60, 100, true, false},
		{"unchanged", c32(5), c32(5), 10, 0, true, false},
		{"32-bit wrap", c32(1<<32 - 600), c32(5400), 60, 100, true, true},
		{"64-bit", c64(1 << 40), c64(1<<40 + 6000), 60, 100, true, false},
		{"64-bit counter beyond 32 bits never wraps", c64(1<<40 + 10), c64(5), 60, 0, false, false},
		{"64-bit reset", c64(9000), c64(10), 60, 0, false, false},
		{"32-bit value out of range is a reset", counter{value: 1 << 33, width: 32}, c32(10), 60, 0, false, false},
		{"absent before", counter{}, c32(10), 60, 0, false, false},
		{"absent now", c32(10), counter{}, 60, 0, false, false},
		{"width changed", c32(10), c64(20), 60, 0, false, false},
		{"interval zero", c32(10), c32(20), 0, 0, false, false},
		{"interval negative", c32(10), c32(20), -1, 0, false, false},
	} {
		got, wrapped, ok := perSecond(tc.prev, tc.cur, tc.secs)
		if ok != tc.ok || got != tc.want || wrapped != tc.wrapped {
			t.Errorf("%s: perSecond = %v, wrapped %v, %v; want %v, %v, %v", tc.name, got, wrapped, ok, tc.want, tc.wrapped, tc.ok)
		}
	}
}

func TestInterfaceRates(t *testing.T) {
	prev := ifCounters{
		hcIn: counter{1000, 64}, hcOut: counter{2000, 64},
		in: counter{100, 32}, out: counter{200, 32},
		inErr: counter{1, 32}, outErr: counter{1000, 32},
	}
	cur := ifCounters{
		hcIn: counter{1000 + 7500, 64}, hcOut: counter{2000 + 15000, 64},
		in: counter{100 + 1, 32}, out: counter{200 + 1, 32},
		inErr: counter{1 + 30, 32}, outErr: counter{1000 + 30, 32},
	}
	r := interfaceRates(prev, cur, 60, 0)
	if !r.hasIn || r.inBps != 1000 || !r.hasOut || r.outBps != 2000 || !r.hasErrs || r.errs != 1 {
		t.Fatalf("rates from 64-bit octets: %+v", r)
	}

	// Without 64-bit octets on either side, the 32-bit ones count.
	prev.hcIn, cur.hcOut = counter{}, counter{}
	r = interfaceRates(prev, cur, 1, 0)
	if !r.hasIn || r.inBps != 8 || !r.hasOut || r.outBps != 8 {
		t.Fatalf("rates from 32-bit octets: %+v", r)
	}

	// An error counter that went backwards was cleared, not wrapped:
	// error counters do not move 2^32 in one interval.
	wrapped := cur
	wrapped.outErr = counter{29, 32}
	if r = interfaceRates(prev, wrapped, 1, 0); r.hasErrs {
		t.Fatalf("errors after an error counter went backwards: %+v", r)
	}

	// One error counter missing: no error rate.
	cur.outErr = counter{}
	if r = interfaceRates(prev, cur, 1, 0); r.hasErrs {
		t.Fatalf("errors without ifOutErrors: %+v", r)
	}
}

// TestInterfaceRatesAboveTheSpeed: only a rate computed across a 32-bit
// wrap is held against the interface's speed. Above it, the "wrap" was a
// clear (no reboot): that rate and the error rate beside it are dropped.
// A 64-bit or a not-wrapped rate is real whatever the speed says.
func TestInterfaceRatesAboveTheSpeed(t *testing.T) {
	prev := ifCounters{in: counter{1 << 31, 32}, out: counter{1000, 32}, inErr: counter{0, 32}, outErr: counter{0, 32}}
	cleared := ifCounters{in: counter{500, 32}, out: counter{1000 + 7500, 32}, inErr: counter{0, 32}, outErr: counter{0, 32}}
	// in: (2^32 - 2^31 + 500) bytes in 60 s, a "wrap" of about 286 Mbit/s.
	r := interfaceRates(prev, cleared, 60, 100e6)
	if r.hasIn || !r.hasOut || r.outBps != 1000 || r.hasErrs {
		t.Fatalf("32-bit clear on a 100 Mbit/s interface: %+v", r)
	}
	if r = interfaceRates(prev, cleared, 60, 0); !r.hasIn || !r.hasErrs {
		t.Fatalf("unknown speed caps nothing: %+v", r)
	}
	if r = interfaceRates(prev, cleared, 60, 1e9); !r.hasIn {
		t.Fatalf("1 Gbit/s interface: a wrap of 286 Mbit/s is plausible: %+v", r)
	}

	// lo at an advertised 10 Mbit/s carrying 100 Mbit/s: 64-bit counters
	// and 32-bit counters that did not wrap are reported.
	bytes := uint64(100e6 / 8 * 60)
	hc := interfaceRates(ifCounters{hcIn: counter{1 << 40, 64}}, ifCounters{hcIn: counter{1<<40 + bytes, 64}}, 60, 10e6)
	if !hc.hasIn || hc.inBps != 100e6 {
		t.Fatalf("64-bit rate above the speed: %+v", hc)
	}
	plain := interfaceRates(ifCounters{in: counter{1000, 32}}, ifCounters{in: counter{1000 + bytes, 32}}, 60, 10e6)
	if !plain.hasIn || plain.inBps != 100e6 {
		t.Fatalf("not-wrapped 32-bit rate above the speed: %+v", plain)
	}
}

// TestHCClearGivesNoErrorSpike: a clear on an interface with 64-bit
// octets resets them (no rate) and its 32-bit error counters, which go
// backwards and are a reset too, never a wrap of 2^32 errors.
func TestHCClearGivesNoErrorSpike(t *testing.T) {
	prev := ifCounters{hcIn: counter{1 << 40, 64}, inErr: counter{500, 32}, outErr: counter{20, 32}}
	cur := ifCounters{hcIn: counter{100, 64}, inErr: counter{0, 32}, outErr: counter{0, 32}}
	if r := interfaceRates(prev, cur, 60, 100e9); r.hasIn || r.hasErrs {
		t.Fatalf("after a clear: %+v", r)
	}
}

// TestSaturatedT1KeepsItsRate: a T1 (ifSpeed 1544000, ifHighSpeed 1)
// running full across a 32-bit wrap keeps its rate; the precise ifSpeed
// is the cap, not ifHighSpeed's rounded 1 Mbit/s.
func TestSaturatedT1KeepsItsRate(t *testing.T) {
	t1 := ifRow{speed: gauge{1544000, true}, highSpeed: gauge{1, true}}
	bytes := uint64(1544000 / 8 * 60)
	prev := ifCounters{in: counter{1<<32 - 1000, 32}}
	cur := ifCounters{in: counter{bytes - 1000, 32}}
	r := interfaceRates(prev, cur, 60, t1.bitsPerSecond())
	if !r.hasIn || r.inBps != 1544000 {
		t.Fatalf("saturated T1 across a wrap: %+v", r)
	}
}

func TestInterfaceSpeed(t *testing.T) {
	for _, tc := range []struct {
		high, low uint64
		hasHigh   bool
		hasLow    bool
		want      float64
	}{
		{10000, 0, true, false, 10e9},       // no ifSpeed
		{10000, 0, true, true, 10e9},        // ifSpeed 0: unknown
		{0, 100e6, true, true, 100e6},       // ifHighSpeed 0: below 1 Mbit/s or unknown
		{1, 1544000, true, true, 1544000},   // the precise ifSpeed wins
		{10, 10000000, true, true, 10e6},    // net-snmp's lo
		{1000, 4294967295, true, true, 1e9}, // ifSpeed saturated
		{0, 4294967295, false, true, 0},     // ifSpeed saturated, no ifHighSpeed
		{0, 0, false, false, 0},
	} {
		r := ifRow{highSpeed: gauge{tc.high, tc.hasHigh}, speed: gauge{tc.low, tc.hasLow}}
		if got := r.bitsPerSecond(); got != tc.want {
			t.Errorf("%+v: speed %v, want %v", tc, got, tc.want)
		}
	}
}
