// The connection-test executor's tests: payload parsing, the single result
// event and the terminal mapping, driven by the real sshprobe against an
// in-process SSH server (gliderlabs, the same fixture pattern as the probe
// tests).
package worker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	glssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker/sshprobe"
)

var ctx = context.Background()

// probeFixture is the executor test's SSH target: a gliderlabs server with
// one accepted client key.
type probeFixture struct {
	addr string
	pem  string
	sudo int
}

func startExecutorServer(t *testing.T, sudoExit int) *probeFixture {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	authorized := clientKey.PublicKey().Marshal()
	srv := &glssh.Server{
		Handler: func(sess glssh.Session) {
			cmd := sess.Command()
			if len(cmd) > 0 && cmd[0] == "sudo" && sudoExit != 0 {
				_ = sess.Exit(sudoExit)
			}
		},
		PublicKeyHandler: func(_ glssh.Context, key glssh.PublicKey) bool {
			return bytes.Equal(key.Marshal(), authorized)
		},
	}
	srv.AddHostKey(sshFixHostKey(t))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &probeFixture{addr: ln.Addr().String(), pem: string(pem.EncodeToMemory(block)), sudo: sudoExit}
}

func sshFixHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func (f *probeFixture) hostPort() (string, int) {
	host, portStr, err := net.SplitHostPort(f.addr)
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		panic(err)
	}
	return host, port
}

// connectionTestJob builds the claimed job the executor parses: the same
// kind-payload shape the manager's claim path ships.
func connectionTestJob(name, address string, port int, keyPem, credential string, bastions []string) *workerpb.Job {
	secret := credential
	if secret == "" {
		// The claim's credential is the Secret JSON the manager opens at
		// claim time; a key credential wraps its PEM inside it.
		secret = secretJSON(keyPem)
	}
	payload := map[string]any{
		"host":       map[string]any{"id": "u1", "name": name, "address": address, "port": port, "username": "ansible"},
		"credential": secret,
	}
	hops := make([]map[string]any, 0, len(bastions))
	for _, b := range bastions {
		hops = append(hops, map[string]any{"id": b, "address": "127.0.0.1", "port": 22, "username": "jumper", "credential": ""})
	}
	payload["bastions"] = hops
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return &workerpb.Job{Id: "job-1", Kind: "connection_test", KindPayload: string(raw)}
}

// secretJSON encodes a key PEM as the Secret JSON the claim carries.
func secretJSON(keyPem string) string {
	raw, err := json.Marshal(credentials.Secret{PrivateKey: keyPem})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestSSHProbeExecutorOkTestSucceedsWithOneResultEvent(t *testing.T) {
	f := startExecutorServer(t, 0)
	host, port := f.hostPort()
	exec := NewSSHProbeExecutor(&sshprobe.Probe{}, nil)
	job := connectionTestJob("web1", host, port, f.pem, "", nil)

	var events []*workerpb.RunEvent
	status, stats, err := exec.Run(ctx, job, RunEnv{}, func(ev *workerpb.RunEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status != StatusSucceeded {
		t.Fatalf("status = %s", status)
	}
	if stats["ok"] != true {
		t.Fatalf("stats = %v", stats)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	ev := events[0]
	if ev.Host != "web1" || ev.Task != "connection_test" || ev.State != "ok" {
		t.Fatalf("event = %+v", ev)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(ev.Payload), &res); err != nil {
		t.Fatalf("payload = %q: %v", ev.Payload, err)
	}
	if res["host_key_state"] != "unpinned" || res["host_key"] == "" {
		t.Fatalf("result = %v", res)
	}
}

func TestSSHProbeExecutorFailedTestIsTerminalFailedNotError(t *testing.T) {
	failing := startExecutorServer(t, 1)
	fhost, fport := failing.hostPort()
	exec := NewSSHProbeExecutor(&sshprobe.Probe{}, nil)
	job := connectionTestJob("web1", fhost, fport, failing.pem, "", nil)
	var events []*workerpb.RunEvent
	status, stats, err := exec.Run(ctx, job, RunEnv{}, func(ev *workerpb.RunEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status != StatusFailed {
		t.Fatalf("status = %s", status)
	}
	if stats["ok"] != false {
		t.Fatalf("stats = %v", stats)
	}
	if len(events) != 1 || events[0].State != "failed" {
		t.Fatalf("events = %+v", events)
	}
}

func TestSSHProbeExecutorRejectsBrokenInput(t *testing.T) {
	f := startExecutorServer(t, 0)
	host, port := f.hostPort()
	exec := NewSSHProbeExecutor(&sshprobe.Probe{}, nil)

	cases := []struct {
		name string
		job  *workerpb.Job
	}{
		{"unknown kind", &workerpb.Job{Kind: "run", KindPayload: "{}"}},
		{"payload not JSON", &workerpb.Job{Kind: "connection_test", KindPayload: "{oops"}},
		{"payload carries no host", &workerpb.Job{Kind: "connection_test", KindPayload: `{"credential":""}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, _, err := exec.Run(ctx, c.job, RunEnv{}, func(*workerpb.RunEvent) error { return nil })
			if err == nil || status != StatusError {
				t.Fatalf("Run = %s, %v, want error", status, err)
			}
		})
	}

	// A broken credential inside a well-formed payload.
	payload := `{"host":{"name":"web1","address":"` + host + `","port":` + strconv.Itoa(port) + `},"credential":"{oops"}`
	if _, _, err := exec.Run(ctx, &workerpb.Job{Kind: "connection_test", KindPayload: payload}, RunEnv{}, func(*workerpb.RunEvent) error { return nil }); err == nil {
		t.Fatal("a credential that is not Secret JSON must fail the claim")
	}
}

func TestSSHProbeExecutorNeverEmitsCredentialMaterial(t *testing.T) {
	f := startExecutorServer(t, 0)
	host, port := f.hostPort()
	exec := NewSSHProbeExecutor(&sshprobe.Probe{}, nil)
	job := connectionTestJob("web1", host, port, f.pem, "", nil)
	var events []*workerpb.RunEvent
	if _, _, err := exec.Run(ctx, job, RunEnv{}, func(ev *workerpb.RunEvent) error {
		events = append(events, ev)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if strings.Contains(ev.Payload, f.pem) || strings.Contains(ev.Task, f.pem) || strings.Contains(ev.Host, f.pem) {
			t.Fatalf("event carries the private key: %+v", ev)
		}
	}
}

// execBastion is the executor test's jump host: an x/crypto SSH server
// whose only service is the direct-tcpip channel the probe's chain dials
// through (the same fixture pattern as the probe tests).
type execBastion struct {
	addr      string
	directTCP int
	// fingerprint is the bastion's host key (SHA256:…), what the operator
	// would pin.
	fingerprint string
}

// execKey is one ed25519 keypair: the signer (server side) and the PEM
// private key (the credential the probe authenticates with).
type execKey struct {
	signer ssh.Signer
	pem    string
}

func newExecKey(t *testing.T) execKey {
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
	return execKey{signer: signer, pem: string(pem.EncodeToMemory(block))}
}

// wrongFingerprintOf derives a fingerprint guaranteed to differ from fp.
func wrongFingerprintOf(fp string) string {
	if strings.HasPrefix(fp[7:], "A") {
		return "SHA256:B" + fp[8:]
	}
	return "SHA256:A" + fp[8:]
}

func startExecBastion(t *testing.T, clientKey execKey) *execBastion {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), clientKey.signer.PublicKey().Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("unknown key")
		},
	}
	hostKey := newExecKey(t)
	cfg.AddHostKey(hostKey.signer)
	hostFP := ssh.FingerprintSHA256(hostKey.signer.PublicKey())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &execBastion{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
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
					b.directTCP++
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
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	b.addr = ln.Addr().String()
	b.fingerprint = hostFP
	return b
}

func (b *execBastion) hostPort() (string, int) {
	host, portStr, err := net.SplitHostPort(b.addr)
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		panic(err)
	}
	return host, port
}

// TestSSHProbeExecutorVerifiesTheHopPin drives the whole worker path: the
// kind payload's hop and the claim's bastion_host_keys reach the probe, so
// a pinned chain is verified (a matching pin reports the hop pinned) and a
// CHANGED one fails the test closed with the per-hop state.
func TestSSHProbeExecutorVerifiesTheHopPin(t *testing.T) {
	target := startExecutorServer(t, 0)
	hopKey := newExecKey(t) // the hop's login key; the bastion accepts it
	hop := startExecBastion(t, hopKey)
	hh, hp := hop.hostPort()
	th, tp := target.hostPort()
	exec := NewSSHProbeExecutor(&sshprobe.Probe{}, nil)

	buildJob := func(pinned map[string]string) *workerpb.Job {
		payload := map[string]any{
			"host":       map[string]any{"id": "u1", "name": "web1", "address": th, "port": tp, "username": "ansible"},
			"credential": secretJSON(target.pem),
			"bastions": []map[string]any{{
				"id": "b1", "address": hh, "port": hp, "username": "jumper",
				"credential": secretJSON(hopKey.pem),
			}},
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		return &workerpb.Job{Id: "job-1", Kind: "connection_test", KindPayload: string(raw), BastionHostKeys: pinned}
	}

	// The correct pin: the test proceeds and reports the hop pinned.
	run := func(job *workerpb.Job) (status Status, res map[string]any) {
		t.Helper()
		var events []*workerpb.RunEvent
		st, _, err := exec.Run(ctx, job, RunEnv{}, func(ev *workerpb.RunEvent) error {
			events = append(events, ev)
			return nil
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("events = %+v", events)
		}
		if err := json.Unmarshal([]byte(events[0].Payload), &res); err != nil {
			t.Fatalf("payload = %q: %v", events[0].Payload, err)
		}
		return st, res
	}

	correct := hop.fingerprint
	status, res := run(buildJob(map[string]string{"b1": correct}))
	if status != StatusSucceeded {
		t.Fatalf("a chain whose pin matches must succeed: %s (%v)", status, res)
	}
	entries, _ := res["hop_keys"].([]any)
	if len(entries) != 1 {
		t.Fatalf("hop keys = %v, want one entry", res["hop_keys"])
	}
	hop0 := entries[0].(map[string]any)
	if hop0["bastion_id"] != "b1" || hop0["state"] != "pinned" {
		t.Fatalf("hop entry = %v, want b1 pinned", hop0)
	}
	if hop.directTCP != 1 {
		t.Fatalf("direct-tcpip opens = %d, want 1 — the chain was not used", hop.directTCP)
	}

	// A pin that does not match: the test fails closed, the per-hop state
	// reads changed, and nothing is ever carried through the hop — the
	// mismatch aborts the handshake before the channel opens.
	dials := hop.directTCP
	wrong := map[string]string{"b1": wrongFingerprintOf(correct)}
	status, res = run(buildJob(wrong))
	if status != StatusFailed {
		t.Fatalf("a changed hop key must fail the test: %s (%v)", status, res)
	}
	entries, _ = res["hop_keys"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["state"] != "changed" {
		t.Fatalf("hop entries = %v, want the changed hop reported", res["hop_keys"])
	}
	if hop.directTCP != dials {
		t.Fatalf("a changed bastion key must abort before the hop carries anything: opens %d -> %d", dials, hop.directTCP)
	}
}
