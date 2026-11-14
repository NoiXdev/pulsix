package snmp_test

import (
	"context"
	"net"
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
			if err != nil || req.Community != testCommunity || req.PDUType != gosnmp.GetRequest {
				continue
			}
			resp := &gosnmp.SnmpPacket{
				Version: gosnmp.Version2c, Community: req.Community, PDUType: gosnmp.GetResponse,
				RequestID: req.RequestID,
			}
			for _, v := range req.Variables {
				pdu, ok := values[v.Name]
				if !ok {
					pdu = gosnmp.SnmpPDU{Type: gosnmp.NoSuchObject}
				}
				pdu.Name = v.Name
				resp.Variables = append(resp.Variables, pdu)
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
