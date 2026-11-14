package snmp

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/noixdev/pulsix/internal/probe"
)

// fakeDevice is a client that answers from tables instead of the network.
type fakeDevice struct {
	mu sync.Mutex
	// values answers Get by OID (leading dot); a missing OID is
	// NoSuchObject.
	values map[string]gosnmp.SnmpPDU
	// columns answers BulkWalk by root OID, rows in any order (the fake
	// sorts them like a device would).
	columns map[string][]gosnmp.SnmpPDU
	// getErr fails every Get from the getOK+1st on.
	getErr error
	getOK  int
	// pduError is the error status of every Get answer.
	pduError gosnmp.SNMPError
	// walkErr fails the walk of a root.
	walkErr map[string]error
	// replied is what answered reports.
	replied bool

	gets   [][]string
	walked []string
	rows   int // rows handed to walk callbacks
	closed bool
}

func newFakeDevice() *fakeDevice {
	return &fakeDevice{values: map[string]gosnmp.SnmpPDU{}, columns: map[string][]gosnmp.SnmpPDU{}, walkErr: map[string]error{}}
}

func (f *fakeDevice) get(oids []string) (*gosnmp.SnmpPacket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets = append(f.gets, oids)
	if f.getErr != nil && len(f.gets) > f.getOK {
		return nil, f.getErr
	}
	pkt := &gosnmp.SnmpPacket{Error: f.pduError}
	for _, o := range oids {
		pdu, ok := f.values[o]
		if !ok {
			pdu = gosnmp.SnmpPDU{Name: o, Type: gosnmp.NoSuchObject}
		}
		pdu.Name = o
		pkt.Variables = append(pkt.Variables, pdu)
	}
	return pkt, nil
}

func (f *fakeDevice) walk(root string, fn gosnmp.WalkFunc) error {
	f.mu.Lock()
	f.walked = append(f.walked, root)
	rows := append([]gosnmp.SnmpPDU(nil), f.columns[root]...)
	err := f.walkErr[root]
	f.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return lastArc(rows[i].Name) < lastArc(rows[j].Name) })
	for _, r := range rows {
		f.mu.Lock()
		f.rows++
		f.mu.Unlock()
		if err := fn(r); err != nil {
			return err
		}
	}
	return err
}

func (f *fakeDevice) answered() bool { return f.replied }

func (f *fakeDevice) close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

func lastArc(oid string) int {
	var n int
	_, _ = fmt.Sscanf(oid[strings.LastIndex(oid, ".")+1:], "%d", &n)
	return n
}

// set answers oid with a value of typ.
func (f *fakeDevice) set(oid string, typ gosnmp.Asn1BER, v any) {
	f.values[oid] = gosnmp.SnmpPDU{Name: oid, Type: typ, Value: v}
}

// row adds column's value for ifIndex idx.
func (f *fakeDevice) row(column string, idx int, typ gosnmp.Asn1BER, v any) {
	f.columns[column] = append(f.columns[column], gosnmp.SnmpPDU{Name: fmt.Sprintf("%s.%d", column, idx), Type: typ, Value: v})
}

// iface adds an interface with its names, statuses and octet and error
// counters (64-bit octets when hc).
func (f *fakeDevice) iface(idx int, name, descr string, admin, oper int, in, out uint64, hc bool) {
	if name != "" {
		f.row(colIfName, idx, gosnmp.OctetString, []byte(name))
	}
	f.row(colIfDescr, idx, gosnmp.OctetString, []byte(descr))
	f.row(colIfAdminStatus, idx, gosnmp.Integer, admin)
	f.row(colIfOperStatus, idx, gosnmp.Integer, oper)
	f.row(colIfInOctets, idx, gosnmp.Counter32, uint(in&0xffffffff))
	f.row(colIfOutOctets, idx, gosnmp.Counter32, uint(out&0xffffffff))
	f.row(colIfInErrors, idx, gosnmp.Counter32, uint(0))
	f.row(colIfOutErrors, idx, gosnmp.Counter32, uint(0))
	if hc {
		f.row(colIfHCInOctets, idx, gosnmp.Counter64, in)
		f.row(colIfHCOutOctets, idx, gosnmp.Counter64, out)
	}
}

// fakePoller is a Poller whose dial hands out dev and records what it was
// given, on a clock the test moves.
type fakePoller struct {
	*Poller
	dev     *fakeDevice
	now     time.Time
	dialed  []dialed
	dialErr error
}

type dialed struct {
	target  netip.AddrPort
	timeout time.Duration
	creds   credentials
}

func newFakePoller(dev *fakeDevice) *fakePoller {
	fp := &fakePoller{dev: dev, now: t0}
	fp.Poller = NewPoller(func() time.Time { return fp.now })
	fp.dial = func(_ context.Context, _ *probe.Guard, target netip.AddrPort, timeout time.Duration, c credentials) (client, error) {
		fp.dialed = append(fp.dialed, dialed{target, timeout, c})
		if fp.dialErr != nil {
			return nil, fp.dialErr
		}
		return dev, nil
	}
	return fp
}

// key names one observation: metric and item.
type key struct{ metric, item string }

// byKey indexes observations by metric and item and fails on a duplicate
// or an observation that is not exactly one of value, label and error.
func byKey(t *testing.T, obs []probe.Observation) map[key]probe.Observation {
	t.Helper()
	m := make(map[key]probe.Observation, len(obs))
	for _, o := range obs {
		k := key{o.Metric, o.Item}
		if _, dup := m[k]; dup {
			t.Fatalf("%v reported twice: %+v", k, obs)
		}
		n := 0
		if o.Value != nil {
			n++
		}
		if o.Label != nil {
			n++
		}
		if o.Err != "" {
			n++
		}
		if n != 1 {
			t.Fatalf("%v must carry exactly one of value, label and error: %+v", k, o)
		}
		m[k] = o
	}
	return m
}

func wantValue(t *testing.T, m map[key]probe.Observation, metric, item string, want float64) {
	t.Helper()
	o, ok := m[key{metric, item}]
	if !ok || o.Value == nil {
		t.Fatalf("%s[%s]: %+v, want value %v", metric, item, o, want)
	}
	if *o.Value != want {
		t.Fatalf("%s[%s] = %v, want %v", metric, item, *o.Value, want)
	}
}

func wantLabel(t *testing.T, m map[key]probe.Observation, metric, item, want string) {
	t.Helper()
	o, ok := m[key{metric, item}]
	if !ok || o.Label == nil {
		t.Fatalf("%s[%s]: %+v, want label %q", metric, item, o, want)
	}
	if *o.Label != want {
		t.Fatalf("%s[%s] = %q, want %q", metric, item, *o.Label, want)
	}
}

func wantErr(t *testing.T, m map[key]probe.Observation, metric, item, want string) {
	t.Helper()
	o, ok := m[key{metric, item}]
	if !ok || o.Err == "" {
		t.Fatalf("%s[%s]: %+v, want error %q", metric, item, o, want)
	}
	if o.Err != want {
		t.Fatalf("%s[%s] error %q, want %q", metric, item, o.Err, want)
	}
}

func wantAbsent(t *testing.T, m map[key]probe.Observation, metric, item string) {
	t.Helper()
	if o, ok := m[key{metric, item}]; ok {
		t.Fatalf("%s[%s] reported: %+v", metric, item, o)
	}
}

var errFakeTimeout = errors.New("request timeout (after 1 retries)")
