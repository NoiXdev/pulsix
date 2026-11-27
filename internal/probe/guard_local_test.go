package probe_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// localText is the fixed text of a refusal by an agent's own deny list
// (backlog 3.82): it never names the list or the range that matched.
const localText = "target address not allowed by the agent's local policy"

func prefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// TestGuardLocalDeny: an agent's own deny list (backlog 3.82) refuses with
// ErrLocalPolicy, which is still an ErrNotAllowed; the built-in policy's
// refusal wins where both apply, and WithLocalDeny leaves the original
// guard as it was.
func TestGuardLocalDeny(t *testing.T) {
	base := probe.NewGuard(nil, nil)
	g := base.WithLocalDeny(prefixes("10.0.0.0/8", "fd00::/8", "::ffff:192.168.0.0/112", "172.16.5.9/16"))
	tests := []struct {
		ip   string
		want error
	}{
		{"10.1.2.3", probe.ErrLocalPolicy},
		{"::ffff:10.1.2.3", probe.ErrLocalPolicy},
		{"fd12::1", probe.ErrLocalPolicy},
		{"192.168.4.4", probe.ErrLocalPolicy},    // a mapped /112 is 192.168.0.0/16
		{"172.16.200.1", probe.ErrLocalPolicy},   // host bits masked
		{"64:ff9b::a00:1", probe.ErrLocalPolicy}, // NAT64 embedding 10.0.0.1
		{"2002:a00:1::1", probe.ErrLocalPolicy},  // 6to4 embedding 10.0.0.1
		{"127.0.0.1", probe.ErrNotAllowed},       // built-in refusal wins
		{"192.0.2.1", nil},
		{"2001:db8::1", nil},
	}
	for _, tc := range tests {
		t.Run(tc.ip, func(t *testing.T) {
			ip := netip.MustParseAddr(tc.ip)
			err := g.Check(ip)
			if err != tc.want { //nolint:errorlint // the sentinel itself, not a wrap of it
				t.Fatalf("Check(%s) = %v, want %v", tc.ip, err, tc.want)
			}
			if g.Allowed(ip) != (tc.want == nil) {
				t.Fatalf("Allowed(%s) = %v, want %v", tc.ip, g.Allowed(ip), tc.want == nil)
			}
		})
	}
	if !errors.Is(probe.ErrLocalPolicy, probe.ErrNotAllowed) {
		t.Fatal("ErrLocalPolicy is not an ErrNotAllowed")
	}
	if probe.ErrLocalPolicy.Error() != localText {
		t.Fatalf("ErrLocalPolicy = %q, want %q", probe.ErrLocalPolicy, localText)
	}
	if !base.Allowed(netip.MustParseAddr("10.1.2.3")) {
		t.Fatal("WithLocalDeny changed the original guard")
	}
	if w := base.WithWrites(true).WithLocalDeny(prefixes("10.0.0.0/8")); !w.WritesAllowed() {
		t.Fatal("WithLocalDeny dropped the writes switch")
	}
	if w := g.WithWrites(true); w.Allowed(netip.MustParseAddr("10.1.2.3")) {
		t.Fatal("WithWrites dropped the local deny list")
	}
}

// TestGuardDenySeesThroughEmbeddedIPv4: PULSIX_PROBE_DENY_CIDRS judges an
// IPv4-in-IPv6 address by the IPv4 address it embeds, as the built-in
// policy does (backlog 3.82, R9).
func TestGuardDenySeesThroughEmbeddedIPv4(t *testing.T) {
	g := probe.NewGuard(prefixes("10.0.0.0/8"), nil)
	for _, ip := range []string{"64:ff9b::a00:1", "2002:a00:1::1", "::ffff:0:a00:1"} {
		if err := g.Check(netip.MustParseAddr(ip)); err != probe.ErrNotAllowed { //nolint:errorlint // the sentinel itself
			t.Errorf("Check(%s) = %v, want ErrNotAllowed", ip, err)
		}
	}
	nsp := prefixes("2001:db8:122::/48")
	// 10.0.0.1 at the /48 layout (RFC 6052 §2.2).
	op := probe.NewGuard(prefixes("10.0.0.0/8"), nsp)
	if op.Allowed(netip.MustParseAddr("2001:db8:122:a00:0:100::")) {
		t.Error("an operator NAT64 address embedding a denied address is allowed")
	}
	if !g.Allowed(netip.MustParseAddr("64:ff9b::c000:201")) {
		t.Error("a NAT64 address embedding an allowed address is refused")
	}
}

func TestGuardDialerReportsTheLocalRefusal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	g := probetest.LoopbackGuard().WithLocalDeny(prefixes("127.0.0.0/8"))
	_, err = g.Dialer(time.Second).DialContext(context.Background(), "tcp", ln.Addr().String())
	if !errors.Is(err, probe.ErrLocalPolicy) || !errors.Is(err, probe.ErrNotAllowed) {
		t.Fatalf("dial a locally denied address: err %v, want ErrLocalPolicy", err)
	}
}

// TestResolveAllowedLocalDeny: a name or literal whose every allowed
// address the local list refuses is ErrLocalPolicy; one the built-in
// policy refuses stays ErrNotAllowed.
func TestResolveAllowedLocalDeny(t *testing.T) {
	ctx := context.Background()
	g := probetest.LoopbackGuard().WithLocalDeny(prefixes("127.0.0.0/8", "::1/128"))
	for _, host := range []string{"127.0.0.1", "localhost"} {
		if _, err := probe.ResolveAllowed(ctx, g, host); !errors.Is(err, probe.ErrLocalPolicy) {
			t.Errorf("ResolveAllowed(%s) err = %v, want ErrLocalPolicy", host, err)
		}
	}
	if _, err := probe.ResolveAllowed(ctx, g, "169.254.169.254"); err != probe.ErrNotAllowed { //nolint:errorlint // the sentinel itself
		t.Errorf("a built-in refusal: err %v, want ErrNotAllowed", err)
	}
}

func TestRefusalText(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{probe.ErrNotAllowed, "target address not allowed"},
		{probe.ErrLocalPolicy, localText},
		{fmt.Errorf("dial tcp: %w", probe.ErrLocalPolicy), localText},
		{&net.OpError{Op: "dial", Err: probe.ErrNotAllowed}, "target address not allowed"},
	}
	for _, tc := range tests {
		if got := probe.RefusalText(tc.err); got != tc.want {
			t.Errorf("RefusalText(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestRefusalRecorder: any local refusal among a probe's dials makes the
// text the local one; no refusal at all records nothing.
func TestRefusalRecorder(t *testing.T) {
	var r probe.Refusal
	r.Note(errors.New("connection refused"))
	r.Note(nil)
	if r.Refused() {
		t.Fatal("an ordinary failure was recorded as a refusal")
	}
	r.Note(probe.ErrNotAllowed)
	if !r.Refused() || r.Text() != "target address not allowed" {
		t.Fatalf("after a built-in refusal: %v %q", r.Refused(), r.Text())
	}
	r.Note(&net.OpError{Op: "dial", Err: probe.ErrLocalPolicy})
	if r.Text() != localText {
		t.Fatalf("after a local refusal: %q", r.Text())
	}
	r.Note(probe.ErrNotAllowed)
	if r.Text() != localText {
		t.Fatalf("a later built-in refusal replaced the local text: %q", r.Text())
	}
}
