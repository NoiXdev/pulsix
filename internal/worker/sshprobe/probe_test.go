package sshprobe

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	glssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/credentials"
)

// The fixtures: an in-process target SSH server (gliderlabs/ssh, the
// brief-pinned test-only dependency — it integrates with the x/crypto
// client this package is) and an in-process bastion (x/crypto's server
// side) that answers direct-tcpip channel opens, which is the channel type
// a jump chain's later hops are dialed through.

// keyFixture is one ed25519 keypair in the two forms the probe and the
// fixtures need: the signer (server side) and the PEM private key (the
// credential the probe authenticates with).
type keyFixture struct {
	signer ssh.Signer
	pem    string
}

func newKey(t *testing.T) keyFixture {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return keyFixture{signer: signer, pem: string(pem.EncodeToMemory(block))}
}

func (k keyFixture) secret() credentials.Secret {
	return credentials.Secret{PrivateKey: k.pem}
}

// probeServer is the target fixture.
type probeServer struct {
	addr string
	// authAttempts counts the server-side authentication handler calls —
	// the fail-closed assertion (a changed host key aborts before any
	// auth) reads it.
	authAttempts atomic.Int32

	sudoExit    int
	pythonExit  int
	pythonHangs bool
	password    string
	authorized  ssh.PublicKey
}

type serverOption func(*probeServer)

// withSudoExit makes `sudo -n true` exit with the code (0 = passwordless
// sudo works, the default).
func withSudoExit(code int) serverOption {
	return func(s *probeServer) { s.sudoExit = code }
}

// withPythonExit makes `python3 -c 'print(1)'` exit with the code (0 =
// Python present, the default).
func withPythonExit(code int) serverOption {
	return func(s *probeServer) { s.pythonExit = code }
}

// withPythonHang makes `python3 -c 'print(1)'` never return — the exec
// deadline's test target.
func withPythonHang() serverOption {
	return func(s *probeServer) { s.pythonHangs = true }
}

// withPassword switches the fixture to password authentication.
func withPassword(password string) serverOption {
	return func(s *probeServer) { s.password = password }
}

// withAuthorizedKey replaces the accepted client key (the default: the key
// the test itself generates).
func withAuthorizedKey(key keyFixture) serverOption {
	return func(s *probeServer) { s.authorized = key.signer.PublicKey() }
}

func startProbeServer(t *testing.T, opts ...serverOption) *probeServer {
	t.Helper()
	s := &probeServer{}
	for _, o := range opts {
		o(s)
	}
	srv := &glssh.Server{
		Handler: func(sess glssh.Session) {
			cmd := sess.Command()
			switch {
			case len(cmd) > 0 && cmd[0] == "sudo" && s.sudoExit != 0:
				_ = sess.Exit(s.sudoExit)
			case len(cmd) > 0 && cmd[0] == "python3" && s.pythonHangs:
				<-sess.Context().Done() // the deadline or the client close ends it
			case len(cmd) > 0 && cmd[0] == "python3" && s.pythonExit != 0:
				_ = sess.Exit(s.pythonExit)
			}
		},
		PublicKeyHandler: func(_ glssh.Context, key glssh.PublicKey) bool {
			s.authAttempts.Add(1)
			return s.authorized != nil && bytes.Equal(key.Marshal(), s.authorized.Marshal())
		},
		PasswordHandler: func(_ glssh.Context, password string) bool {
			s.authAttempts.Add(1)
			return s.password != "" && password == s.password
		},
	}
	srv.AddHostKey(newKey(t).signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	s.addr = ln.Addr().String()
	return s
}

// bastion is the jump-host fixture: an x/crypto SSH server whose only
// service is the direct-tcpip channel the next hop (or the target) is
// dialed through — exactly the channel type the probe's chain opens. It
// counts every channel open and only authenticates its own client key.
type bastion struct {
	addr      string
	directTCP atomic.Int32
	hostKey   keyFixture
}

func startBastion(t *testing.T, clientKey keyFixture) *bastion {
	return startBastionWithKey(t, clientKey, newKey(t))
}

func (b *bastion) handle(conn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sconn.Close() }()
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "direct-tcpip" {
			_ = ch.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		b.directTCP.Add(1)
		var payload struct {
			TargetAddr string
			TargetPort uint32
			OriginAddr string
			OriginPort uint32
		}
		if err := ssh.Unmarshal(ch.ExtraData(), &payload); err != nil {
			_ = ch.Reject(ssh.ConnectionFailed, "bad payload")
			continue
		}
		target, err := net.Dial("tcp", net.JoinHostPort(payload.TargetAddr, strconv.Itoa(int(payload.TargetPort))))
		if err != nil {
			_ = ch.Reject(ssh.ConnectionFailed, "target unreachable")
			continue
		}
		serverCh, reqs, err := ch.Accept()
		if err != nil {
			_ = target.Close()
			continue
		}
		go ssh.DiscardRequests(reqs)
		go func() {
			_, _ = io.Copy(serverCh, target)
			_ = target.Close()
		}()
		go func() {
			_, _ = io.Copy(target, serverCh)
			_ = target.Close()
		}()
	}
}

// probeTarget is what the tests hand the probe: address and port of the
// target server.
type probeTargetAddr struct {
	Address string
	Port    int
}

func (s *probeServer) asTarget() probeTargetAddr {
	return asAddr(s.addr)
}

func (b *bastion) asHop() BastionHop {
	host, portStr, err := net.SplitHostPort(b.addr)
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		panic(err)
	}
	return BastionHop{Address: host, Port: port}
}

func asAddr(addr string) probeTargetAddr {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		panic(err)
	}
	return probeTargetAddr{Address: host, Port: port}
}

func newProbe() *Probe {
	return &Probe{DialTimeout: 5 * time.Second, Now: time.Now}
}

func runProbe(t *testing.T, tgt probeTargetAddr, cred credentials.Secret, bastions ...BastionHop) Result {
	t.Helper()
	res, err := newProbe().Run(context.Background(), ProbeTarget{
		Address: tgt.Address, Port: tgt.Port, Username: "ansible",
		Credential: cred, Bastions: bastions,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func wantCheck(t *testing.T, res Result, name string, wantOK bool) string {
	t.Helper()
	for _, c := range res.Checks {
		if c.Name == name {
			if c.OK != wantOK {
				t.Fatalf("check %s ok = %v (detail %q), want %v; result %+v", name, c.OK, c.Detail, wantOK, res)
			}
			return c.Detail
		}
	}
	t.Fatalf("check %s missing from %+v", name, res)
	return ""
}

func TestProbeReachableAuthOk(t *testing.T) {
	key := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key))
	tgt := srv.asTarget()

	res := runProbe(t, tgt, key.secret())
	if !res.OK() {
		t.Fatalf("result not ok: %+v", res)
	}
	if !res.Reachable || !res.Auth || !res.Sudo || !res.Python {
		t.Fatalf("flags = %+v", res)
	}
	if res.HostKeyState != string(KeyStateUnpinned) {
		t.Fatalf("host key state = %q, want unpinned", res.HostKeyState)
	}
	if !strings.HasPrefix(res.HostKey, "SHA256:") {
		t.Fatalf("host key fingerprint = %q", res.HostKey)
	}
	if detail := wantCheck(t, res, "sudo", true); detail != "ok" {
		t.Fatalf("sudo detail = %q", detail)
	}
	wantCheck(t, res, "python", true)
	if res.Err != "" {
		t.Fatalf("err = %q", res.Err)
	}
	if strings.Contains(resString(res), key.pem) {
		t.Fatal("result carries the private key")
	}
}

func TestProbeHostKeyStableAcrossRuns(t *testing.T) {
	key := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key))
	tgt := srv.asTarget()

	first := runProbe(t, tgt, key.secret())
	second := runProbe(t, tgt, key.secret())
	if first.HostKey == "" || first.HostKey != second.HostKey {
		t.Fatalf("host key unstable: %q vs %q", first.HostKey, second.HostKey)
	}
	if second.HostKeyState != string(KeyStateUnpinned) {
		t.Fatalf("second run state = %q", second.HostKeyState)
	}
}

func TestProbeWrongCredential(t *testing.T) {
	key := newKey(t)
	wrong := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key))
	tgt := srv.asTarget()

	res := runProbe(t, tgt, wrong.secret())
	if !res.Reachable || res.Auth {
		t.Fatalf("flags = %+v", res)
	}
	if res.HostKey == "" {
		t.Fatal("the host key must have been captured before auth")
	}
	if res.OK() {
		t.Fatalf("result ok: %+v", res)
	}
	if !strings.Contains(res.Err, "unable to authenticate") {
		t.Fatalf("err = %q", res.Err)
	}
	wantCheck(t, res, "auth", false)
	for _, c := range res.Checks {
		if c.Name == "sudo" || c.Name == "python" {
			t.Fatalf("check %s ran despite failed auth: %+v", c.Name, res.Checks)
		}
	}
}

func TestProbeSudoMissing(t *testing.T) {
	key := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key), withSudoExit(1))
	tgt := srv.asTarget()

	res := runProbe(t, tgt, key.secret())
	if res.Sudo || !res.Python {
		t.Fatalf("flags = %+v", res)
	}
	if res.OK() {
		t.Fatalf("ok despite missing sudo: %+v", res)
	}
	if detail := wantCheck(t, res, "sudo", false); !strings.Contains(detail, "exit status 1") {
		t.Fatalf("sudo detail = %q", detail)
	}
	if res.Err == "" || !strings.Contains(res.Err, "exit status 1") {
		t.Fatalf("err = %q", res.Err)
	}
	// Python is still observed: the informational checks run regardless.
	wantCheck(t, res, "python", true)
}

func TestProbePythonMissing(t *testing.T) {
	key := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key), withPythonExit(127))
	tgt := srv.asTarget()

	res := runProbe(t, tgt, key.secret())
	if res.Python || !res.Sudo {
		t.Fatalf("flags = %+v", res)
	}
	if res.OK() {
		t.Fatalf("ok despite missing python: %+v", res)
	}
	if detail := wantCheck(t, res, "python", false); !strings.Contains(detail, "exit status 127") {
		t.Fatalf("python detail = %q", detail)
	}
}

func TestProbeUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := asAddr(ln.Addr().String())
	_ = ln.Close()

	res := runProbe(t, closed, credentials.Secret{})
	if res.Reachable {
		t.Fatalf("reachable %+v", res)
	}
	if res.OK() {
		t.Fatalf("ok %+v", res)
	}
	if res.HostKey != "" || res.HostKeyState != "" {
		t.Fatalf("no key expected: %+v", res)
	}
	if res.Err == "" {
		t.Fatal("err must name the dial failure")
	}
	wantCheck(t, res, "reachability", false)
	if len(res.Checks) != 1 {
		t.Fatalf("only the reachability check should exist: %+v", res.Checks)
	}
}

func TestProbePinnedMismatchAbortsBeforeAuth(t *testing.T) {
	key := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key))
	tgt := srv.asTarget()

	realKey := runProbe(t, tgt, key.secret()).HostKey
	// The counter is shared: snapshot it after the successful first probe,
	// so the assertion reads only the mismatch run's activity.
	before := srv.authAttempts.Load()

	res, err := newProbe().Run(context.Background(), ProbeTarget{
		Address: tgt.Address, Port: tgt.Port, Username: "ansible",
		Credential: key.secret(), PinnedKey: realKey[:6] + "WRONG",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.HostKeyState != string(KeyStateChanged) {
		t.Fatalf("state = %q, want changed", res.HostKeyState)
	}
	if res.Auth || res.Sudo || res.Python {
		t.Fatalf("flags = %+v", res)
	}
	if res.OK() {
		t.Fatalf("ok %+v", res)
	}
	if !strings.Contains(res.Err, "host key changed") {
		t.Fatalf("err = %q", res.Err)
	}
	if res.HostKey != realKey {
		t.Fatalf("the observed key must still be reported: %q vs %q", res.HostKey, realKey)
	}
	// Fail closed: the mismatch aborts the handshake before the server
	// ever sees an authentication attempt.
	if n := srv.authAttempts.Load(); n != before {
		t.Fatalf("auth attempts = %d, want %d", n, before)
	}
	wantCheck(t, res, "host_key", false)
}

func TestProbePinnedMatchProceeds(t *testing.T) {
	key := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key))
	tgt := srv.asTarget()

	pinned := runProbe(t, tgt, key.secret()).HostKey

	res, err := newProbe().Run(context.Background(), ProbeTarget{
		Address: tgt.Address, Port: tgt.Port, Username: "ansible",
		Credential: key.secret(), PinnedKey: pinned,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.HostKeyState != string(KeyStatePinned) {
		t.Fatalf("state = %q, want pinned", res.HostKeyState)
	}
	if !res.OK() {
		t.Fatalf("pinned match must proceed to auth and exec: %+v", res)
	}
	if n := srv.authAttempts.Load(); n == 0 {
		t.Fatal("the pinned match never authenticated")
	}
}

func TestProbePasswordCredential(t *testing.T) {
	srv := startProbeServer(t, withPassword("s3cret"))
	tgt := srv.asTarget()

	res := runProbe(t, tgt, credentials.Secret{Password: "s3cret"})
	if !res.OK() {
		t.Fatalf("password auth failed: %+v", res)
	}
	if res.HostKeyState != string(KeyStateUnpinned) {
		t.Fatalf("state = %q", res.HostKeyState)
	}
}

func TestProbeEmptyCredentialFailsAuth(t *testing.T) {
	srv := startProbeServer(t)
	tgt := srv.asTarget()

	res := runProbe(t, tgt, credentials.Secret{})
	if !res.Reachable || res.Auth {
		t.Fatalf("flags = %+v", res)
	}
	if res.OK() {
		t.Fatalf("ok %+v", res)
	}
}

func TestProbeThroughBastion(t *testing.T) {
	key := newKey(t)
	hopKey := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key))
	hop := startBastion(t, hopKey)
	tgt := srv.asTarget()
	h := hop.asHop()
	h.Username = "jumper"
	h.Credential = hopKey.secret()

	res := runProbe(t, tgt, key.secret(), h)
	if !res.OK() {
		t.Fatalf("chained probe failed: %+v", res)
	}
	if hop.directTCP.Load() != 1 {
		t.Fatalf("direct-tcpip opens = %d, want 1 — the chain was not used", hop.directTCP.Load())
	}
	if res.HostKey != runProbe(t, tgt, key.secret()).HostKey {
		t.Fatal("chained and direct observations disagree")
	}
}

func TestProbeHopCredentialFailureIsUnreachable(t *testing.T) {
	key := newKey(t)
	wrongHopKey := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key))
	hop := startBastion(t, key) // accepts a DIFFERENT key than the hop presents
	tgt := srv.asTarget()
	h := hop.asHop()
	h.Username = "jumper"
	h.Credential = wrongHopKey.secret()

	res := runProbe(t, tgt, key.secret(), h)
	if res.Reachable || res.OK() {
		t.Fatalf("a hop that rejects the credential must end unreachable: %+v", res)
	}
	if !strings.Contains(res.Err, "hop 1") {
		t.Fatalf("err must name the hop: %q", res.Err)
	}
}

// --- Per-hop host keys (backlog 2.40) ---------------------------------

// startBastionWithKey starts a jump host whose host key is FIXED, so a test
// can pin exactly what the probe observed (and then a different one).
func startBastionWithKey(t *testing.T, clientKey, hostKey keyFixture) *bastion {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), clientKey.signer.PublicKey().Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("unknown key")
		},
	}
	cfg.AddHostKey(hostKey.signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &bastion{hostKey: hostKey}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go b.handle(conn, cfg)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	b.addr = ln.Addr().String()
	return b
}

func TestProbeCapturesHopKeys(t *testing.T) {
	targetKey, hop1Key, hop2Key := newKey(t), newKey(t), newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(targetKey))
	hop1 := startBastion(t, hop1Key)
	hop2 := startBastion(t, hop2Key)
	tgt := srv.asTarget()
	h1, h2 := hop1.asHop(), hop2.asHop()
	h1.Username, h1.Credential = "j1", hop1Key.secret()
	h2.Username, h2.Credential = "j2", hop2Key.secret()

	res := runProbe(t, tgt, targetKey.secret(), h1, h2)
	if !res.OK() {
		t.Fatalf("two-hop probe failed: %+v", res)
	}
	if len(res.HopKeys) != 2 {
		t.Fatalf("hop keys = %+v, want one entry per hop", res.HopKeys)
	}
	h1.ID, h2.ID = "hop-1", "hop-2"
	res = runProbe(t, tgt, targetKey.secret(), h1, h2)
	if len(res.HopKeys) != 2 {
		t.Fatalf("hop keys = %+v, want one entry per hop", res.HopKeys)
	}
	want := []struct{ id, fp string }{
		{h1.ID, ssh.FingerprintSHA256(hop1.hostKey.signer.PublicKey())},
		{h2.ID, ssh.FingerprintSHA256(hop2.hostKey.signer.PublicKey())},
	}
	for i, w := range want {
		got := res.HopKeys[i]
		if got.BastionID != w.id {
			t.Fatalf("hop %d bastion id = %q, want %q", i, got.BastionID, w.id)
		}
		if got.Fingerprint != w.fp {
			t.Fatalf("hop %d fingerprint = %q, want %q", i, got.Fingerprint, w.fp)
		}
		if got.State != string(KeyStateUnpinned) {
			t.Fatalf("hop %d state = %q, want unpinned", i, got.State)
		}
		if !strings.HasPrefix(got.PublicKey, "ssh-ed25519 ") {
			t.Fatalf("hop %d public key = %q, want an authorized-key line", i, got.PublicKey)
		}
	}
	wantCheck(t, res, "bastion_key", true)
}

func TestProbePinnedHopMatchProceeds(t *testing.T) {
	targetKey, hopKey := newKey(t), newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(targetKey))
	bastionHostKey := newKey(t)
	hop := startBastionWithKey(t, hopKey, bastionHostKey)
	tgt := srv.asTarget()
	h := hop.asHop()
	h.Username, h.Credential = "jumper", hopKey.secret()
	h.PinnedKey = ssh.FingerprintSHA256(bastionHostKey.signer.PublicKey())

	res := runProbe(t, tgt, targetKey.secret(), h)
	if !res.OK() {
		t.Fatalf("a probe through a bastion matching its pin must proceed: %+v", res)
	}
	if len(res.HopKeys) != 1 || res.HopKeys[0].State != string(KeyStatePinned) {
		t.Fatalf("hop keys = %+v, want one entry with state pinned", res.HopKeys)
	}
}

func TestProbePinnedHopChangedAbortsBeforeAuth(t *testing.T) {
	targetKey, hopKey := newKey(t), newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(targetKey))
	bastionHostKey := newKey(t)
	hop := startBastionWithKey(t, hopKey, bastionHostKey)
	tgt := srv.asTarget()
	h := hop.asHop()
	h.Username, h.Credential = "jumper", hopKey.secret()
	// A pin that does not match: the probe must abort at the hop — nothing
	// is authenticated through it, the target is never reached.
	h.PinnedKey = wrongFingerprint(ssh.FingerprintSHA256(bastionHostKey.signer.PublicKey()))

	res := runProbe(t, tgt, targetKey.secret(), h)
	if res.OK() || res.Reachable {
		t.Fatalf("a changed bastion key must abort the probe: %+v", res)
	}
	if len(res.HopKeys) != 1 || res.HopKeys[0].State != string(KeyStateChanged) {
		t.Fatalf("hop keys = %+v, want the changed bastion reported as changed", res.HopKeys)
	}
	if res.HopKeys[0].Fingerprint != ssh.FingerprintSHA256(bastionHostKey.signer.PublicKey()) {
		t.Fatalf("the changed bastion's observed key must still be reported: %+v", res.HopKeys[0])
	}
	if !strings.Contains(res.Err, "hop 1") || !strings.Contains(res.Err, "host key changed") {
		t.Fatalf("err must name the hop and the change: %q", res.Err)
	}
	wantCheck(t, res, "bastion_key", false)
}

// wrongFingerprint derives a fingerprint that is guaranteed to differ from
// the given one (same shape, different digest).
func wrongFingerprint(fp string) string {
	if strings.HasPrefix(fp[7:], "A") {
		return "SHA256:B" + fp[8:]
	}
	return "SHA256:A" + fp[8:]
}

func TestProbeCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newProbe().Run(ctx, ProbeTarget{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func resString(res Result) string {
	var b strings.Builder
	b.WriteString(res.Err)
	for _, c := range res.Checks {
		b.WriteString(c.Detail)
	}
	return b.String()
}

// TestNilCredentialMethods pins the empty-credential behavior: no auth
// methods ship, the handshake still runs.
func TestNilCredentialMethods(t *testing.T) {
	methods, err := authMethods(credentials.Secret{})
	if err != nil || len(methods) != 0 {
		t.Fatalf("methods = %v, %v", methods, err)
	}
}

// TestProbeReportsTheFullHostPublicKey (final review C2): every outcome
// that observed the host key — a passing test, a failed login, a changed
// key — reports the key itself in authorized-key form next to its
// fingerprint, so pinning stores what ansible's known_hosts needs to
// enforce the pin. A probe that never reached key exchange reports none.
func TestProbeReportsTheFullHostPublicKey(t *testing.T) {
	key := newKey(t)
	srv := startProbeServer(t, withAuthorizedKey(key))
	tgt := srv.asTarget()
	fingerprintOf := func(t *testing.T, line string) string {
		t.Helper()
		pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil || len(rest) != 0 {
			t.Fatalf("host public key %q is not one authorized-key line: %v", line, err)
		}
		return ssh.FingerprintSHA256(pub)
	}

	ok := runProbe(t, tgt, key.secret())
	cases := []struct {
		name   string
		target ProbeTarget
	}{
		{"passing test", ProbeTarget{Address: tgt.Address, Port: tgt.Port, Username: "ansible", Credential: key.secret()}},
		{"failed login", ProbeTarget{Address: tgt.Address, Port: tgt.Port, Username: "ansible", Credential: newKey(t).secret()}},
		{"changed key", ProbeTarget{Address: tgt.Address, Port: tgt.Port, Username: "ansible", Credential: key.secret(), PinnedKey: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := newProbe().Run(context.Background(), c.target)
			if err != nil {
				t.Fatal(err)
			}
			if res.HostPublicKey == "" {
				t.Fatalf("no host public key reported: %+v", res)
			}
			if got := fingerprintOf(t, res.HostPublicKey); got != res.HostKey || got != ok.HostKey {
				t.Fatalf("public key fingerprint %s, reported %s, want %s", got, res.HostKey, ok.HostKey)
			}
		})
	}

	unreachable, err := newProbe().Run(context.Background(), ProbeTarget{Address: "127.0.0.1", Port: 1, Username: "ansible", Credential: key.secret()})
	if err != nil {
		t.Fatal(err)
	}
	if unreachable.HostPublicKey != "" {
		t.Fatalf("an unreachable host reported a public key: %q", unreachable.HostPublicKey)
	}
}
