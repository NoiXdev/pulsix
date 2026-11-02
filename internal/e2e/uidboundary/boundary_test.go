package uidboundary

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	glssh "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/runner"
)

// envInImage is what the host-side driver (internal/e2e/playbook_uid_test.go)
// sets on `docker run`. Outside the image these tests cannot mean anything:
// uid 65533, the shared group, the setuid helper and ansible-runner are all
// properties of deploy/worker/Dockerfile, not of a developer's machine.
const envInImage = "PULSIX_UID_BOUNDARY_IN_IMAGE"

// The image's own fixed values, mirrored from deploy/worker/Dockerfile. The
// test asserts against these rather than reading them back from the
// configuration, so a Dockerfile that quietly changed one of them fails
// here.
const (
	workerUID = 65532
	playUID   = 65533
	dataDir   = "/var/lib/pulsix-worker"
	bundleDir = "/etc/pulsix-worker"
)

const orgID = "01900000-0000-7000-8000-0000000000aa"

func inImage(t *testing.T) {
	t.Helper()
	if os.Getenv(envInImage) == "" {
		t.Skipf("not inside the worker image (%s unset); internal/e2e's TestPlaybookUIDBoundary runs this", envInImage)
	}
	if os.Getuid() != workerUID {
		t.Fatalf("running as uid %d, want the image's worker uid %d", os.Getuid(), workerUID)
	}
}

// workerConfig is the configuration a worker in this image loads, built
// through worker.Load from the environment an operator would set. Going
// through Load on purpose: PULSIX_ANSIBLE_RUNNER_BIN's default is the bare
// name "ansible-runner" and PULSIX_WORKER_SETUID_HELPER's is a path, and
// both defaults are part of what has to work.
func workerConfig(t *testing.T) worker.Config {
	t.Helper()
	cfg, err := worker.Load([]string{
		"PULSIX_MANAGER_ADDR=manager:8444",
		"PULSIX_WORKER_CERT=" + filepath.Join(bundleDir, "cert.pem"),
		"PULSIX_WORKER_KEY=" + filepath.Join(bundleDir, "key.pem"),
		"PULSIX_CA_CERT=" + filepath.Join(bundleDir, "ca.pem"),
		"PULSIX_WORKER_DATA_DIR=" + dataDir,
		"PULSIX_WORKER_PLAY_UID=" + strconv.Itoa(playUID),
		"PULSIX_CLAIM_INTERVAL=20ms",
	})
	if err != nil {
		t.Fatalf("worker.Load: %v", err)
	}
	return cfg
}

// writeWorkerKey puts a stand-in for the worker's client key where the
// compose stack mounts the real one, owner-only, and loosens the directory
// so the loop's own hardening step has something to do.
func writeWorkerKey(t *testing.T) string {
	t.Helper()
	if err := os.Chmod(bundleDir, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", bundleDir, err)
	}
	key := filepath.Join(bundleDir, "key.pem")
	if err := os.WriteFile(key, []byte("-----BEGIN PRIVATE KEY-----\nnot a real key\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
	return key
}

// runOneJob drives the REAL worker through one real job: worker.Loop with
// the real runner.Runner executor, a fake manager that hands out exactly
// this claim, and the loop's own work dir creation, sharing, spawn and
// cleanup. Nothing about the run is staged by hand — the point of this
// file is that the previous version of this test staged the layout itself
// and the shipped image was broken in four ways it could not see.
func runOneJob(t *testing.T, job *workerpb.Job) (*workerpb.ReportResultRequest, []*workerpb.RunEvent, string) {
	t.Helper()
	cfg := workerConfig(t)

	var logs bytes.Buffer
	client := &fakeManager{job: job, reported: make(chan struct{})}
	executor := runner.NewRunner("", "", "", 0, slog.New(slog.NewTextHandler(&logs, nil)))
	loop := worker.NewLoop(cfg, client, executor, worker.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	select {
	case <-client.reported:
	case <-ctx.Done():
		t.Fatalf("the job was never reported:\n%s", logs.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Loop.Run = %v", err)
	}
	t.Logf("worker log:\n%s", logs.String())
	return client.result(), client.events(), logs.String()
}

// localhostJob is a run job whose play targets the worker itself — the
// exposure the whole feature exists for.
func localhostJob(t *testing.T, playbook string) *workerpb.Job {
	t.Helper()
	return &workerpb.Job{
		Id:           "01900000-0000-7000-8000-00000000001a",
		Kind:         "run",
		OrgId:        orgID,
		WorkerToken:  "token",
		Playbook:     playbook,
		InventoryDoc: `{"all": {"hosts": ["localhost"]}, "_meta": {"hostvars": {"localhost": {"ansible_connection": "local"}}}}`,
	}
}

// TestLocalhostPlayRunsUnderThePlayUID is the boundary itself, through the
// worker's own code path: the play runs, reports uid 65533, and the one
// thing it must not be able to do — read the worker's client key — fails
// with a permission error. The work dir is gone when the job ends, and the
// bundle directory has been tightened to owner-only.
func TestLocalhostPlayRunsUnderThePlayUID(t *testing.T) {
	inImage(t)
	key := writeWorkerKey(t)

	playbook := `
- hosts: localhost
  gather_facts: false
  tasks:
    - name: report the uid the play runs under
      ansible.builtin.command: id -u
      changed_when: false
    - name: read the worker's own client key
      ansible.builtin.slurp:
        src: ` + key + `
      ignore_errors: true
`
	result, events, _ := runOneJob(t, localhostJob(t, playbook))

	if got := result.GetStatus(); got != "succeeded" {
		t.Errorf("status = %q, want succeeded (error: %q)", got, result.GetError())
	}
	if !eventsContain(events, strconv.Itoa(playUID)) {
		t.Errorf("no event reports uid %d; the play did not run under the play uid:\n%s", playUID, eventDump(events))
	}
	if !eventsContain(events, "Permission denied") {
		t.Errorf("the play was NOT refused when reading %s — the boundary does not hold:\n%s", key, eventDump(events))
	}
	if leftovers := workDirs(t); len(leftovers) != 0 {
		t.Errorf("work dirs survived the run: %v — the data volume grows per job", leftovers)
	}
	info, err := os.Stat(bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("%s = %v after the run, want 0700: the loop must tighten it at start-up", bundleDir, got)
	}
}

// TestRepoPlaybookRunsUnderThePlayUID: a `repo:<id>:<subpath>` playbook is
// executed where it was cloned, under the worker's own repo cache, and is
// never copied into the work dir. Sharing the work dir therefore does
// nothing for it — the play's uid cannot even traverse the cache unless
// the fetch opens it. The cache must stay read-only to the play: one job
// that could write there could poison the next job's checkout.
func TestRepoPlaybookRunsUnderThePlayUID(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)

	repoID := "01900000-0000-7000-8000-0000000000bb"
	source := gitFixture(t)
	job := localhostJob(t, "")
	job.Id = "01900000-0000-7000-8000-00000000002a"
	job.Playbook = "repo:" + repoID + ":playbooks/site.yml"
	job.KindPayload = `{"repo":{"id":"` + repoID + `","url":"` + source + `","branch":"main","subpath":"playbooks/site.yml"}}`

	result, events, _ := runOneJob(t, job)

	if got := result.GetStatus(); got != "succeeded" {
		t.Fatalf("status = %q, want succeeded (error: %q):\n%s", got, result.GetError(), eventDump(events))
	}
	if !eventsContain(events, "from the repository") {
		t.Errorf("the repo playbook's own task never ran:\n%s", eventDump(events))
	}

	clone := filepath.Join(dataDir, "repo-cache", orgID, repoID)
	for p := clone; p != dataDir; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := info.Mode().Perm(); perm&0o050 != 0o050 {
			t.Errorf("%s = %v: the play uid cannot reach the checkout through it", p, perm)
		}
		if perm := info.Mode().Perm(); perm&0o020 != 0 {
			t.Errorf("%s = %v is group-writable; a job could poison the next job's checkout", p, perm)
		}
	}
}

// TestSSHTaskRunsUnderThePlayUID is the non-localhost half the
// documentation claims and nothing covered: the separation applies to the
// whole run, so an SSH task has to work under uid 65533 too. It is a real
// SSH connection — an in-process server on loopback, reached by the
// image's own ssh client, with the run's key materialized from the claim's
// credential the way a real job's is.
//
// The failure this guards against is concrete and has happened before in
// this image: OpenSSH looks up the running uid in passwd before it does
// anything else and refuses to start without an entry ("No user exists for
// uid"), and Ansible expands remote_tmp from that same passwd entry's home
// — which is why pulsix-play has both.
func TestSSHTaskRunsUnderThePlayUID(t *testing.T) {
	inImage(t)
	writeWorkerKey(t)

	clientPEM, clientPub := sshKeyPair(t)
	addr := startSSHServer(t, clientPub)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}

	job := localhostJob(t, `
- hosts: target
  gather_facts: false
  tasks:
    - name: run a command over SSH
      ansible.builtin.raw: id -u
      changed_when: false
`)
	job.Id = "01900000-0000-7000-8000-00000000003a"
	job.SshUsername = "pulsix"
	job.InventoryDoc = `{"all": {"hosts": ["target"]}, "_meta": {"hostvars": {"target": {` +
		`"ansible_host": "` + host + `", "ansible_port": ` + port + `, "ansible_user": "pulsix", "ansible_pipelining": true}}}}`
	job.Connections = map[string]string{
		"target": `{"host_id":"01900000-0000-7000-8000-0000000000cc","credential_id":"cred-ssh","bastion_source":"none"}`,
	}
	job.Credentials = map[string]string{
		"cred-ssh": `{"private_key": ` + mustJSON(t, clientPEM) + `}`,
	}

	result, events, logs := runOneJob(t, job)

	if strings.Contains(eventDump(events), "No user exists for uid") {
		t.Fatalf("the ssh client could not resolve uid %d in passwd:\n%s", playUID, eventDump(events))
	}
	if got := result.GetStatus(); got != "succeeded" {
		t.Fatalf("status = %q, want succeeded (error: %q):\n%s\n%s", got, result.GetError(), eventDump(events), logs)
	}
	// The raw module's output is the uid the task ran as on the far side;
	// the ssh CLIENT is what ran under the play uid, and it is the client
	// that used to fall over without a passwd entry.
	if !eventsContain(events, "ok=1") && !eventsContain(events, `"rc": 0`) {
		t.Errorf("no successful SSH task in the stream:\n%s", eventDump(events))
	}
	if leftovers := workDirs(t); len(leftovers) != 0 {
		t.Errorf("work dirs survived the run: %v", leftovers)
	}
}

// workDirs lists the job work dirs still sitting in the data dir.
func workDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("read %s: %v", dataDir, err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "job-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func eventsContain(events []*workerpb.RunEvent, want string) bool {
	for _, ev := range events {
		if strings.Contains(ev.GetPayload(), want) {
			return true
		}
	}
	return false
}

func eventDump(events []*workerpb.RunEvent) string {
	var b strings.Builder
	for _, ev := range events {
		fmt.Fprintf(&b, "%s %s\n", ev.GetState(), ev.GetPayload())
	}
	return b.String()
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// gitFixture builds a one-commit repository the repo fetch can clone over
// a file:// URL, with a task whose output identifies it in the stream.
func gitFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=pulsix", "GIT_AUTHOR_EMAIL=pulsix@example.org",
			"GIT_COMMITTER_NAME=pulsix", "GIT_COMMITTER_EMAIL=pulsix@example.org")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(dir, "playbooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	play := "- hosts: localhost\n  gather_facts: false\n  tasks:\n" +
		"    - name: a playbook from the repository\n      ansible.builtin.command: echo from the repository\n      changed_when: false\n"
	if err := os.WriteFile(filepath.Join(dir, "playbooks", "site.yml"), []byte(play), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "site")
	return dir
}

// sshKeyPair returns an OpenSSH private key (the shape a git_key or
// ssh_key credential carries) and its public half.
func sshKeyPair(t *testing.T) (string, ssh.PublicKey) {
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

// startSSHServer runs an exec-only SSH server on loopback, authenticating
// exactly authorized and running each session's command through /bin/sh —
// the same fixture shape internal/e2e's scenario uses against real
// Ansible.
func startSSHServer(t *testing.T, authorized ssh.PublicKey) string {
	t.Helper()
	addr, _ := serveSSH(t, authorized, nil)
	return addr
}

// serveSSH is startSSHServer with a hook and the server's host key: a
// non-nil answer sees each session's command first and, when it returns
// ok, its output replaces the command's — a managed host that lies about
// what its modules returned.
func serveSSH(t *testing.T, authorized ssh.PublicKey, answer func(cmd string) (string, bool)) (string, ssh.PublicKey) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	srv := &glssh.Server{
		Handler: func(sess glssh.Session) {
			if answer != nil {
				if out, ok := answer(sess.RawCommand()); ok {
					_, _ = io.Copy(io.Discard, sess)
					_, _ = io.WriteString(sess, out)
					_ = sess.Exit(0)
					return
				}
			}
			cmd := exec.Command("/bin/sh", "-c", sess.RawCommand())
			cmd.Stdout, cmd.Stderr = sess, sess.Stderr()
			stdin, err := cmd.StdinPipe()
			if err != nil {
				_ = sess.Exit(1)
				return
			}
			go func() { _, _ = io.Copy(stdin, sess); _ = stdin.Close() }()
			if err := cmd.Run(); err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					_ = sess.Exit(exit.ExitCode())
					return
				}
				_ = sess.Exit(1)
			}
		},
		PublicKeyHandler: func(_ glssh.Context, key glssh.PublicKey) bool {
			return bytes.Equal(key.Marshal(), authorized.Marshal())
		},
	}
	srv.AddHostKey(signer)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String(), signer.PublicKey()
}

// fakeManager is the manager side of the worker's gRPC port: it hands out
// one claim, collects the run's events, and records the terminal report.
type fakeManager struct {
	job *workerpb.Job

	mu       sync.Mutex
	handed   bool
	sent     []*workerpb.RunEvent
	report   *workerpb.ReportResultRequest
	reported chan struct{}
	once     sync.Once
	// stream is the RunEvents stream handed out by the one call this fake
	// ever serves; cancel pushes a directive down it the way a real
	// manager's cancel endpoint would.
	stream *fakeStream
}

func (f *fakeManager) ClaimJob(context.Context, *workerpb.ClaimJobRequest, ...grpc.CallOption) (*workerpb.ClaimJobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.handed {
		return &workerpb.ClaimJobResponse{}, nil
	}
	f.handed = true
	return &workerpb.ClaimJobResponse{Job: f.job}, nil
}

func (f *fakeManager) RunEvents(ctx context.Context, _ ...grpc.CallOption) (grpc.BidiStreamingClient[workerpb.RunEvent, workerpb.ManagerDirective], error) {
	s := &fakeStream{ctx: ctx, mgr: f, closed: make(chan struct{}), directives: make(chan *workerpb.ManagerDirective, 1)}
	f.mu.Lock()
	f.stream = s
	f.mu.Unlock()
	return s, nil
}

// cancel pushes a cancel directive down the run's stream, the way a real
// manager's cancel endpoint reaches a worker: through the same RunEvents
// bidi stream, not a side channel the loop never reads.
func (f *fakeManager) cancel() {
	f.mu.Lock()
	s := f.stream
	f.mu.Unlock()
	if s == nil {
		return
	}
	s.directives <- &workerpb.ManagerDirective{Cancel: true}
}

func (f *fakeManager) ReportResult(_ context.Context, in *workerpb.ReportResultRequest, _ ...grpc.CallOption) (*workerpb.ReportResultResponse, error) {
	f.mu.Lock()
	f.report = in
	f.mu.Unlock()
	f.once.Do(func() { close(f.reported) })
	return &workerpb.ReportResultResponse{}, nil
}

func (f *fakeManager) Enroll(context.Context, *workerpb.EnrollRequest, ...grpc.CallOption) (*workerpb.EnrollResponse, error) {
	return nil, errors.New("not in this fake")
}

func (f *fakeManager) RenewCertificate(context.Context, *workerpb.RenewCertificateRequest, ...grpc.CallOption) (*workerpb.RenewCertificateResponse, error) {
	return nil, errors.New("not in this fake")
}

func (f *fakeManager) result() *workerpb.ReportResultRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.report
}

func (f *fakeManager) events() []*workerpb.RunEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*workerpb.RunEvent(nil), f.sent...)
}

// fakeStream is one RunEvents stream: the worker sends, and the manager may
// push a directive: the cancel tests in cancel_test.go and
// cancel_nouid_test.go do.
type fakeStream struct {
	ctx        context.Context
	mgr        *fakeManager
	closed     chan struct{}
	once       sync.Once
	directives chan *workerpb.ManagerDirective
}

func (s *fakeStream) Send(ev *workerpb.RunEvent) error {
	s.mgr.mu.Lock()
	defer s.mgr.mu.Unlock()
	s.mgr.sent = append(s.mgr.sent, ev)
	return nil
}

func (s *fakeStream) Recv() (*workerpb.ManagerDirective, error) {
	select {
	case d := <-s.directives:
		return d, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case <-s.closed:
		return nil, io.EOF
	}
}

func (s *fakeStream) CloseSend() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *fakeStream) Header() (metadata.MD, error) { return nil, nil }
func (s *fakeStream) Trailer() metadata.MD         { return nil }
func (s *fakeStream) SendMsg(any) error            { return nil }
func (s *fakeStream) RecvMsg(any) error            { return nil }
func (s *fakeStream) Context() context.Context     { return s.ctx }
