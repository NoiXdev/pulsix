package probe_test

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/probe"
)

// dnsStub is a minimal authoritative DNS server on loopback UDP, hand-rolled
// from the RFC 1035 wire format. It answers A queries for the names in its
// zone, an empty NOERROR for any other type of a known name, and NXDOMAIN for
// every unknown name.
type dnsStub struct {
	zone map[string][]netip.Addr
}

const (
	dnsTypeA   = 1
	dnsClassIN = 1
)

func startDNSStub(t *testing.T, zone map[string][]netip.Addr) (server string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	s := &dnsStub{zone: zone}
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if resp := s.answer(buf[:n]); resp != nil {
				_, _ = pc.WriteTo(resp, from)
			}
		}
	}()
	return "127.0.0.1:" + strconv.Itoa(pc.LocalAddr().(*net.UDPAddr).Port)
}

// answer builds the response to one query, or nil for a malformed one.
func (s *dnsStub) answer(req []byte) []byte {
	if len(req) < 12 || binary.BigEndian.Uint16(req[4:6]) != 1 {
		return nil
	}
	// Walk the question's name: uncompressed labels up to the root.
	off := 12
	var labels []string
	for {
		if off >= len(req) {
			return nil
		}
		l := int(req[off])
		off++
		if l == 0 {
			break
		}
		if l&0xC0 != 0 || off+l > len(req) {
			return nil
		}
		labels = append(labels, strings.ToLower(string(req[off:off+l])))
		off += l
	}
	if off+4 > len(req) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(req[off : off+2])
	question := req[12 : off+4]
	name := strings.Join(labels, ".")

	addrs, known := s.zone[name]
	rcode := uint16(0)
	if !known {
		rcode = 3 // NXDOMAIN
	}
	var answers []netip.Addr
	if known && qtype == dnsTypeA {
		answers = addrs
	}

	flags := uint16(0x8000) | // QR
		binary.BigEndian.Uint16(req[2:4])&0x7900 | // opcode and RD from the query
		0x0400 | // AA
		0x0080 | // RA
		rcode
	resp := make([]byte, 12, 512)
	binary.BigEndian.PutUint16(resp[0:2], binary.BigEndian.Uint16(req[0:2]))
	binary.BigEndian.PutUint16(resp[2:4], flags)
	binary.BigEndian.PutUint16(resp[4:6], 1)
	binary.BigEndian.PutUint16(resp[6:8], uint16(len(answers)))
	resp = append(resp, question...)
	for _, a := range answers {
		ip := a.As4()
		resp = append(resp, 0xC0, 0x0C) // pointer to the question's name
		resp = binary.BigEndian.AppendUint16(resp, dnsTypeA)
		resp = binary.BigEndian.AppendUint16(resp, dnsClassIN)
		resp = binary.BigEndian.AppendUint32(resp, 60)
		resp = binary.BigEndian.AppendUint16(resp, 4)
		resp = append(resp, ip[:]...)
	}
	return resp
}

func exampleZone() map[string][]netip.Addr {
	return map[string][]netip.Addr{"example.test": {netip.MustParseAddr("192.0.2.7")}}
}

func dnsParams(server string, extra map[string]any) map[string]any {
	p := map[string]any{"server": server, "timeout_s": float64(2)}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func TestDNSAnswered(t *testing.T) {
	server := startDNSStub(t, exampleZone())
	obs := probe.Run(context.Background(), loopbackGuard(), "dns", dnsParams(server, map[string]any{"name": "example.test"}), "")
	m := byMetric(t, obs)
	if len(m) != 2 {
		t.Fatalf("want up and resolve_ms only, got %+v", obs)
	}
	if v := wantValue(t, m, "dns.up"); v != 1 {
		t.Fatalf("dns.up = %v, want 1", v)
	}
	if v := wantValue(t, m, "dns.resolve_ms"); v < 0 {
		t.Fatalf("dns.resolve_ms = %v, want >= 0", v)
	}
}

func TestDNSExpect(t *testing.T) {
	server := startDNSStub(t, exampleZone())
	tests := []struct {
		expect string
		want   float64
	}{
		{"192.0.2.7", 1},
		{"192.0.2.8", 0},
	}
	for _, tc := range tests {
		t.Run(tc.expect, func(t *testing.T) {
			obs := probe.Run(context.Background(), loopbackGuard(), "dns",
				dnsParams(server, map[string]any{"name": "example.test", "type": "A", "expect": tc.expect}), "")
			m := byMetric(t, obs)
			if v := wantValue(t, m, "dns.up"); v != 1 {
				t.Fatalf("dns.up = %v, want 1", v)
			}
			if v := wantValue(t, m, "dns.answer_match"); v != tc.want {
				t.Fatalf("dns.answer_match = %v, want %v", v, tc.want)
			}
		})
	}
}

func TestDNSNXDOMAINIsDown(t *testing.T) {
	server := startDNSStub(t, exampleZone())
	obs := probe.Run(context.Background(), loopbackGuard(), "dns",
		dnsParams(server, map[string]any{"name": "missing.test", "expect": "192.0.2.7"}), "")
	wantOnly(t, obs, "dns.up", 0)
}

func TestDNSNoRecordsIsDown(t *testing.T) {
	server := startDNSStub(t, exampleZone())
	obs := probe.Run(context.Background(), loopbackGuard(), "dns",
		dnsParams(server, map[string]any{"name": "example.test", "type": "AAAA"}), "")
	wantOnly(t, obs, "dns.up", 0)
}

func TestDNSNameSubstitutesAddress(t *testing.T) {
	server := startDNSStub(t, exampleZone())
	tests := []struct {
		name    string
		params  map[string]any
		address string
	}{
		{"default name is the address", nil, "example.test"},
		{"explicit placeholder", map[string]any{"name": "{address}"}, "example.test"},
		{"placeholder inside a name", map[string]any{"name": "{address}.test"}, "example"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := probe.Run(context.Background(), loopbackGuard(), "dns", dnsParams(server, tc.params), tc.address)
			if v := wantValue(t, byMetric(t, obs), "dns.up"); v != 1 {
				t.Fatalf("dns.up = %v, want 1", v)
			}
		})
	}
}

func TestDNSEmptyAddressIsAnErrorOnlyWhenNeeded(t *testing.T) {
	server := startDNSStub(t, exampleZone())
	obs := probe.Run(context.Background(), loopbackGuard(), "dns", dnsParams(server, map[string]any{"expect": "x"}), "")
	wantErrors(t, obs, "host has no address", "dns.up", "dns.resolve_ms", "dns.answer_match")

	obs = probe.Run(context.Background(), loopbackGuard(), "dns", dnsParams(server, map[string]any{"name": "example.test"}), "")
	if v := wantValue(t, byMetric(t, obs), "dns.up"); v != 1 {
		t.Fatalf("a fixed name needs no address: dns.up = %v, want 1", v)
	}
}

func TestDNSRefusedServerIsAnError(t *testing.T) {
	server := startDNSStub(t, exampleZone())
	obs := probe.Run(context.Background(), probe.NewGuard(nil), "dns",
		dnsParams(server, map[string]any{"name": "example.test", "expect": "192.0.2.7"}), "")
	wantErrors(t, obs, "target address not allowed", "dns.up", "dns.resolve_ms", "dns.answer_match")
}

func TestDNSUnparsableServerIsAnError(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "dns", dnsParams("not-an-ip", map[string]any{"name": "example.test"}), "")
	m := byMetric(t, obs)
	if len(m) != 2 || m["dns.up"].Err == "" || m["dns.resolve_ms"].Err == "" {
		t.Fatalf("want one error per metric, got %+v", obs)
	}
}

func TestDNSOtherTypesUnansweredAreDown(t *testing.T) {
	server := startDNSStub(t, exampleZone())
	for _, typ := range []string{"AAAA", "CNAME", "MX", "TXT", "NS"} {
		t.Run(typ, func(t *testing.T) {
			obs := probe.Run(context.Background(), loopbackGuard(), "dns",
				dnsParams(server, map[string]any{"name": "missing.test", "type": typ, "expect": "x"}), "")
			wantOnly(t, obs, "dns.up", 0)
		})
	}
}

func TestDNSNameThatIsAnAddressIsAnError(t *testing.T) {
	// The resolver answers an IP literal itself without asking the server,
	// so an A or AAAA check of one would read up whatever the server does.
	server := startDNSStub(t, exampleZone())
	tests := []struct {
		name    string
		params  map[string]any
		address string
		metrics []string
	}{
		{"default name on an ipv4 host", nil, "192.0.2.1", []string{"dns.up", "dns.resolve_ms"}},
		{"default name on a bracketed ipv6 host", map[string]any{"type": "AAAA"}, "[2001:db8::1]", []string{"dns.up", "dns.resolve_ms"}},
		{"explicit ipv6 name", map[string]any{"name": "2001:db8::1", "type": "AAAA", "expect": "2001:db8::1"}, "", []string{"dns.up", "dns.resolve_ms", "dns.answer_match"}},
		{"explicit ipv4 name", map[string]any{"name": "192.0.2.7", "type": "A"}, "", []string{"dns.up", "dns.resolve_ms"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := probe.Run(context.Background(), loopbackGuard(), "dns", dnsParams(server, tc.params), tc.address)
			wantErrors(t, obs, "name is an address", tc.metrics...)
		})
	}
}
