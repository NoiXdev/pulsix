// The scenario's in-process fixtures: a gliderlabs/ssh host server (exec
// only — the probe's two well-known checks plus real commands for the
// runner playbooks), a jump-host bastion that answers direct-tcpip channel
// opens (the x/crypto server side, the same shape the probe tests use), a
// git server that serves the fixture repository over SSH, and the fake
// ansible-runner binary. Everything listens on loopback; the host server
// prefers 127.0.0.2, which the Linux CI runner can bind and macOS cannot —
// there the fallback to 127.0.0.1 keeps the gate runnable and the
// "through the bastion" claim rests on the bastion's direct-tcpip counter
// and the probe's own route detail instead of the network topology.
package e2e

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	glssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

// keyPair is one ed25519 keypair in the two forms the scenario needs: the
// signer (server side) and the PEM private key (the credential side).
type keyPair struct {
	signer ssh.Signer
	pem    string
}

// newKeyPair generates one keypair.
func newKeyPair(t *testing.T) keyPair {
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
	return keyPair{signer: signer, pem: string(pem.EncodeToMemory(block))}
}

// loopbackHost returns the host part of the host SSH server's bind
// address: 127.0.0.2 when the platform can bind it (Linux), 127.0.0.1
// otherwise (macOS needs a root-owned lo0 alias for the rest of the /8).
// The bastion and git servers always bind 127.0.0.1.
func loopbackHost(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err == nil {
		_ = ln.Close()
		return "127.0.0.2"
	}
	return "127.0.0.1"
}

// hostSSHServer is the managed host's SSH endpoint.
type hostSSHServer struct {
	addr string
	host string
	port int
}

// startHostSSHServer starts the host's SSH server, accepting exactly the
// given public key. Exec sessions run for real (through /bin/sh), with the
// probe's two well-known commands answered directly: `sudo -n true` exits
// 0 (passwordless sudo) and `python3 -c 'print(1)'` prints 1 — the probe
// treats both as passing without needing them to exist on the test host.
func startHostSSHServer(t *testing.T, authorized ssh.PublicKey, bindHost string) *hostSSHServer {
	t.Helper()
	srv := &glssh.Server{
		Handler: func(sess glssh.Session) {
			cmd := sess.Command()
			if len(cmd) == 0 {
				_ = sess.Exit(1) // no shell sessions in this scenario
				return
			}
			switch cmd[0] {
			case "sudo":
				// `sudo -n true`: passwordless sudo works.
				return
			case "python3":
				// `python3 -c 'print(1)'`: controller-side Python present.
				_, _ = fmt.Fprint(sess, "1\n")
				return
			default:
				runRealCommand(sess)
			}
		},
		PublicKeyHandler: func(_ glssh.Context, key glssh.PublicKey) bool {
			return authorized != nil && bytes.Equal(key.Marshal(), authorized.Marshal())
		},
	}
	srv.AddHostKey(newKeyPair(t).signer)
	ln, err := net.Listen("tcp", net.JoinHostPort(bindHost, "0"))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return &hostSSHServer{addr: ln.Addr().String(), host: host, port: port}
}

// runRealCommand executes the session's command through /bin/sh, wired to
// the channel — the shape a real exec-only SSH host shows the raw module
// and the probe's fallback paths.
func runRealCommand(sess glssh.Session) {
	cmd := exec.Command("/bin/sh", "-c", sess.RawCommand())
	cmd.Stdout = sess
	cmd.Stderr = sess.Stderr()
	cmd.Stdin = sess
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			_ = sess.Exit(exit.ExitCode())
			return
		}
		_ = sess.Exit(1)
	}
}

// bastionServer is the jump host: an x/crypto SSH server whose only
// service is the direct-tcpip channel the next hop (the target) is dialed
// through. It counts every channel open, so the scenario can assert the
// route was really used.
type bastionServer struct {
	addr      string
	port      int
	directTCP int
}

// startBastion starts the bastion, authenticating only clientKey.
func startBastion(t *testing.T, clientKey ssh.PublicKey) *bastionServer {
	t.Helper()
	b := &bastionServer{}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), clientKey.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("unknown key")
		},
	}
	cfg.AddHostKey(newKeyPair(t).signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
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
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	_ = host
	b.addr = ln.Addr().String()
	b.port = port
	return b
}

func (b *bastionServer) handle(conn net.Conn, cfg *ssh.ServerConfig) {
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
}

// gitServer serves the fixture repository over SSH (read-only: the only
// service is git-upload-pack, the fetch half of git's SSH protocol).
type gitServer struct {
	url string // ssh://git@127.0.0.1:<port>/fixture.git
}

// startGitServer materializes the fixture repository as a bare repo and
// serves fetches from it, authenticating the deploy key.
func startGitServer(t *testing.T, deployKey ssh.PublicKey) *gitServer {
	t.Helper()
	bare := materializeFixtureRepo(t)
	srv := &glssh.Server{
		Handler: func(sess glssh.Session) {
			cmd := sess.Command()
			if len(cmd) != 2 || cmd[0] != "git-upload-pack" {
				_ = sess.Exit(1) // read-only fixture
				return
			}
			runGitUploadPack(sess, bare)
		},
		PublicKeyHandler: func(_ glssh.Context, key glssh.PublicKey) bool {
			return deployKey != nil && bytes.Equal(key.Marshal(), deployKey.Marshal())
		},
	}
	srv.AddHostKey(newKeyPair(t).signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	return &gitServer{url: "ssh://git@127.0.0.1:" + portStr + "/fixture.git"}
}

// materializeFixtureRepo copies the fixture working tree into a temp dir,
// commits it and clones it bare — the repository the server serves. No
// requirements.yml: the galaxy install needs network, and Task 11's sync
// tests cover that path at the unit level.
func materializeFixtureRepo(t *testing.T) string {
	tmp := t.TempDir()
	seed := filepath.Join(tmp, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"hello.yml", "fail.yml"} {
		raw, err := os.ReadFile(filepath.Join("fixtures", "git", f))
		if err != nil {
			t.Fatalf("read fixture %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(seed, f), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=pulsix", "GIT_AUTHOR_EMAIL=pulsix@example.org",
			"GIT_COMMITTER_NAME=pulsix", "GIT_COMMITTER_EMAIL=pulsix@example.org",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, stderr.String())
		}
	}
	git(seed, "init", "-q", "-b", "main")
	git(seed, "add", ".")
	git(seed, "commit", "-q", "-m", "fixture playbooks")
	bare := filepath.Join(tmp, "fixture.git")
	git(tmp, "clone", "-q", "--bare", seed, bare)
	return bare
}

// runGitUploadPack execs `git upload-pack <bare>` with the session as its
// stdio — exactly what a real git-over-SSH server does for a fetch.
func runGitUploadPack(sess glssh.Session, bare string) {
	cmd := exec.Command("git", "upload-pack", bare)
	cmd.Stdout = sess
	cmd.Stderr = sess.Stderr()
	cmd.Stdin = sess
	_ = cmd.Run()
}

// writeFakeRunner writes the fake ansible-runner binary: deterministic,
// driven by the run's own materialized extra vars (env/extravars), and
// emitting the pinned JSONL event stream the worker parses. It mirrors the
// Task 9 fixture pattern; the outcome reads exactly the extra vars the
// template set, so the retry path's var flip drives it end to end.
func writeFakeRunner(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ansible-runner")
	script := `#!/bin/sh
# FAKE ansible-runner (test fixture). Emits a deterministic event stream.
workdir="$2"
doc="$("$workdir/inventory")" || exit 1
host="$(printf '%s' "$doc" | sed -n 's/.*"hostvars":{"\([^"]*\)":{.*/\1/p')"
vars="$workdir/env/extravars"
greeting="$(sed -n 's/.*"greeting":"\([^"]*\)".*/\1/p' "$vars")"
should_fail="$(sed -n 's/.*"should_fail":"\([^"]*\)".*/\1/p' "$vars")"
printf '%s\n' '{"event":"playbook_on_start","stdout":"PLAY [all]","event_data":{}}'
printf '%s\n' "{\"event\":\"runner_on_start\",\"stdout\":\"\",\"event_data\":{\"host\":\"$host\",\"task\":\"Pulsix greeting\",\"task_action\":\"debug\"}}"
if [ "$should_fail" = "true" ]; then
  printf '%s\n' "{\"event\":\"runner_on_failed\",\"stdout\":\"failed: [$host] pulsix e2e deliberate failure\",\"event_data\":{\"host\":\"$host\",\"task\":\"Pulsix deliberate failure\"}}"
  printf '%s\n' "{\"event\":\"playbook_on_stats\",\"stdout\":\"\",\"event_data\":{\"ok\":{\"$host\":1},\"changed\":{},\"failures\":{\"$host\":1},\"dark\":{},\"skipped\":{}}}"
  exit 2
fi
printf '%s\n' "{\"event\":\"runner_on_ok\",\"stdout\":\"ok: [$host] => {\\\"msg\\\": \\\"$greeting\\\"}\",\"event_data\":{\"host\":\"$host\",\"task\":\"Pulsix greeting\",\"res\":{\"msg\":\"$greeting\"}}}"
printf '%s\n' "{\"event\":\"runner_on_start\",\"stdout\":\"\",\"event_data\":{\"host\":\"$host\",\"task\":\"Pulsix marker\",\"task_action\":\"raw\"}}"
printf '%s\n' "{\"event\":\"runner_on_ok\",\"stdout\":\"changed: [$host]\",\"event_data\":{\"host\":\"$host\",\"task\":\"Pulsix marker\",\"res\":{\"changed\":true}}}"
printf '%s\n' "{\"event\":\"playbook_on_stats\",\"stdout\":\"\",\"event_data\":{\"ok\":{\"$host\":1},\"changed\":{\"$host\":1},\"failures\":{},\"dark\":{},\"skipped\":{}}}"
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
