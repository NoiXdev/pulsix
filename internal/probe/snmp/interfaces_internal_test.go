package snmp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/noixdev/pulsix/internal/probe/probetest"
)

var ifParams = params(map[string]any{"interfaces": true})

func TestNameInterfaces(t *testing.T) {
	long := strings.Repeat("x", 600)
	rows := []ifRow{
		{index: 1, name: "eth0", descr: "Ethernet 0"},
		{index: 2, descr: "only a descr"},
		{index: 3},
		{index: 4, name: "Gi1/0/1\x00\x07", descr: "ignored"},
		{index: 5, name: "bad\xffname"},
		{index: 6, name: long, descr: "short descr is not used"},
		{index: 7, name: "dup"},
		{index: 8, name: "dup"},
		{index: 9, name: "\x00\x01", descr: "\x02"},
		{index: 10, name: "if11"}, // takes the fallback name of 11
		{index: 11},
		{index: 12, name: "dup (7)"}, // collides with 7's suffixed name
		{index: 13, name: strings.Repeat("y", 510)},
		{index: 14, name: strings.Repeat("y", 510)},
	}
	nameInterfaces(rows)
	want := map[int]string{
		1: "eth0", 2: "only a descr", 3: "if3", 4: "Gi1/0/1", 5: "bad�name", 6: "if6",
		7: "dup (7)", 8: "dup (8)", 9: "if9", 10: "if11 (10)", 11: "if11 (11)", 12: "if12",
		13: "if13", 14: "if14",
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.item != want[r.index] {
			t.Errorf("interface %d: item %q, want %q", r.index, r.item, want[r.index])
		}
		if seen[r.item] || !reportableItem(r.item) {
			t.Errorf("interface %d: item %q is a duplicate or unreportable", r.index, r.item)
		}
		seen[r.item] = true
	}
}

func TestInterfaceStatus(t *testing.T) {
	for _, tc := range []struct {
		admin, oper int64
		want        string
		ok          bool
	}{
		{1, 1, "up", true},
		{1, 2, "down", true},
		{1, 7, "down", true}, // lowerLayerDown
		{1, 5, "down", true}, // dormant
		{2, 2, "admin_down", true},
		{2, 1, "admin_down", true},
		{3, 3, "down", true}, // testing
		{0, 1, "", false},
		{1, 0, "", false},
	} {
		got, ok := interfaceStatus(tc.admin, tc.oper)
		if got != tc.want || ok != tc.ok {
			t.Errorf("status(%d, %d) = %q, %v; want %q, %v", tc.admin, tc.oper, got, ok, tc.want, tc.ok)
		}
	}
}

func ifDevice() *fakeDevice {
	dev := newFakeDevice()
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(100000))
	dev.iface(1, "lo", "loopback", 1, 1, 1000, 2000, true)
	dev.iface(2, "", "eth1 descr", 1, 2, 1<<32-100, 50, false)
	dev.iface(3, "eth2", "", 2, 2, 0, 0, true)
	return dev
}

// advance moves the device 60 s on: lo's 64-bit octets by 7500 in and
// 15000 out, eth1's 32-bit octets past the wrap by 750 in, and eth1's
// error counters by 120.
func advance(fp *fakePoller, dev *fakeDevice) {
	fp.now = fp.now.Add(time.Minute)
	dev.columns = map[string][]gosnmp.SnmpPDU{}
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(100000+6000))
	dev.iface(1, "lo", "loopback", 1, 1, 1000+7500, 2000+15000, true)
	dev.iface(2, "", "eth1 descr", 1, 2, 650, 50, false)
	dev.iface(3, "eth2", "", 2, 2, 0, 0, true)
	dev.columns[colIfInErrors] = nil
	dev.row(colIfInErrors, 1, gosnmp.Counter32, uint(0))
	dev.row(colIfInErrors, 2, gosnmp.Counter32, uint(100))
	dev.row(colIfInErrors, 3, gosnmp.Counter32, uint(0))
	dev.columns[colIfOutErrors] = nil
	dev.row(colIfOutErrors, 1, gosnmp.Counter32, uint(0))
	dev.row(colIfOutErrors, 2, gosnmp.Counter32, uint(20))
	dev.row(colIfOutErrors, 3, gosnmp.Counter32, uint(0))
}

func TestPollInterfacesAndTheirRates(t *testing.T) {
	dev := ifDevice()
	fp := newFakePoller(dev)

	// First poll: statuses, no rates.
	m := byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantLabel(t, m, "snmp.if.status", "lo", "up")
	wantLabel(t, m, "snmp.if.status", "eth1 descr", "down")
	wantLabel(t, m, "snmp.if.status", "eth2", "admin_down")
	for _, item := range []string{"lo", "eth1 descr", "eth2"} {
		for _, metric := range []string{"snmp.if.in_bps", "snmp.if.out_bps", "snmp.if.errors"} {
			wantAbsent(t, m, metric, item)
		}
	}
	if len(m) != 3+3 {
		t.Fatalf("first poll: %+v", m)
	}

	// Second poll, 60 s later: rates.
	advance(fp, dev)
	m = byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantValue(t, m, "snmp.if.in_bps", "lo", 1000)
	wantValue(t, m, "snmp.if.out_bps", "lo", 2000)
	wantValue(t, m, "snmp.if.errors", "lo", 0)
	wantValue(t, m, "snmp.if.in_bps", "eth1 descr", 100) // 32-bit wrap
	wantValue(t, m, "snmp.if.out_bps", "eth1 descr", 0)
	wantValue(t, m, "snmp.if.errors", "eth1 descr", 2)
	wantValue(t, m, "snmp.if.in_bps", "eth2", 0)
	wantLabel(t, m, "snmp.if.status", "eth2", "admin_down")
}

func TestPollRebootGivesNoRates(t *testing.T) {
	dev := ifDevice()
	fp := newFakePoller(dev)
	_, _ = poll(fp, ifParams, v2cSecret)
	advance(fp, dev)
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(3000)) // rebooted 30 s ago
	m := byKey(t, first(poll(fp, ifParams, v2cSecret)))
	for _, metric := range []string{"snmp.if.in_bps", "snmp.if.out_bps", "snmp.if.errors"} {
		wantAbsent(t, m, metric, "lo")
	}
	wantLabel(t, m, "snmp.if.status", "lo", "up")

	// The poll after the reboot has its rates again.
	fp.now = fp.now.Add(time.Minute)
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(9000))
	m = byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantValue(t, m, "snmp.if.in_bps", "lo", 0)
}

func TestPollAnotherTargetStartsOver(t *testing.T) {
	dev := ifDevice()
	fp := newFakePoller(dev)
	_, _ = poll(fp, ifParams, v2cSecret)
	advance(fp, dev)
	m := byKey(t, first(fp.Poll(context.Background(), probetest.LoopbackGuard(), ifParams, "127.0.0.2", v2cSecret)))
	wantAbsent(t, m, "snmp.if.in_bps", "lo")
}

func TestProbeIsStatelessAndHasNoRates(t *testing.T) {
	dev := ifDevice()
	fp := newFakePoller(dev)
	_, _ = poll(fp, ifParams, v2cSecret)
	advance(fp, dev)
	// A fresh poller, as Probe uses, on the same device.
	fresh := newFakePoller(dev)
	fresh.now = fp.now
	m := byKey(t, first(poll(fresh, ifParams, v2cSecret)))
	wantAbsent(t, m, "snmp.if.in_bps", "lo")
}

func TestPollKeepsAtMost128InterfacesInIndexOrder(t *testing.T) {
	dev := newFakeDevice()
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(1))
	// Added from the highest index down; the device answers in index order.
	for i := 1000; i >= 1; i-- {
		dev.iface(i, fmt.Sprintf("port%d", i), "", 1, 1, 0, 0, true)
	}
	fp := newFakePoller(dev)
	obs, info := poll(fp, ifParams, v2cSecret)
	m := byKey(t, obs)
	if !info.InterfacesSkipped {
		t.Fatal("1000 interfaces: none reported skipped")
	}
	for i := 1; i <= 128; i++ {
		wantLabel(t, m, "snmp.if.status", fmt.Sprintf("port%d", i), "up")
	}
	wantAbsent(t, m, "snmp.if.status", "port129")
	if len(m) != 3+128 {
		t.Fatalf("%d observations, want 3 host metrics and 128 statuses", len(m))
	}
	// Every column walk stops after the kept interfaces: at most 129 rows
	// each (the 129th tells the walk to stop), not 1000.
	if limit := 129 * len(ifColumns); dev.rows > limit {
		t.Fatalf("walks read %d rows, want at most %d", dev.rows, limit)
	}

	small := ifDevice()
	_, info = poll(newFakePoller(small), ifParams, v2cSecret)
	if info.InterfacesSkipped {
		t.Fatal("3 interfaces: some reported skipped")
	}
}

func TestPollHostileInterfaceRows(t *testing.T) {
	dev := newFakeDevice()
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(1))
	dev.iface(1, "ok", "", 1, 1, 0, 0, true)
	// Rows whose index is not one ifIndex: ignored.
	dev.columns[colIfDescr] = append(dev.columns[colIfDescr],
		gosnmp.SnmpPDU{Name: colIfDescr + ".2.5", Type: gosnmp.OctetString, Value: []byte("two arcs")},
		gosnmp.SnmpPDU{Name: colIfDescr + ".0", Type: gosnmp.OctetString, Value: []byte("zero")},
		gosnmp.SnmpPDU{Name: colIfDescr + ".99999999999", Type: gosnmp.OctetString, Value: []byte("huge")},
	)
	// Values of the wrong type: the status is unknown, the counter absent.
	dev.row(colIfDescr, 3, gosnmp.OctetString, []byte("odd types"))
	dev.row(colIfAdminStatus, 3, gosnmp.OctetString, []byte("up"))
	dev.row(colIfOperStatus, 3, gosnmp.Integer, 1)
	dev.row(colIfHCInOctets, 3, gosnmp.OctetString, []byte("1"))
	// An ifDescr that is not a string: the interface has no name.
	dev.row(colIfDescr, 4, gosnmp.ObjectIdentifier, ".1.3.6.1")
	dev.row(colIfAdminStatus, 4, gosnmp.Integer, 1)
	dev.row(colIfOperStatus, 4, gosnmp.Integer, 1)
	// A row for an interface not in ifTable: ignored.
	dev.row(colIfName, 2, gosnmp.OctetString, []byte("ghost"))
	fp := newFakePoller(dev)
	m := byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantLabel(t, m, "snmp.if.status", "ok", "up")
	wantErr(t, m, "snmp.if.status", "odd types", errNoStatus)
	wantAbsent(t, m, "snmp.if.status", "ghost")
	wantLabel(t, m, "snmp.if.status", "if4", "up")
	if len(m) != 3+3 {
		t.Fatalf("%+v", m)
	}
}

func TestPollInterfaceTableUnreadable(t *testing.T) {
	dev := ifDevice()
	dev.walkErr[colIfDescr] = errFakeTimeout
	fp := newFakePoller(dev)
	m := byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantValue(t, m, "snmp.up", "", 1)
	wantErr(t, m, "snmp.if.status", "", errNoInterfaceTable)
	if len(m) != 4 {
		t.Fatalf("%+v", m)
	}
}

func TestPollColumnUnreadableDropsOnlyItsMetric(t *testing.T) {
	dev := ifDevice()
	fp := newFakePoller(dev)
	_, _ = poll(fp, ifParams, v2cSecret)
	advance(fp, dev)
	dev.walkErr[colIfOutErrors] = errFakeTimeout
	dev.walkErr[colIfOperStatus] = errFakeTimeout
	m := byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantValue(t, m, "snmp.if.in_bps", "lo", 1000)
	wantAbsent(t, m, "snmp.if.errors", "lo")
	wantErr(t, m, "snmp.if.status", "lo", errNoStatus)
}

// TestPollWalkReadsBoundedRows: a device that answers rows of no use (an
// index of several arcs) without end is not walked to its end.
func TestPollWalkReadsBoundedRows(t *testing.T) {
	dev := newFakeDevice()
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(1))
	dev.iface(1, "ok", "", 1, 1, 0, 0, true)
	for i := 1; i <= 20000; i++ {
		dev.row(colIfDescr, i, gosnmp.OctetString, []byte("x"))
		dev.columns[colIfDescr][len(dev.columns[colIfDescr])-1].Name = fmt.Sprintf("%s.0.%d", colIfDescr, i)
	}
	fp := newFakePoller(dev)
	m := byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantLabel(t, m, "snmp.if.status", "ok", "up")
	if limit := (maxWalkRows + 1) * len(ifColumns); dev.rows > limit {
		t.Fatalf("walks read %d rows, want at most %d", dev.rows, limit)
	}
}

// TestPollUnreadableTableKeepsThePreviousSample: a poll whose interface
// table could not be read leaves the sample before it, so the next poll
// has rates over both intervals.
func TestPollUnreadableTableKeepsThePreviousSample(t *testing.T) {
	dev := ifDevice()
	fp := newFakePoller(dev)
	_, _ = poll(fp, ifParams, v2cSecret)
	fp.now = fp.now.Add(time.Minute)
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(100000+6000))
	dev.walkErr[colIfDescr] = errFakeTimeout
	m := byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantErr(t, m, "snmp.if.status", "", errNoInterfaceTable)

	delete(dev.walkErr, colIfDescr)
	advance(fp, dev) // two minutes after the first poll
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(100000+12000))
	m = byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantValue(t, m, "snmp.if.in_bps", "lo", 500) // 7500 bytes in 120 s
}

// TestPollRateAboveTheSpeedIsDropped: a 32-bit counter cleared without a
// reboot reads as a wrap; on a 10 Mbit/s interface that rate is
// impossible and not reported.
func TestPollRateAboveTheSpeedIsDropped(t *testing.T) {
	dev := ifDevice()
	dev.row(colIfSpeed, 2, gosnmp.Gauge32, uint(10_000_000))
	fp := newFakePoller(dev)
	_, _ = poll(fp, ifParams, v2cSecret)
	advance(fp, dev)
	dev.row(colIfSpeed, 2, gosnmp.Gauge32, uint(10_000_000))
	// eth1's ifInOctets went from 2^32-100 to 650: read as a wrap of 750
	// bytes, 100 bit/s, under the speed. Clear it instead: 2^31 → 650.
	m := byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantValue(t, m, "snmp.if.in_bps", "eth1 descr", 100)

	dev.columns[colIfInOctets] = nil
	dev.row(colIfInOctets, 1, gosnmp.Counter32, uint(0))
	dev.row(colIfInOctets, 2, gosnmp.Counter32, uint(1<<31))
	dev.row(colIfInOctets, 3, gosnmp.Counter32, uint(0))
	fp.now = fp.now.Add(time.Minute)
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(100000+12000))
	_, _ = poll(fp, ifParams, v2cSecret)
	dev.columns[colIfInOctets] = nil
	dev.row(colIfInOctets, 1, gosnmp.Counter32, uint(0))
	dev.row(colIfInOctets, 2, gosnmp.Counter32, uint(650))
	dev.row(colIfInOctets, 3, gosnmp.Counter32, uint(0))
	fp.now = fp.now.Add(time.Minute)
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(100000+18000))
	m = byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantAbsent(t, m, "snmp.if.in_bps", "eth1 descr")
	wantAbsent(t, m, "snmp.if.errors", "eth1 descr")
	wantValue(t, m, "snmp.if.out_bps", "eth1 descr", 0)
}

// TestPollLoopbackAboveItsAdvertisedSpeed: net-snmp's lo advertises
// 10 Mbit/s; its 64-bit counters at 100 Mbit/s are reported as they are.
func TestPollLoopbackAboveItsAdvertisedSpeed(t *testing.T) {
	dev := ifDevice()
	dev.row(colIfSpeed, 1, gosnmp.Gauge32, uint(10_000_000))
	dev.row(colIfHighSpeed, 1, gosnmp.Gauge32, uint(10))
	fp := newFakePoller(dev)
	_, _ = poll(fp, ifParams, v2cSecret)
	fp.now = fp.now.Add(time.Minute)
	dev.columns = map[string][]gosnmp.SnmpPDU{}
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(100000+6000))
	dev.iface(1, "lo", "loopback", 1, 1, 1000+100e6/8*60, 2000, true)
	dev.row(colIfSpeed, 1, gosnmp.Gauge32, uint(10_000_000))
	dev.row(colIfHighSpeed, 1, gosnmp.Gauge32, uint(10))
	m := byKey(t, first(poll(fp, ifParams, v2cSecret)))
	wantValue(t, m, "snmp.if.in_bps", "lo", 100e6)
}
