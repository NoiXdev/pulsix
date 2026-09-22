package sshprobe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/credentials"
)

// Defaults and the internal sentinel.
const (
	// DefaultDialTimeout bounds every phase the probe drives: one TCP dial,
	// one SSH handshake (key exchange + authentication), and — after
	// connect — the two exec commands together. The SSH protocol offers no
	// context support, so net.Conn deadlines are the mechanism, anchored
	// on Now.
	DefaultDialTimeout = 10 * time.Second
	defaultPort        = 22

	// The two well-known commands the test runs (spec §5), in this order.
	sudoCommand   = "sudo -n true"
	pythonCommand = "python3 -c 'print(1)'"
)

// errHostKeyChanged is the host-key callback's abort signal: the host
// presented a key that differs from the pin. The handshake stops here —
// nothing is authenticated, nothing is executed.
var errHostKeyChanged = errors.New("host key changed")

// sshAuthFailedFragment is the stable fragment of golang.org/x/crypto/ssh's
// authentication-failure error ("ssh: unable to authenticate, attempted
// methods …"), which NewClientConn wraps in "ssh: handshake failed: …" —
// so a contains match against the wrapped text is the only signal, and
// every other failure after a completed TCP dial is a transport failure.
const sshAuthFailedFragment = "ssh: unable to authenticate"

// Probe runs one connection test. DialTimeout bounds each dial and each
// handshake, and the exec phase as a whole (DefaultDialTimeout when zero);
// Now anchors those deadlines, so tests stay deterministic.
type Probe struct {
	DialTimeout time.Duration
	Now         func() time.Time
}

// BastionHop is one jump host of the chain the probe dials through, in dial
// order: the first hop directly, every later one through the previous hop's
// direct-tcpip channel. Each hop authenticates with its own credential.
type BastionHop struct {
	// ID is the bastion's uuid — the key the manager's pin state is keyed
	// by and what the result's hop entries report. Empty when the caller
	// does not know ids (the chain is then reported positionally).
	ID         string
	Address    string
	Port       int
	Username   string
	Credential credentials.Secret
	// PinnedKey is the hop's pinned fingerprint ("SHA256:…") the observed
	// host key must match; empty means unpinned (trust on first use). A
	// mismatch aborts the chain before the hop can carry anything — fail
	// closed, exactly like the target's pin.
	PinnedKey string
}

// ProbeTarget is the host the probe tests.
type ProbeTarget struct {
	Address    string
	Port       int
	Username   string
	Credential credentials.Secret
	// Bastions is the full jump chain in dial order, each hop's own
	// credential included. Empty means a direct dial.
	Bastions []BastionHop
	// PinnedKey is the pinned fingerprint ("SHA256:…") the observed host
	// key must match; empty means unpinned (trust on first use). A
	// mismatch aborts the probe before any authentication — fail closed.
	PinnedKey string
}

// Check is one row of the result's checks list: a named probe stage and
// what it observed.
type Check struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	OK     bool   `json:"ok"`
}

// Result is the whole test's outcome. It carries no credential material —
// it is exactly what lands as the connection_test event's payload and, on
// finish, in the host_connection_tests row.
type Result struct {
	// Reachable: the TCP connection was established and the SSH handshake
	// got past key exchange (the host key was seen).
	Reachable bool `json:"reachable"`
	// HostKey is the OpenSSH fingerprint of the observed host key
	// ("SHA256:…"), empty when the handshake never got that far.
	HostKey string `json:"host_key"`
	// HostPublicKey is the observed host key itself in authorized-key form
	// ("ssh-ed25519 AAAA…", no comment), empty when HostKey is. Pinning
	// stores it: ansible's known_hosts needs the key, not its fingerprint.
	HostPublicKey string `json:"host_public_key,omitempty"`
	// HostKeyState is the TOFU verdict of HostKey against the pin:
	// unpinned, pinned, changed — or empty when nothing was observed
	// (never reached).
	HostKeyState string `json:"host_key_state"`
	// Auth: the SSH authentication succeeded (a changed host key aborts
	// before authentication, so it implies Auth=false).
	Auth bool `json:"auth"`
	// Sudo and Python report the two exec checks; they run only after a
	// successful authentication.
	Sudo   bool `json:"sudo"`
	Python bool `json:"python"`
	// Checks is the per-stage detail list in stage order; a stage that was
	// never reached is omitted, not failed.
	Checks []Check `json:"checks"`
	// HopKeys reports each jump host the chain dialed: its id, the host key
	// the probe observed there (fingerprint plus the key in authorized-key
	// form) and the TOFU verdict of the observation against the hop's pin.
	// Empty when the dial never got to a hop. The manager records them with
	// the test so the operator can pin a bastion's key and the runner can
	// refuse routes through a bastion whose pinned key no longer matches
	// (backlog 2.40).
	HopKeys []HopKey `json:"hop_keys,omitempty"`
	// Err names the first failing stage in one operator-readable line,
	// built from exit statuses and SSH error texts only — it never
	// echoes credential material.
	Err string `json:"err,omitempty"`
}

// HopKey is one jump host's observed host key: what the probe saw at one
// hop of the chain, in the manager's recordable form. It carries no
// credential material — the key is the BASTION's public key.
type HopKey struct {
	BastionID   string `json:"bastion_id"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key,omitempty"`
	State       string `json:"state"`
}

// OK reports whether the test passed (spec §5: the test covers
// reachability, host key, login, passwordless sudo and Python presence; a
// failed sudo or Python still yields ok=false, with the checks list
// carrying the details). A host key marked changed never authenticates, so
// the state is implied by the flags.
func (r Result) OK() bool {
	return r.Reachable && r.Auth && r.Sudo && r.Python
}

// Run executes the probe. Dial failures, authentication failures and host
// key changes are RESULTS (the test ran and observed an outcome); the error
// return is reserved for the caller's own broken input — an already
// cancelled context, or a credential whose private key cannot be parsed,
// which is claim corruption, not a host property.
func (p *Probe) Run(ctx context.Context, target ProbeTarget) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	auth, err := authMethods(target.Credential)
	if err != nil {
		return Result{}, err
	}

	timeout := p.dialTimeout()
	var seen observedKey
	cfg := &ssh.ClientConfig{
		User:            target.Username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback(target.PinnedKey, &seen),
		Timeout:         timeout,
	}
	seenKey := func() string { return seen.fingerprint }

	// Every hop whose handshake ran reports what it saw, dial order.
	var hopSeen []observedHop
	client, conn, err := p.dial(ctx, target.Bastions, joinHostPort(target.Address, target.Port), cfg, &hopSeen)
	if err != nil {
		res := p.dialOutcome(target.PinnedKey, seen.fingerprint, hopSeen, err)
		if res.HostKey != "" {
			res.HostPublicKey = seen.publicKey
		}
		return res, nil
	}
	defer func() { _ = client.Close() }()

	// Cancellation: the SSH session offers no context support; closing the
	// client from the watcher unblocks whatever exec is in flight. The
	// deferred Close covers the normal path.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()

	res := Result{
		Reachable:     true,
		HostKey:       seenKey(),
		HostPublicKey: seen.publicKey,
		HostKeyState:  string(Compare(target.PinnedKey, seenKey())),
		Auth:          true,
		HopKeys:       hopKeys(hopSeen),
	}
	res.Checks = append(res.Checks,
		Check{Name: "reachability", Detail: "connected " + route(target.Bastions), OK: true},
		Check{Name: "host_key", Detail: keyDetail(seenKey(), res.HostKeyState), OK: true},
	)
	for _, hk := range res.HopKeys {
		// A hop whose handshake completed never reports changed here — a
		// changed hop aborts the chain — so the check is informative, not
		// gating: pinned and unpinned (trust on first use) both pass.
		res.Checks = append(res.Checks, Check{Name: "bastion_key", Detail: keyDetail(hk.Fingerprint, hk.State), OK: true})
	}
	res.Checks = append(res.Checks,
		Check{Name: "auth", Detail: "ssh authentication ok", OK: true},
	)

	// The exec phase shares the dial budget, measured from now: the two
	// commands are fast, and a hung command must not wedge the worker.
	if err := conn.SetDeadline(p.now().Add(timeout)); err != nil {
		res.Err = "connection deadline: " + err.Error()
		return res, nil
	}

	sudo := p.exec(client, "sudo", sudoCommand)
	res.Sudo = sudo.OK
	if !sudo.OK {
		res.Err = sudo.Detail
	}
	res.Checks = append(res.Checks, sudo)

	py := p.exec(client, "python", pythonCommand)
	res.Python = py.OK
	if !py.OK && res.Err == "" {
		res.Err = py.Detail
	}
	res.Checks = append(res.Checks, py)
	return res, nil
}

// dialOutcome maps a failed dial to a result. Five shapes matter: a hop's
// own failure (its credential rejected, its route dead — the target was
// never reached), a hop's PINNED host key changing (the chain aborts at the
// hop — fail closed, the observed key is still reported), the host-key
// callback's abort at the target, an authentication failure at the target
// (reachable, key seen, not authenticated), and everything else
// (unreachable — a failed TCP dial or a dead handshake). Only the final
// dial step's errors are unwrapped, so intermediate hop failures can never
// be misattributed to the target.
func (p *Probe) dialOutcome(pinned, seenKey string, hopSeen []observedHop, err error) Result {
	res := Result{HopKeys: hopKeys(hopSeen)}
	switch {
	case changedHop(hopSeen, err) >= 0:
		i := changedHop(hopSeen, err)
		hop := hopSeen[i]
		res.Err = fmt.Sprintf("hop %d: host key changed: bastion presents %s, pin is %s", hop.index+1, hop.seen.fingerprint, hop.pinned)
		res.Checks = append(res.Checks,
			Check{Name: "reachability", Detail: res.Err, OK: false},
			Check{Name: "bastion_key", Detail: keyDetail(hop.seen.fingerprint, string(KeyStateChanged)), OK: false},
		)
	case isHopFailure(err):
		// A hop's own failure: the target was never reached. Every hop the
		// chain dialed before it still reports its observed key.
		res.Err = err.Error()
		res.Checks = append(res.Checks, Check{Name: "reachability", Detail: err.Error(), OK: false})
		for _, hk := range res.HopKeys {
			res.Checks = append(res.Checks, Check{Name: "bastion_key", Detail: keyDetail(hk.Fingerprint, hk.State), OK: true})
		}
	case errors.Is(err, errHostKeyChanged):
		res.Reachable = true
		res.HostKey = seenKey
		res.HostKeyState = string(Compare(pinned, seenKey))
		res.Err = "host key changed: host presents " + seenKey + ", pin is " + pinned
		res.Checks = append(res.Checks,
			Check{Name: "reachability", Detail: "connected", OK: true},
			Check{Name: "host_key", Detail: keyDetail(seenKey, res.HostKeyState), OK: false},
		)
		for _, hk := range res.HopKeys {
			res.Checks = append(res.Checks, Check{Name: "bastion_key", Detail: keyDetail(hk.Fingerprint, hk.State), OK: true})
		}
	case strings.Contains(err.Error(), sshAuthFailedFragment):
		res.Reachable = true
		res.HostKey = seenKey
		res.HostKeyState = string(Compare(pinned, seenKey))
		res.Err = "ssh authentication failed: " + err.Error()
		res.Checks = append(res.Checks,
			Check{Name: "reachability", Detail: "connected", OK: true},
			Check{Name: "host_key", Detail: keyDetail(seenKey, res.HostKeyState), OK: true},
		)
		for _, hk := range res.HopKeys {
			res.Checks = append(res.Checks, Check{Name: "bastion_key", Detail: keyDetail(hk.Fingerprint, hk.State), OK: true})
		}
		res.Checks = append(res.Checks, Check{Name: "auth", Detail: err.Error(), OK: false})
	default:
		res.Err = err.Error()
		res.Checks = append(res.Checks, Check{Name: "reachability", Detail: err.Error(), OK: false})
		for _, hk := range res.HopKeys {
			res.Checks = append(res.Checks, Check{Name: "bastion_key", Detail: keyDetail(hk.Fingerprint, hk.State), OK: true})
		}
	}
	return res
}

// exec runs one command on the established connection and reports the exit
// status. A non-zero status (sudo refusing without a TTY, a missing
// python3, exit 127) is an observation, not an error.
func (p *Probe) exec(client *ssh.Client, name, command string) Check {
	session, err := client.NewSession()
	if err != nil {
		return Check{Name: name, Detail: "open session: " + err.Error(), OK: false}
	}
	defer func() { _ = session.Close() }()
	var stderr strings.Builder
	session.Stderr = &stderr
	err = session.Run(command)
	switch {
	case err == nil:
		return Check{Name: name, Detail: "ok", OK: true}
	case isExitStatus(err):
		detail := "exit status " + strconv.Itoa(exitStatus(err))
		if line := firstLine(stderr.String()); line != "" {
			detail += ": " + line
		}
		return Check{Name: name, Detail: detail, OK: false}
	default:
		return Check{Name: name, Detail: err.Error(), OK: false}
	}
}

// hopError wraps a failure inside the jump chain, so a hop's own failure
// (its credential rejected, its route dead) can never be misattributed to
// the target — dialOutcome reports the target as unreachable for it.
type hopError struct {
	index int // 1-based: the first hop is hop 1
	err   error
}

func (e *hopError) Error() string {
	return fmt.Sprintf("hop %d: %v", e.index, e.err)
}

func (e *hopError) Unwrap() error { return e.err }

// dial opens the hop chain and the target: the first step over TCP, every
// later step through the previous hop's direct-tcpip channel. All
// deadlines ride the root connection — the one real socket everything
// channels through — because x/crypto's channel connections do not support
// deadlines; setting one before each step's transport opens and clearing
// it after the step's handshake bounds every phase without strangling the
// idle chain. On success it returns the final client and the root
// connection the exec phase bounds; on failure everything opened so far
// is closed and intermediate hop failures are wrapped in hopError, so the
// target's own errors stay classifiable. hopSeen collects every hop whose
// handshake ran — dial order, each one's observed key and pin — so the
// result reports them even when a later step (or a changed hop key) ends
// the dial.
func (p *Probe) dial(ctx context.Context, hops []BastionHop, target string, targetCfg *ssh.ClientConfig, hopSeen *[]observedHop) (*ssh.Client, net.Conn, error) {
	timeout := p.dialTimeout()
	type step struct {
		addr string
		cfg  *ssh.ClientConfig
		seen *observedKey
	}
	*hopSeen = make([]observedHop, 0, len(hops))
	steps := make([]step, 0, len(hops)+1)
	for i, h := range hops {
		hopAuth, err := authMethods(h.Credential)
		if err != nil {
			return nil, nil, &hopError{index: i + 1, err: fmt.Errorf("credential: %w", err)}
		}
		seen := &observedKey{}
		*hopSeen = append(*hopSeen, observedHop{index: i, id: h.ID, pinned: h.PinnedKey, seen: seen})
		steps = append(steps, step{
			addr: joinHostPort(h.Address, h.Port),
			cfg: &ssh.ClientConfig{
				User: h.Username, Auth: hopAuth,
				// The hop's own pin: a pinned hop is verified exactly like
				// the target (a mismatch aborts the chain); unpinned hops
				// are trusted on first use — the callback still captures
				// what they presented, the result reports it.
				HostKeyCallback: hostKeyCallback(h.PinnedKey, seen),
				Timeout:         timeout,
			},
			seen: seen,
		})
	}
	steps = append(steps, step{target, targetCfg, nil})

	var client *ssh.Client
	var conn net.Conn
	defer func() {
		// The error path: close everything opened so far. The final client
		// rides the first connection, so closing the root releases the
		// whole chain.
		if client != nil {
			_ = client.Close()
			return
		}
		if conn != nil {
			_ = conn.Close()
		}
	}()
	last := len(steps) - 1
	for i, s := range steps {
		if i == 0 {
			var d net.Dialer
			d.Timeout = timeout
			c, err := d.DialContext(ctx, "tcp", s.addr)
			if err != nil {
				return nil, nil, fmt.Errorf("dial %s: %w", s.addr, err)
			}
			conn = c
		}
		// Bound this step's transport and handshake on the root socket —
		// every later hop and the target ride it — then clear the deadline,
		// which would otherwise strangle the idle chain.
		if err := conn.SetDeadline(p.now().Add(timeout)); err != nil {
			return nil, nil, err
		}
		var (
			handshake ssh.Conn
			chans     <-chan ssh.NewChannel
			reqs      <-chan *ssh.Request
			err       error
		)
		if i == 0 {
			handshake, chans, reqs, err = ssh.NewClientConn(conn, s.addr, s.cfg)
		} else {
			var c net.Conn
			c, err = client.Dial("tcp", s.addr)
			if err == nil {
				handshake, chans, reqs, err = ssh.NewClientConn(c, s.addr, s.cfg)
			}
		}
		if err != nil {
			if i == last {
				return nil, nil, err // the target's own failure: stays classifiable
			}
			return nil, nil, &hopError{index: i + 1, err: fmt.Errorf("%s: %w", s.addr, err)}
		}
		if err := conn.SetDeadline(time.Time{}); err != nil {
			return nil, nil, err
		}
		if i == last {
			out, outConn := ssh.NewClient(handshake, chans, reqs), conn
			client, conn = nil, nil // detach from the cleanup defer
			return out, outConn, nil
		}
		client = ssh.NewClient(handshake, chans, reqs)
	}
	return nil, nil, errors.New("no dial steps") // unreachable: steps always ends with the target
}

// observedKey is one host key one handshake saw: its OpenSSH fingerprint
// and the key itself in authorized-key form.
type observedKey struct {
	fingerprint string
	publicKey   string
}

// observedHop is one jump host's observation: which chain position and
// bastion it belongs to, the pin it was verified against, and the key its
// own handshake saw (filled by that step's callback).
type observedHop struct {
	index  int // 0-based into the chain
	id     string
	pinned string
	seen   *observedKey
}

// hopKeys folds the dial's hop observations into the result's reportable
// entries. A hop the dial never reached (its handshake never ran) carries
// no observation and is omitted; an id the caller did not know is reported
// empty, positionally.
func hopKeys(seen []observedHop) []HopKey {
	out := make([]HopKey, 0, len(seen))
	for _, h := range seen {
		if h.seen.fingerprint == "" {
			continue // the handshake never got to this hop
		}
		out = append(out, HopKey{
			BastionID:   h.id,
			Fingerprint: h.seen.fingerprint,
			PublicKey:   h.seen.publicKey,
			State:       string(Compare(h.pinned, h.seen.fingerprint)),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// changedHop reports the index of the hop whose pinned key mismatch ended
// the dial, -1 when the error is not a hop's changed key. The mismatch
// aborts the chain at the hop (the callback's errHostKeyChanged, wrapped
// in hopError for every non-final step): the chain position identifies
// which hop it was.
func changedHop(seen []observedHop, err error) int {
	var hop *hopError
	if !errors.As(err, &hop) {
		return -1
	}
	for i := range seen {
		if seen[i].index == hop.index-1 && errors.Is(hop.err, errHostKeyChanged) {
			return i
		}
	}
	return -1
}

// hostKeyCallback captures the key and its fingerprint and enforces the
// pin: a pin the observed key does not match aborts the handshake before
// any authentication can happen.
func hostKeyCallback(pinned string, seen *observedKey) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		seen.fingerprint = ssh.FingerprintSHA256(key)
		seen.publicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		if pinned != "" && seen.fingerprint != pinned {
			return errHostKeyChanged
		}
		return nil
	}
}

// authMethods turns a credential into ssh auth methods: an ssh_key
// credential signs with its private key, an ssh_password credential offers
// password authentication. An empty credential ships none — the handshake
// still runs, the server refuses, and the probe reports the outcome.
func authMethods(secret credentials.Secret) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if secret.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(secret.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if secret.Password != "" {
		methods = append(methods, ssh.Password(secret.Password))
	}
	return methods, nil
}

func isExitStatus(err error) bool {
	var exitErr *ssh.ExitError
	return errors.As(err, &exitErr)
}

func exitStatus(err error) int {
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitStatus()
	}
	return -1
}

// firstLine returns the first line of a remote stderr, bounded — remote
// output flows into the stored result, so it is trimmed to one short line.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if line, _, ok := strings.Cut(s, "\n"); ok {
		s = line
	}
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func keyDetail(fingerprint, state string) string {
	return fingerprint + " (" + state + ")"
}

func route(hops []BastionHop) string {
	if len(hops) == 0 {
		return "(direct)"
	}
	names := make([]string, 0, len(hops))
	for _, h := range hops {
		names = append(names, h.Address)
	}
	return "(via " + strings.Join(names, " -> ") + ")"
}

func joinHostPort(address string, port int) string {
	if port <= 0 {
		port = defaultPort
	}
	return net.JoinHostPort(address, strconv.Itoa(port))
}

func (p *Probe) dialTimeout() time.Duration {
	if p.DialTimeout > 0 {
		return p.DialTimeout
	}
	return DefaultDialTimeout
}

func (p *Probe) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}
