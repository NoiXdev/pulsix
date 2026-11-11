package probe

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func TestTargetHost(t *testing.T) {
	tests := []struct {
		in, want string
		err      error
	}{
		{"example.com", "example.com", nil},
		{" example.com ", "example.com", nil},
		{"192.0.2.1", "192.0.2.1", nil},
		{"2001:db8::1", "2001:db8::1", nil},
		{"[2001:db8::1]", "2001:db8::1", nil},
		{"fe80::1%eth0", "fe80::1%eth0", nil},
		{"", "", errNoAddress},
		{"   ", "", errNoAddress},
		{"host:22", "", errBadTarget},
		{"[example.com]", "", errBadTarget},
		{"http://example.com", "", errBadTarget},
		{"exa mple.com", "", errBadTarget},
		{"example.com/path", "", errBadTarget},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := targetHost(tc.in)
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Fatalf("targetHost(%q) = %q, %v; want %q, %v", tc.in, got, err, tc.want, tc.err)
			}
		})
	}
}

func TestBoundUsesTheEarlierDeadline(t *testing.T) {
	ctx, cancel := bound(context.Background(), map[string]any{"timeout_s": float64(2)}, 5*time.Second)
	defer cancel()
	if dl, _ := ctx.Deadline(); time.Until(dl) > 2*time.Second || time.Until(dl) < time.Second {
		t.Fatalf("params timeout not applied: %v", time.Until(dl))
	}

	ctx, cancel = bound(context.Background(), nil, 5*time.Second)
	defer cancel()
	if dl, _ := ctx.Deadline(); time.Until(dl) > 5*time.Second || time.Until(dl) < 4*time.Second {
		t.Fatalf("default timeout not applied: %v", time.Until(dl))
	}

	parent, cancelParent := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelParent()
	ctx, cancel = bound(parent, map[string]any{"timeout_s": float64(30)}, 5*time.Second)
	defer cancel()
	if dl, _ := ctx.Deadline(); time.Until(dl) > 200*time.Millisecond {
		t.Fatalf("parent deadline not kept: %v", time.Until(dl))
	}
}

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
	obs := probeDNS(context.Background(), NewGuardForTest(true, nil), map[string]any{"name": "example.test", "type": "SRV"}, "", NewGuardForTest(true, nil).Dialer)
	if len(obs) != 2 || obs[0].Err == "" || obs[1].Err == "" {
		t.Fatalf("want one error per metric, got %+v", obs)
	}
}

func TestDNSDialRefusalIsAnError(t *testing.T) {
	// The up-front check passes (the guard allows loopback) but the dialer
	// refuses, as a guard would for a server that is only refused at dial
	// time. Go's resolver hides the dial error, so the probe must remember it.
	params := map[string]any{"name": "example.test", "server": "127.0.0.1:53", "expect": "192.0.2.7", "timeout_s": float64(2)}
	obs := probeDNS(context.Background(), NewGuardForTest(true, nil), params, "", NewGuard(nil).Dialer)
	if len(obs) != 3 {
		t.Fatalf("want one error per metric, got %+v", obs)
	}
	for _, o := range obs {
		if o.Value != nil || o.Err != ErrNotAllowed.Error() {
			t.Fatalf("%s: value %v, err %q; want err %q", o.Metric, o.Value, o.Err, ErrNotAllowed)
		}
	}
}
