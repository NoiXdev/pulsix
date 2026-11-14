package dns

import (
	"context"
	"net/netip"
	"testing"

	"github.com/noixdev/pulsix/internal/probe"
)

func TestParseDNSServer(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"192.0.2.53", "192.0.2.53:53", true},
		{"192.0.2.53:5353", "192.0.2.53:5353", true},
		{"2001:db8::53", "[2001:db8::53]:53", true},
		{"[2001:db8::53]", "[2001:db8::53]:53", true},
		{"[2001:db8::53]:5353", "[2001:db8::53]:5353", true},
		{"not-an-ip", "", false},
		{"192.0.2.53:0", "", false},
		{"dns.example:53", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseDNSServer(tc.in)
			if (err == nil) != tc.ok {
				t.Fatalf("parseDNSServer(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
			}
			if tc.ok && got != netip.MustParseAddrPort(tc.want) {
				t.Fatalf("parseDNSServer(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestAnswerMatches(t *testing.T) {
	tests := []struct {
		name    string
		answers []string
		expect  string
		want    bool
	}{
		{"exact ip", []string{"192.0.2.7"}, "192.0.2.7", true},
		{"other ip", []string{"192.0.2.7"}, "192.0.2.8", false},
		{"any of several", []string{"192.0.2.1", "192.0.2.7"}, "192.0.2.7", true},
		{"ipv6 spelled differently", []string{"2001:db8::1"}, "2001:DB8:0:0::1", true},
		{"name ignores case and trailing dot", []string{"Mail.Example.COM."}, "mail.example.com", true},
		{"expect with trailing dot", []string{"ns1.example.com."}, "NS1.example.com.", true},
		{"txt ignores case", []string{"v=spf1 -all"}, "V=SPF1 -ALL", true},
		{"substring is no match", []string{"mail.example.com."}, "example.com", false},
		{"no answers", nil, "x", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := answerMatches(tc.answers, tc.expect); got != tc.want {
				t.Fatalf("answerMatches(%q, %q) = %v, want %v", tc.answers, tc.expect, got, tc.want)
			}
		})
	}
}

func TestDNSUnsupportedTypeIsAnError(t *testing.T) {
	obs := probeDNS(context.Background(), probe.NewGuardForTest(true, nil), map[string]any{"name": "example.test", "type": "SRV"}, "", probe.NewGuardForTest(true, nil).Dialer)
	if len(obs) != 2 || obs[0].Err == "" || obs[1].Err == "" {
		t.Fatalf("want one error per metric, got %+v", obs)
	}
}

func TestDNSDialRefusalIsAnError(t *testing.T) {
	// The up-front check passes (the guard allows loopback) but the dialer
	// refuses, as a guard would for a server that is only refused at dial
	// time. Go's resolver hides the dial error, so the probe must remember it.
	params := map[string]any{"name": "example.test", "server": "127.0.0.1:53", "expect": "192.0.2.7", "timeout_s": float64(2)}
	obs := probeDNS(context.Background(), probe.NewGuardForTest(true, nil), params, "", probe.NewGuard(nil, nil).Dialer)
	if len(obs) != 3 {
		t.Fatalf("want one error per metric, got %+v", obs)
	}
	for _, o := range obs {
		if o.Value != nil || o.Err != probe.ErrNotAllowed.Error() {
			t.Fatalf("%s: value %v, err %q; want err %q", o.Metric, o.Value, o.Err, probe.ErrNotAllowed)
		}
	}
}
