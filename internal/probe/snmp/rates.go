package snmp

import "time"

// counter is one counter reading: its value and width (32 or 64 bits, from
// the answer's type, Counter32 or Counter64); width 0 means the device did
// not report it.
type counter struct {
	value uint64
	width uint8
}

// ifCounters are one interface's counters at one poll.
type ifCounters struct {
	hcIn, hcOut   counter // ifHCInOctets, ifHCOutOctets
	in, out       counter // ifInOctets, ifOutOctets
	inErr, outErr counter // ifInErrors, ifOutErrors
}

// sample is what a Poller keeps of one poll for the next one's rates: when
// it was taken, the device's sysUpTime and the counters by ifIndex.
type sample struct {
	at        time.Time
	uptime    uint32 // sysUpTime, hundredths of a second
	hasUptime bool
	ifs       map[int]ifCounters
}

// elapsed is the time between two polls in seconds, and whether rates over
// it can be trusted: there is a previous poll, both report sysUpTime, the
// clock moved forward, and the device did not restart in between — its
// sysUpTime neither went backwards nor advanced by less than half the
// interval (a restart long enough ago to have caught up past the previous
// value). sysUpTime wraps after 497 days; that poll is taken for a restart
// and gives no rates once.
func elapsed(prev *sample, cur sample) (float64, bool) {
	if prev == nil || !prev.hasUptime || !cur.hasUptime || cur.uptime < prev.uptime {
		return 0, false
	}
	secs := cur.at.Sub(prev.at).Seconds()
	if secs <= 0 {
		return 0, false
	}
	// One second of slack for the device's clock and the time the
	// requests took.
	if up := float64(cur.uptime-prev.uptime) / 100; up+1 < secs/2 {
		return 0, false
	}
	return secs, true
}

// perSecond is a counter's increase per second between two readings secs
// apart, and whether it was computed across a wrap. A 32-bit counter that
// went backwards wrapped once at 2^32 (two wraps within one interval
// cannot be told apart from one, nor can a counter cleared without a
// reboot; interfaceRates holds such a rate against the interface's
// speed). A 64-bit counter that went backwards was reset and gives no
// rate, as does a width that changed or a reading that is missing.
func perSecond(prev, cur counter, secs float64) (rate float64, wrapped, ok bool) {
	if secs <= 0 || prev.width == 0 || cur.width != prev.width {
		return 0, false, false
	}
	switch {
	case cur.value >= prev.value:
		return float64(cur.value-prev.value) / secs, false, true
	case cur.width == 32 && prev.value < 1<<32:
		return float64(cur.value+1<<32-prev.value) / secs, true, true
	}
	return 0, false, false
}

// increasePerSecond is perSecond for a counter that never wraps in one
// interval: one that went backwards was reset, and gives no rate.
func increasePerSecond(prev, cur counter, secs float64) (float64, bool) {
	if secs <= 0 || prev.width == 0 || cur.width != prev.width || cur.value < prev.value {
		return 0, false
	}
	return float64(cur.value-prev.value) / secs, true
}

// ifRates are one interface's rates between two polls; has* says which
// could be computed.
type ifRates struct {
	inBps, outBps, errs    float64
	hasIn, hasOut, hasErrs bool
}

// speedSlack is how far above an interface's speed a rate across a wrap
// may read before the wrap counts as a clear: the counters and the clock
// are not read at the same instant, so a saturated link reads a little
// above its speed.
const speedSlack = 1.1

// interfaceRates computes an interface's rates over secs: bits per second
// from the 64-bit octet counters when both polls have them, else from the
// 32-bit ones, and errors per second from ifInErrors plus ifOutErrors.
//
// A 32-bit octet counter that went backwards is read as one wrap, but a
// counter cleared without a reboot (an operator's "clear counters") looks
// the same. speed is the interface's bits per second, 0 when unknown; a
// rate across a wrap above it (with speedSlack) was a clear, and is
// dropped, together with the error rate, whose counters were cleared too.
// Only a wrap is held against the speed: devices advertise speeds below
// what an interface carries (net-snmp's lo says 10 Mbit/s, Cisco's
// ifSpeed follows the configured bandwidth), so a 64-bit rate or one
// without a wrap is reported whatever the speed says.
//
// Error counters never move 2^32 in one interval, so one that went
// backwards was cleared and gives no rate, never a wrap.
//
// What stays undetectable: a clear on a 32-bit-only interface whose speed
// is unknown, or whose false wrap stays under the speed (from about
// 1 Gbit/s at a 60 s interval, since a wrap spans at most 2^32 octets),
// reads as one spike for that poll; two wraps within one interval read as
// one.
func interfaceRates(prev, cur ifCounters, secs, speed float64) ifRates {
	var r ifRates
	cleared := false
	octets := func(hcPrev, hcCur, prev32, cur32 counter) (float64, bool) {
		if hcPrev.width == 64 && hcCur.width == 64 {
			v, _, ok := perSecond(hcPrev, hcCur, secs)
			return v * 8, ok
		}
		v, wrapped, ok := perSecond(prev32, cur32, secs)
		if ok && wrapped && speed > 0 && v*8 > speed*speedSlack {
			cleared = true
			return 0, false
		}
		return v * 8, ok
	}
	r.inBps, r.hasIn = octets(prev.hcIn, cur.hcIn, prev.in, cur.in)
	r.outBps, r.hasOut = octets(prev.hcOut, cur.hcOut, prev.out, cur.out)
	inErr, okIn := increasePerSecond(prev.inErr, cur.inErr, secs)
	outErr, okOut := increasePerSecond(prev.outErr, cur.outErr, secs)
	r.errs, r.hasErrs = inErr+outErr, okIn && okOut && !cleared
	return r
}
