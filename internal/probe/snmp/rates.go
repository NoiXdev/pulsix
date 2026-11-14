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
// apart. A 32-bit counter that went backwards wrapped once at 2^32 (two
// wraps within one interval cannot be told apart from one); a 64-bit
// counter that went backwards was reset and gives no rate, as does a width
// that changed or a reading that is missing.
func perSecond(prev, cur counter, secs float64) (float64, bool) {
	if secs <= 0 || prev.width == 0 || cur.width != prev.width {
		return 0, false
	}
	var d uint64
	switch {
	case cur.value >= prev.value:
		d = cur.value - prev.value
	case cur.width == 32 && prev.value < 1<<32:
		d = cur.value + 1<<32 - prev.value
	default:
		return 0, false
	}
	return float64(d) / secs, true
}

// ifRates are one interface's rates between two polls; has* says which
// could be computed.
type ifRates struct {
	inBps, outBps, errs    float64
	hasIn, hasOut, hasErrs bool
}

// interfaceRates computes an interface's rates over secs: bits per second
// from the 64-bit octet counters when both polls have them, else from the
// 32-bit ones, and errors per second from ifInErrors plus ifOutErrors.
func interfaceRates(prev, cur ifCounters, secs float64) ifRates {
	var r ifRates
	octets := func(hcPrev, hcCur, prev32, cur32 counter) (float64, bool) {
		if hcPrev.width == 64 && hcCur.width == 64 {
			v, ok := perSecond(hcPrev, hcCur, secs)
			return v * 8, ok
		}
		v, ok := perSecond(prev32, cur32, secs)
		return v * 8, ok
	}
	r.inBps, r.hasIn = octets(prev.hcIn, cur.hcIn, prev.in, cur.in)
	r.outBps, r.hasOut = octets(prev.hcOut, cur.hcOut, prev.out, cur.out)
	inErr, okIn := perSecond(prev.inErr, cur.inErr, secs)
	outErr, okOut := perSecond(prev.outErr, cur.outErr, secs)
	r.errs, r.hasErrs = inErr+outErr, okIn && okOut
	return r
}
