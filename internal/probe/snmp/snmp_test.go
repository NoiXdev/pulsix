package snmp_test

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
	"github.com/noixdev/pulsix/internal/probe/snmp"
)

const testCommunity = "wire-test-community"

// agent is a v2c SNMP agent on loopback UDP that answers Get requests
// carrying community from values (a missing OID is NoSuchObject) and
// ignores everything else, as a device ignores a wrong community. With
// values nil it answers nothing at all. It returns its port.
func agent(t *testing.T, values map[string]gosnmp.SnmpPDU) int {
	t.Helper()
	return agentWith(t, values, nil, nil)
}

// bulkFunc answers a GetBulk for oid with up to n variables.
type bulkFunc func(oid string, n int) []gosnmp.SnmpPDU

// agentWith is agent that also answers GetBulk through bulk (nil: not at
// all) and counts the GetBulk requests in bulks.
func agentWith(t *testing.T, values map[string]gosnmp.SnmpPDU, bulk bulkFunc, bulks *atomic.Int64) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 65535)
		dec := &gosnmp.GoSNMP{Version: gosnmp.Version2c}
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if values == nil {
				continue
			}
			req, err := dec.SnmpDecodePacket(buf[:n])
			if err != nil || req.Community != testCommunity {
				continue
			}
			resp := &gosnmp.SnmpPacket{
				Version: gosnmp.Version2c, Community: req.Community, PDUType: gosnmp.GetResponse,
				RequestID: req.RequestID,
			}
			switch {
			case req.PDUType == gosnmp.GetRequest:
				for _, v := range req.Variables {
					pdu, ok := values[v.Name]
					if !ok {
						pdu = gosnmp.SnmpPDU{Type: gosnmp.NoSuchObject}
					}
					pdu.Name = v.Name
					resp.Variables = append(resp.Variables, pdu)
				}
			case req.PDUType == gosnmp.GetBulkRequest && bulk != nil && len(req.Variables) == 1:
				if bulks != nil {
					bulks.Add(1)
				}
				resp.Variables = bulk(req.Variables[0].Name, int(req.MaxRepetitions))
			default:
				continue
			}
			out, err := resp.MarshalMsg()
			if err != nil {
				continue
			}
			_, _ = conn.WriteTo(out, from)
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func wireParams(port int, timeout float64, oids ...any) map[string]any {
	return map[string]any{"secret": "wire", "port": float64(port), "timeout_seconds": timeout, "oids": oids}
}

func TestProbeOverTheWire(t *testing.T) {
	port := agent(t, map[string]gosnmp.SnmpPDU{
		".1.3.6.1.2.1.1.3.0": {Type: gosnmp.TimeTicks, Value: uint32(4200)},
		".1.3.6.1.2.1.1.5.0": {Type: gosnmp.OctetString, Value: []byte("core-sw\x00")},
		".1.3.6.1.2.1.2.1.0": {Type: gosnmp.Integer, Value: 24},
	})
	obs := snmp.Probe(context.Background(), probetest.LoopbackGuard(),
		wireParams(port, 2, "name=1.3.6.1.2.1.1.5.0", "ifs=1.3.6.1.2.1.2.1.0", "none=1.3.6.1.2.1.1.9.9"),
		"127.0.0.1", map[string]string{"community": testCommunity})
	got := map[string]probe.Observation{}
	for _, o := range obs {
		got[o.Metric+"/"+o.Item] = o
	}
	if o := got["snmp.up/"]; o.Value == nil || *o.Value != 1 {
		t.Fatalf("snmp.up: %+v (all %+v)", o, obs)
	}
	if o := got["snmp.uptime_seconds/"]; o.Value == nil || *o.Value != 42 {
		t.Fatalf("snmp.uptime_seconds: %+v", o)
	}
	if o := got["snmp.text/name"]; o.Label == nil || *o.Label != "core-sw" {
		t.Fatalf("snmp.text[name]: %+v", o)
	}
	if o := got["snmp.value/ifs"]; o.Value == nil || *o.Value != 24 {
		t.Fatalf("snmp.value[ifs]: %+v", o)
	}
	if o := got["snmp.value/none"]; o.Err != "the device has no such object" {
		t.Fatalf("snmp.value[none]: %+v", o)
	}
	if o := got["snmp.response_ms/"]; o.Value == nil || *o.Value < 0 {
		t.Fatalf("snmp.response_ms: %+v", o)
	}
}

func TestProbeWrongCommunityIsDown(t *testing.T) {
	port := agent(t, map[string]gosnmp.SnmpPDU{})
	obs := snmp.Probe(context.Background(), probetest.LoopbackGuard(), wireParams(port, 1, "x=1.3.6.1.2.1.1.5.0"),
		"127.0.0.1", map[string]string{"community": "not-the-community"})
	probetest.WantOnly(t, obs, "snmp.up", 0)
}

// TestProbeTimeout: a device that never answers is down after the request
// and its one retry, each timeout_seconds long.
func TestProbeTimeout(t *testing.T) {
	port := agent(t, nil)
	start := time.Now()
	obs := snmp.Probe(context.Background(), probetest.LoopbackGuard(), wireParams(port, 1, "x=1.3.6.1.2.1.1.5.0"),
		"127.0.0.1", map[string]string{"community": testCommunity})
	probetest.WantOnly(t, obs, "snmp.up", 0)
	if d := time.Since(start); d < 1500*time.Millisecond || d > 4*time.Second {
		t.Fatalf("took %v, want about two requests of 1 s", d)
	}
}

// TestProbeEndsWithItsContext: cancelling the probe's context ends a poll
// that is waiting for an answer, long before its timeout.
func TestProbeEndsWithItsContext(t *testing.T) {
	port := agent(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	obs := snmp.Probe(ctx, probetest.LoopbackGuard(), wireParams(port, 10, "x=1.3.6.1.2.1.1.5.0"),
		"127.0.0.1", map[string]string{"community": testCommunity})
	probetest.WantOnly(t, obs, "snmp.up", 0)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v after a cancel at 300 ms", d)
	}
}

// TestProbeGuardRefusal: the production guard refuses loopback; that is an
// error on every metric, and nothing is sent.
func TestProbeGuardRefusal(t *testing.T) {
	port := agent(t, map[string]gosnmp.SnmpPDU{})
	for _, address := range []string{"127.0.0.1", "localhost", "::ffff:127.0.0.1"} {
		obs := snmp.Probe(context.Background(), probe.NewGuard(nil, nil), wireParams(port, 1, "x=1.3.6.1.2.1.1.5.0"),
			address, map[string]string{"community": testCommunity})
		if len(obs) != 4 {
			t.Fatalf("%s: %+v", address, obs)
		}
		for _, o := range obs {
			if o.Err != probe.ErrNotAllowed.Error() {
				t.Fatalf("%s: %+v, want %q on every metric", address, o, probe.ErrNotAllowed)
			}
		}
	}
}

// TestProbeBadOIDOverTheWire: an entry SNMP cannot encode fails its own
// item; the device is up and the entry's siblings in the same request
// are answered.
func TestProbeBadOIDOverTheWire(t *testing.T) {
	port := agent(t, map[string]gosnmp.SnmpPDU{
		".1.3.6.1.2.1.1.3.0": {Type: gosnmp.TimeTicks, Value: uint32(100)},
		".1.3.6.1.2.1.2.1.0": {Type: gosnmp.Integer, Value: 3},
	})
	for _, bad := range []string{"3.6.1.2", "1.40.1", "2.4294967295"} {
		obs := snmp.Probe(context.Background(), probetest.LoopbackGuard(),
			wireParams(port, 1, "bad="+bad, "ifs=1.3.6.1.2.1.2.1.0"), "127.0.0.1", map[string]string{"community": testCommunity})
		got := map[string]probe.Observation{}
		for _, o := range obs {
			got[o.Metric+"/"+o.Item] = o
		}
		if o := got["snmp.up/"]; o.Value == nil || *o.Value != 1 {
			t.Fatalf("%s: snmp.up %+v (all %+v)", bad, o, obs)
		}
		if o := got["snmp.value/ifs"]; o.Value == nil || *o.Value != 3 {
			t.Fatalf("%s: sibling %+v", bad, o)
		}
		if o := got["snmp.value/bad"]; o.Err == "" || o.Value != nil {
			t.Fatalf("%s: %+v, want an error on its own item", bad, o)
		}
	}
}

const wireIfDescr = ".1.3.6.1.2.1.2.2.1.2"

// endless answers a walk of ifDescr with rows that never end: name gives
// the k-th row's OID. Every other column is empty.
func endless(name func(k int) string, k0 func(oid string) int) bulkFunc {
	return func(oid string, n int) []gosnmp.SnmpPDU {
		if !strings.HasPrefix(oid, wireIfDescr) {
			return []gosnmp.SnmpPDU{{Name: oid, Type: gosnmp.EndOfMibView}}
		}
		k := k0(oid)
		out := make([]gosnmp.SnmpPDU, 0, n)
		for i := 1; i <= n; i++ {
			out = append(out, gosnmp.SnmpPDU{Name: name(k + i), Type: gosnmp.OctetString, Value: []byte("x")})
		}
		return out
	}
}

// lastArc is the number after oid's last dot, 0 for the column itself.
func lastArc(oid string) int {
	if oid == wireIfDescr {
		return 0
	}
	n, _ := strconv.Atoi(oid[strings.LastIndex(oid, ".")+1:])
	return n
}

// TestProbeHostileWalks drives gosnmp's own BulkWalk against a device that
// never ends a column: the walks stop at the probe's bounds, and a column
// whose OIDs do not increase is an unreadable table, not a hang.
func TestProbeHostileWalks(t *testing.T) {
	sys := map[string]gosnmp.SnmpPDU{".1.3.6.1.2.1.1.3.0": {Type: gosnmp.TimeTicks, Value: uint32(100)}}
	run := func(bulk bulkFunc) ([]probe.Observation, snmp.Info, int64) {
		var bulks atomic.Int64
		port := agentWith(t, sys, bulk, &bulks)
		p := map[string]any{"secret": "wire", "port": float64(port), "timeout_seconds": float64(1), "interfaces": true}
		obs, info := snmp.NewPoller(nil).Poll(context.Background(), probetest.LoopbackGuard(), p, "127.0.0.1", map[string]string{"community": testCommunity})
		return obs, info, bulks.Load()
	}
	statuses := func(obs []probe.Observation) int {
		n := 0
		for _, o := range obs {
			if o.Metric == "snmp.if.status" {
				n++
			}
		}
		return n
	}

	// Valid rows without end: 128 interfaces, the rest skipped.
	obs, info, bulks := run(endless(func(k int) string { return fmt.Sprintf("%s.%d", wireIfDescr, k) }, lastArc))
	if !info.InterfacesSkipped || statuses(obs) != 128 {
		t.Fatalf("endless valid rows: skipped %v, %d statuses", info.InterfacesSkipped, statuses(obs))
	}
	if bulks > 20 {
		t.Fatalf("endless valid rows: %d GetBulk requests", bulks)
	}

	// Rows of no use (two arcs) without end: the walk stops at its row
	// bound and finds no interface.
	obs, _, bulks = run(endless(func(k int) string { return fmt.Sprintf("%s.0.%d", wireIfDescr, k) }, lastArc))
	if statuses(obs) != 0 || bulks > 45 {
		t.Fatalf("endless junk rows: %d statuses, %d GetBulk requests", statuses(obs), bulks)
	}

	// The same OID again and again: gosnmp refuses it as not increasing.
	obs, _, _ = run(func(string, int) []gosnmp.SnmpPDU {
		return []gosnmp.SnmpPDU{{Name: wireIfDescr + ".1", Type: gosnmp.OctetString, Value: []byte("x")}}
	})
	found := false
	for _, o := range obs {
		if o.Metric == "snmp.if.status" && o.Item == "" && o.Err != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("repeated OID: %+v", obs)
	}
}
