package sshprobe

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/credentials"
)

func TestFirstLineTable(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   \n  ", ""},
		{"sudo: a password is required", "sudo: a password is required"},
		{"first\nsecond\nthird", "first"},
		{"first", "first"},
		{strings.Repeat("x", 300), strings.Repeat("x", 120)},
		{strings.Repeat("x", 130) + "\nnext", strings.Repeat("x", 120)},
	}
	for _, c := range cases {
		if got := firstLine(c.in); got != c.want {
			t.Errorf("firstLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestJoinHostPortDefaultsPort(t *testing.T) {
	if got := joinHostPort("host", 0); got != "host:22" {
		t.Fatalf("joinHostPort(host, 0) = %q", got)
	}
	if got := joinHostPort("host", 2222); got != "host:2222" {
		t.Fatalf("joinHostPort(host, 2222) = %q", got)
	}
	if got := joinHostPort("::1", 2222); got != "[::1]:2222" {
		t.Fatalf("IPv6 join = %q", got)
	}
}

func TestProbeZeroValueDefaults(t *testing.T) {
	p := &Probe{}
	if p.dialTimeout() != DefaultDialTimeout {
		t.Fatalf("dialTimeout() = %v, want %v", p.dialTimeout(), DefaultDialTimeout)
	}
	if p.now().IsZero() {
		t.Fatal("now() must fall back to the wall clock")
	}
}

func TestAuthMethodsBadKeyFailsClosed(t *testing.T) {
	if _, err := authMethods(credentials.Secret{PrivateKey: "not a pem"}); err == nil {
		t.Fatal("an unparseable private key must fail the claim")
	}
}

func TestHopErrorClassification(t *testing.T) {
	inner := errors.New("refused")
	wrapped := &hopError{index: 2, err: inner}
	if got := wrapped.Error(); !strings.Contains(got, "hop 2") || !strings.Contains(got, "refused") {
		t.Fatalf("hopError text = %q", got)
	}
	if !errors.Is(wrapped, inner) {
		t.Fatal("hopError must unwrap to its cause")
	}
	if !isHopFailure(wrapped) {
		t.Fatal("classification failed")
	}
	if isHopFailure(inner) {
		t.Fatal("a plain error is not a hop failure")
	}
}

func TestExitStatusFallback(t *testing.T) {
	if got := exitStatus(errors.New("i/o timeout")); got != -1 {
		t.Fatalf("exitStatus(non-exit) = %d", got)
	}
}

// TestExecDeadlineBoundsHungCommand drives the exec deadline: a target
// whose python3 never returns must yield a failed python check (i/o
// timeout, not an exit status) and a finite probe.
func TestExecDeadlineBoundsHungCommand(t *testing.T) {
	key := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key), withPythonHang())
	tgt := srv.asTarget()
	p := &Probe{DialTimeout: 300 * time.Millisecond, Now: time.Now}
	res, err := p.Run(context.Background(), ProbeTarget{
		Address: tgt.Address, Port: tgt.Port, Username: "ansible", Credential: key.secret(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sudo != true {
		t.Fatalf("sudo should have completed before the deadline: %+v", res)
	}
	if res.Python || res.OK() {
		t.Fatalf("a hanging python must fail the test: %+v", res)
	}
	if d := wantCheck(t, res, "python", false); regexp.MustCompile(`exit status \d`).MatchString(d) {
		t.Fatalf("the deadline path must not look like an exit status code: %q", d)
	}
}

// TestExecSessionFailureAfterDeadConnection pins the open-session error
// path of exec: a client whose connection died reports the session failure
// as the check's detail.
func TestExecSessionFailureAfterDeadConnection(t *testing.T) {
	key := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key))
	tgt := srv.asTarget()
	p := newProbe()
	auth, err := authMethods(key.secret())
	if err != nil {
		t.Fatal(err)
	}
	var seen observedKey
	cfg := &ssh.ClientConfig{
		User:            "ansible",
		Auth:            auth,
		HostKeyCallback: hostKeyCallback("", &seen),
		Timeout:         p.dialTimeout(),
	}
	client, conn, err := p.dial(context.Background(), nil, joinHostPort(tgt.Address, tgt.Port), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close() // kill the socket under the client
	check := p.exec(client, "sudo", sudoCommand)
	if check.OK {
		t.Fatalf("exec over a dead connection must fail: %+v", check)
	}
	if !strings.Contains(check.Detail, "open session") {
		t.Fatalf("detail = %q", check.Detail)
	}
}

func TestUnreachableDialReportsUnreachable(t *testing.T) {
	// An unresolvable address fails the TCP dial fast.
	res, err := newProbe().Run(context.Background(), ProbeTarget{
		Address: "no-such-host.invalid", Port: 22, Username: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reachable || res.OK() || res.HostKey != "" {
		t.Fatalf("unreachable host must report unreachable: %+v", res)
	}
}

// TestCancelledDuringDialAborts exercises cancellation racing the dial:
// the outcome is either a dial error naming the context, or a probe that
// connected and then saw its session die when the watcher closed the
// client — both are the cancelled-context shape, never a clean ok.
func TestCancelledDuringDialAborts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := asAddr(ln.Addr().String())
	t.Cleanup(func() { _ = ln.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	res, err := newProbe().Run(ctx, ProbeTarget{Address: addr.Address, Port: addr.Port})
	switch {
	case err != nil:
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("dial error = %v, want the context cancellation", err)
		}
	case res.OK():
		t.Fatalf("a cancelled probe must never report ok: %+v", res)
	}
}
