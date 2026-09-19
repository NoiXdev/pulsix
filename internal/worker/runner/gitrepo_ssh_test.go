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

	glssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/credentials"
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
	url := serveGitOverSSH(t, gitFixture(t), deployPub)

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

// serveGitOverSSH serves the working repository at dir read-only over SSH
// (git-upload-pack only), accepting only deployKey, and returns its
// ssh:// URL on 127.0.0.1.
func serveGitOverSSH(t *testing.T, dir string, deployKey ssh.PublicKey) string {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return "ssh://git@127.0.0.1:" + port + "/fixture.git"
}
