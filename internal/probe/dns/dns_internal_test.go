package dns

import (
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"syscall"
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
	if len(obs) != 3 || obs[0].Err == "" || obs[1].Err == "" || obs[2].Err == "" {
		t.Fatalf("want one error per metric, got %+v", obs)
	}
}

func TestDNSDialRefusalIsAnError(t *testing.T) {
	// The up-front check passes (the guard allows loopback) but the dialer
	// refuses, as a guard would for a server that is only refused at dial
	// time. Go's resolver hides the dial error, so the probe must remember it.
	params := map[string]any{"name": "example.test", "server": "127.0.0.1:53", "expect": "192.0.2.7", "timeout_s": float64(2)}
	obs := probeDNS(context.Background(), probe.NewGuardForTest(true, nil), params, "", probe.NewGuard(nil, nil).Dialer)
	if len(obs) != 4 {
		t.Fatalf("want one error per metric, got %+v", obs)
	}
	for _, o := range obs {
		if o.Value != nil || o.Err != probe.ErrNotAllowed.Error() {
			t.Fatalf("%s: value %v, err %q; want err %q", o.Metric, o.Value, o.Err, probe.ErrNotAllowed)
		}
	}
}

func TestLookupFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		conn string
		want string
	}{
		{"no answer without an error", nil, "", probe.FailureNXDomain},
		{"not found", &net.DNSError{Err: "no such host", IsNotFound: true}, probe.FailureTimeout, probe.FailureNXDomain},
		{"error code", &net.DNSError{Err: "server misbehaving", IsTemporary: true}, "", probe.FailureServFail},
		{"connection refused", &net.DNSError{Err: "read udp: connection refused", IsTemporary: true}, probe.FailureRefused, probe.FailureRefused},
		{"timed out", &net.DNSError{Err: "i/o timeout", IsTimeout: true}, "", probe.FailureTimeout},
		{"wrapped deadline", &net.DNSError{Err: "x", UnwrapErr: context.DeadlineExceeded}, "", probe.FailureTimeout},
		{"unparsable answer", &net.DNSError{Err: "cannot unmarshal DNS message"}, "", probe.FailureError},
		{"no DNSError", context.DeadlineExceeded, "", probe.FailureTimeout},
	} {
		if got := lookupFailure(tc.err, tc.conn); got != tc.want {
			t.Errorf("%s: lookupFailure = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestIOFailureStaysInTheDNSVocabulary: a reset or an early close of the
// TCP fallback is no dns label, so it is not noted.
func TestIOFailureStaysInTheDNSVocabulary(t *testing.T) {
	var f ioFailure
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, &net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNRESET)}} {
		f.note(err)
	}
	if f.label() != "" {
		t.Fatalf("label = %q, want none noted", f.label())
	}
	f.note(&net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNREFUSED)})
	if f.label() != probe.FailureRefused {
		t.Fatalf("label = %q, want refused", f.label())
	}
}

func TestIOFailureKeepsKnownLabels(t *testing.T) {
	var f ioFailure
	f.note(nil)
	if f.label() != "" {
		t.Fatalf("label after nil = %q", f.label())
	}
	f.note(os.ErrDeadlineExceeded)
	f.note(net.ErrClosed)
	if f.label() != probe.FailureTimeout {
		t.Fatalf("label = %q, want timeout kept over an unknown failure", f.label())
	}
}

// TestAnswersText: the answers sorted and joined, names lower-cased
// without the trailing dot, addresses as they come (already canonical),
// TXT strings as received.
func TestAnswersText(t *testing.T) {
	tests := []struct {
		typ     string
		answers []string
		want    string
	}{
		{"A", []string{"192.0.2.9", "192.0.2.10"}, "192.0.2.10, 192.0.2.9"},
		{"AAAA", []string{"2001:db8::1"}, "2001:db8::1"},
		{"MX", []string{"Mail2.Example.COM.", "mail1.example.com."}, "mail1.example.com, mail2.example.com"},
		{"CNAME", []string{"Target.Example."}, "target.example"},
		{"NS", []string{"ns1.example.", "ns0.example."}, "ns0.example, ns1.example"},
		{"TXT", []string{"v=spf1 -all", "Google-Site-Verification=AbC"}, "Google-Site-Verification=AbC, v=spf1 -all"},
	}
	for _, tc := range tests {
		if got := answersText(tc.typ, tc.answers); got != tc.want {
			t.Errorf("answersText(%s, %q) = %q, want %q", tc.typ, tc.answers, got, tc.want)
		}
	}
}

// TestMatchingAnswer is answerMatches naming the answer it found, as
// answersText prints it.
func TestMatchingAnswer(t *testing.T) {
	tests := []struct {
		typ, want string
		answers   []string
		found     string
		ok        bool
	}{
		{"AAAA", "2001:DB8:0:0::1", []string{"2001:db8::1"}, "2001:db8::1", true},
		{"MX", "mail.example.com", []string{"Mail.Example.COM."}, "mail.example.com", true},
		{"TXT", "V=SPF1 -ALL", []string{"v=spf1 -all"}, "v=spf1 -all", true},
		{"A", "192.0.2.1", []string{"192.0.2.10"}, "", false},
	}
	for _, tc := range tests {
		found, ok := matchingAnswer(tc.typ, tc.answers, tc.want)
		if found != tc.found || ok != tc.ok {
			t.Errorf("matchingAnswer(%s, %q, %q) = %q %v, want %q %v", tc.typ, tc.answers, tc.want, found, ok, tc.found, tc.ok)
		}
	}
}
