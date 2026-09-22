package runner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	glssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/worker"
)

// TestFetchRepoOverSSHKeepsHostKeysInTheRunDir is v0.2c final re-review
// out-of-scope 2: OpenSSH resolves ~/.ssh/known_hosts from the account's
// passwd home and ignores $HOME, so the fetch's accept-new used to write
// the git server's host key into the real ~/.ssh/known_hosts — in tests the
// developer's own file. The host key must land in the run's known_hosts
// under its work dir, and the account's file must stay untouched.
func TestFetchRepoOverSSHKeepsHostKeysInTheRunDir(t *testing.T) {
	gitOnPATH(t)
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("no ssh client: %v", err)
	}
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skipf("no passwd home to compare against: %v", err)
	}
	accountKnownHosts := filepath.Join(u.HomeDir, ".ssh", "known_hosts")
	before := readOrEmpty(t, accountKnownHosts)

	deployPriv, deployPub := sshKey(t)
	url, _, _, stop := serveGitOverSSH(t, gitFixture(t), deployPub)
	defer stop()

	cacheRoot, workDir := t.TempDir(), t.TempDir()
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: url, Branch: "main", Subpath: "playbooks/site.yml"}
	playbook, err := (&Runner{}).fetchRepo(context.Background(), spec, secretJSON(t, credentials.Secret{PrivateKey: deployPriv}), cacheRoot, workDir)
	if err != nil {
		t.Fatalf("fetchRepo over ssh: %v", err)
	}
	if _, err := os.Stat(playbook); err != nil {
		t.Fatal(err)
	}

	runKnownHosts := filepath.Join(workDir, homeDirName, ".ssh", "known_hosts")
	if got := readOrEmpty(t, runKnownHosts); !strings.Contains(string(got), "ssh-ed25519") {
		t.Fatalf("the server's host key must be remembered in the run's known_hosts %s, got %q", runKnownHosts, got)
	}
	if after := readOrEmpty(t, accountKnownHosts); !bytes.Equal(before, after) {
		t.Fatalf("the fetch wrote to the account's %s; host keys belong in the run dir", accountKnownHosts)
	}
}

// The git server key pin (backlog 2.41) over the real ssh fixture: the
// first (unpinned) fetch captures what the server presented; a pinned
// fetch demands exactly that key (strict checking against the run's own
// known_hosts, pre-seeded from the claim); a changed key fails the fetch
// with an error naming the key the server now presents.
func TestFetchRepoOverSSHCapturesThePresentedHostKey(t *testing.T) {
	gitOnPATH(t)
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("no ssh client: %v", err)
	}
	deployPriv, deployPub := sshKey(t)
	url, fingerprint, publicKey, stop := serveGitOverSSH(t, gitFixture(t), deployPub)
	defer stop()

	cacheRoot, workDir := t.TempDir(), t.TempDir()
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: url, Branch: "main", Subpath: "playbooks/site.yml"}
	playbook, err := (&Runner{}).fetchRepo(context.Background(), spec, secretJSON(t, credentials.Secret{PrivateKey: deployPriv}), cacheRoot, workDir)
	if err != nil {
		t.Fatalf("fetchRepo over ssh: %v", err)
	}
	if _, err := os.Stat(playbook); err != nil {
		t.Fatal(err)
	}
	observed, err := worker.ObservedGitHostKey(filepath.Join(workDir, homeDirName, ".ssh", "known_hosts"), url)
	if err != nil || observed == nil {
		t.Fatalf("observed = %v, %v", observed, err)
	}
	if observed.Fingerprint != fingerprint || observed.PublicKey != publicKey {
		t.Fatalf("observed = %+v, want the fixture server's key", observed)
	}
}

// TestFetchRepoOverSSHPinnedEnforcement proves the pinned fetch: strict
// checking against the pre-seeded pin succeeds for the matching key (fresh
// clone included), the run's known_hosts carries exactly the pinned entry,
// and a pinned-and-verified fetch reports no observation.
func TestFetchRepoOverSSHPinnedEnforcement(t *testing.T) {
	gitOnPATH(t)
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("no ssh client: %v", err)
	}
	deployPriv, deployPub := sshKey(t)
	url, fingerprint, publicKey, stop := serveGitOverSSH(t, gitFixture(t), deployPub)
	defer stop()
	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: url, Branch: "main",
		Subpath: "playbooks/site.yml", HostKey: fingerprint, HostPublicKey: publicKey}
	secret := secretJSON(t, credentials.Secret{PrivateKey: deployPriv})

	cacheRoot, workDir := t.TempDir(), t.TempDir()
	playbook, err := (&Runner{}).fetchRepo(context.Background(), spec, secret, cacheRoot, workDir)
	if err != nil {
		t.Fatalf("pinned fetch: %v", err)
	}
	if _, err := os.Stat(playbook); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workDir, homeDirName, ".ssh", "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "[127.0.0.1]:" + urlPort(url) + " " + publicKey + "\n"; string(raw) != want {
		t.Fatalf("known_hosts = %q, want the seeded pin %q", raw, want)
	}
	if _, observed, err := (&Runner{}).FetchRepoDir(context.Background(), worker.RepoSpec{
		OrgID: orgID, RepoID: repoID, URL: url, Branch: "main",
		HostKey: fingerprint, HostPublicKey: publicKey,
	}, secret, cacheRoot, t.TempDir()); err != nil || observed != nil {
		t.Fatalf("pinned fetch observation = %v, %v; want none", observed, err)
	}
}

// TestFetchRepoOverSSHRefusesAChangedHostKey is the changed-key refusal
// (backlog 2.41): the same server address starts presenting a different
// host key. The pinned fetch fails closed — ssh refuses the connection
// before any data is transferred — and the error names the key the server
// now presents (the new fingerprint, from ssh's own diagnostic).
func TestFetchRepoOverSSHRefusesAChangedHostKey(t *testing.T) {
	gitOnPATH(t)
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("no ssh client: %v", err)
	}
	deployPriv, deployPub := sshKey(t)
	dir := gitFixture(t)
	url, fingerprint, publicKey, stopFirst := serveGitOverSSH(t, dir, deployPub)

	// First fetch: TOFU (an unpinned sync would pin what it saw).
	cacheRoot := t.TempDir()
	if _, _, err := (&Runner{}).FetchRepoDir(context.Background(), worker.RepoSpec{
		OrgID: orgID, RepoID: repoID, URL: url, Branch: "main",
	}, secretJSON(t, credentials.Secret{PrivateKey: deployPriv}), cacheRoot, t.TempDir()); err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	// The git server rekeys: a fresh server presents a NEW host key on the
	// same address — the scenario a changed key is.
	stopFirst()
	newHostSigner := sshHostSigner(t)
	rekeyedURL, newFingerprint, _, stopSecond := serveGitOverSSHOn(t, dir, deployPub, newHostSigner, urlPort(url))
	defer stopSecond()

	spec := gitRepoSpec{OrgID: orgID, RepoID: repoID, URL: rekeyedURL, Branch: "main",
		Subpath: "playbooks/site.yml", HostKey: fingerprint, HostPublicKey: publicKey}
	_, err := (&Runner{}).fetchRepo(context.Background(), spec, secretJSON(t, credentials.Secret{PrivateKey: deployPriv}), cacheRoot, t.TempDir())
	if err == nil {
		t.Fatal("a fetch against a changed git server key must be refused")
	}
	if !strings.Contains(err.Error(), newFingerprint) {
		t.Fatalf("the refusal must name the key the server now presents (%q): %v", newFingerprint, err)
	}
}

// serveGitOverSSH serves the working repository at dir read-only over SSH
// (git-upload-pack only), accepting only deployKey, on a fresh host key,
// and returns its ssh:// URL, the host key's fingerprint and its
// authorized-key line — the TOFU pin a first sync would report and enforce
// against — plus the server's stop function (the rekey test stops and
// restarts it on the same address).
func serveGitOverSSH(t *testing.T, dir string, deployKey ssh.PublicKey) (url, fingerprint, publicKey string, stop func()) {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	url, fingerprint, publicKey, stop = serveGitOverSSHOn(t, dir, deployKey, hostSigner, "")
	return url, fingerprint, publicKey, stop
}

// serveGitOverSSHOn is serveGitOverSSH with an explicit host key and an
// optional fixed port ("" binds an ephemeral one; a fixed port serves the
// changed-key scenario at the SAME address the first fetch dialed).
func serveGitOverSSHOn(t *testing.T, dir string, deployKey ssh.PublicKey, hostSigner ssh.Signer, port string) (url, fingerprint, publicKey string, stop func()) {
	t.Helper()
	srv := &glssh.Server{
		Handler: func(sess glssh.Session) {
			if cmd := sess.Command(); len(cmd) != 2 || cmd[0] != "git-upload-pack" {
				_ = sess.Exit(1)
				return
			}
			up := exec.Command("git", "upload-pack", dir)
			up.Stdin, up.Stdout, up.Stderr = sess, sess, sess.Stderr()
			if err := up.Run(); err != nil {
				_ = sess.Exit(1)
				return
			}
			_ = sess.Exit(0)
		},
		PublicKeyHandler: func(_ glssh.Context, key glssh.PublicKey) bool {
			return bytes.Equal(key.Marshal(), deployKey.Marshal())
		},
	}
	srv.AddHostKey(hostSigner)
	deadline := time.Now().Add(5 * time.Second)
	for {
		addr := "127.0.0.1:" + port
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			go func() { _ = srv.Serve(ln) }()
			stop = func() { _ = srv.Close() }
			t.Cleanup(stop)
			_, actualPort, _ := net.SplitHostPort(ln.Addr().String())
			fingerprint = ssh.FingerprintSHA256(hostSigner.PublicKey())
			publicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey())))
			return "ssh://git@127.0.0.1:" + actualPort + "/fixture.git", fingerprint, publicKey, stop
		}
		if port == "" || time.Now().After(deadline) {
			t.Fatalf("listen on %s: %v", addr, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// urlPort is the port part of an ssh://git@host:port/… URL.
func urlPort(url string) string {
	authority, _, _ := strings.Cut(strings.TrimPrefix(url, "ssh://"), "/")
	_, port, err := net.SplitHostPort(authority)
	if err != nil {
		return ""
	}
	return port
}

func readOrEmpty(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// sshHostSigner returns a fresh ed25519 host key's signer — the git
// server's host key, swappable for the changed-key scenario.
func sshHostSigner(t *testing.T) ssh.Signer {
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

// sshKey returns a fresh ed25519 key as an OpenSSH private key PEM and its
// public half.
func sshKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), sshPub
}
