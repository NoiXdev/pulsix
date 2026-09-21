// The claim loop against fakes (workerpb.WorkerServiceClient is an
// interface, so the loop tests fake the transport entirely) and against the
// real mTLS bufconn harness for the end-to-end shape.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/grpcsrv"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"unicode/utf8"
)

// fakeRunStream is one RunEvents stream a test controls: the test feeds
// directives; the loop's sends land in sent.
type fakeRunStream struct {
	ctx        context.Context
	directives chan *workerpb.ManagerDirective

	mu      sync.Mutex
	sent    []*workerpb.RunEvent
	sendErr error
	closed  bool
	closedC chan struct{}
}

func newFakeRunStream(ctx context.Context) *fakeRunStream {
	return &fakeRunStream{
		ctx:        ctx,
		directives: make(chan *workerpb.ManagerDirective, 4),
		closedC:    make(chan struct{}),
	}
}

func (s *fakeRunStream) Send(ev *workerpb.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, ev)
	return nil
}

func (s *fakeRunStream) Recv() (*workerpb.ManagerDirective, error) {
	select {
	case d := <-s.directives:
		return d, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case <-s.closedC:
		return nil, errors.New("stream closed")
	}
}

func (s *fakeRunStream) CloseSend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.closedC)
	}
	return nil
}

func (s *fakeRunStream) push(d *workerpb.ManagerDirective) { s.directives <- d }

func (s *fakeRunStream) events() []*workerpb.RunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*workerpb.RunEvent(nil), s.sent...)
}

// The remaining grpc.ClientStream pieces (grpc.BidiStreamingClient embeds it).
func (s *fakeRunStream) Header() (metadata.MD, error) { return nil, nil }
func (s *fakeRunStream) Trailer() metadata.MD         { return nil }
func (s *fakeRunStream) SendMsg(any) error            { return nil }
func (s *fakeRunStream) RecvMsg(any) error            { return nil }
func (s *fakeRunStream) Context() context.Context     { return s.ctx }

// fakeWorkerClient is the WorkerServiceClient the loop talks to in the
// fake-transport tests.
type fakeWorkerClient struct {
	mu sync.Mutex

	claimQueue []*workerpb.ClaimJobResponse // popped per call; nil = queue empty
	claimErr   error                        // set → ClaimJob fails with it
	claims     []*workerpb.ClaimJobRequest

	runEventsErr error
	sendErr      error // every RunEvents stream fails its Sends with this
	streams      []*fakeRunStream

	reports []*workerpb.ReportResultRequest
}

func (f *fakeWorkerClient) ClaimJob(_ context.Context, in *workerpb.ClaimJobRequest, _ ...grpc.CallOption) (*workerpb.ClaimJobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims = append(f.claims, in)
	switch {
	case f.claimErr != nil:
		return nil, f.claimErr
	case len(f.claimQueue) == 0:
		return &workerpb.ClaimJobResponse{}, nil
	default:
		resp := f.claimQueue[0]
		f.claimQueue = f.claimQueue[1:]
		return resp, nil
	}
}

func (f *fakeWorkerClient) RunEvents(ctx context.Context, _ ...grpc.CallOption) (grpc.BidiStreamingClient[workerpb.RunEvent, workerpb.ManagerDirective], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runEventsErr != nil {
		return nil, f.runEventsErr
	}
	st := newFakeRunStream(ctx)
	st.mu.Lock()
	st.sendErr = f.sendErr
	st.mu.Unlock()
	f.streams = append(f.streams, st)
	return st, nil
}

func (f *fakeWorkerClient) ReportResult(_ context.Context, in *workerpb.ReportResultRequest, _ ...grpc.CallOption) (*workerpb.ReportResultResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, in)
	return &workerpb.ReportResultResponse{}, nil
}

func (f *fakeWorkerClient) Enroll(context.Context, *workerpb.EnrollRequest, ...grpc.CallOption) (*workerpb.EnrollResponse, error) {
	return nil, errors.New("not in this fake")
}

func (f *fakeWorkerClient) RenewCertificate(context.Context, *workerpb.RenewCertificateRequest, ...grpc.CallOption) (*workerpb.RenewCertificateResponse, error) {
	return nil, errors.New("not in this fake")
}

func (f *fakeWorkerClient) setClaimErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimErr = err
}

func (f *fakeWorkerClient) streamsNow() []*fakeRunStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeRunStream(nil), f.streams...)
}

func (f *fakeWorkerClient) reported() []*workerpb.ReportResultRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*workerpb.ReportResultRequest(nil), f.reports...)
}

func (f *fakeWorkerClient) claimsNow() []*workerpb.ClaimJobRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*workerpb.ClaimJobRequest(nil), f.claims...)
}

// scriptExecutor is the Executor fake: every Run call goes through run.
type scriptExecutor struct {
	mu    sync.Mutex
	calls int
	jobs  []*workerpb.Job

	run func(i int, execCtx context.Context, job *workerpb.Job, env RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error)
}

func (e *scriptExecutor) Run(execCtx context.Context, job *workerpb.Job, env RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
	e.mu.Lock()
	i := e.calls
	e.calls++
	e.jobs = append(e.jobs, job)
	e.mu.Unlock()
	return e.run(i, execCtx, job, env, sink)
}

func (e *scriptExecutor) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *scriptExecutor) job(i int) *workerpb.Job {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.jobs[i]
}

// cfgFor builds a Config over scratch dir with tiny timings.
func cfgFor(dir string, zones ...uuid.UUID) Config {
	return Config{
		ManagerAddr: "manager:8444", CertPath: "cert.pem", KeyPath: "key.pem", CAPath: "ca.pem",
		DataDir: dir, Zones: zones, ClaimInterval: 5 * time.Millisecond, MaxConcurrent: 1,
	}
}

// jobFor is a claimed job response the fake client queues up.
func jobFor(id uuid.UUID, token string) *workerpb.ClaimJobResponse {
	return &workerpb.ClaimJobResponse{Job: &workerpb.Job{
		Id: id.String(), Kind: "run", OrgId: orgID.String(), ZoneId: zoneID.String(), WorkerToken: token,
	}}
}

// runLoop starts the loop and returns its stop func and done channel.
func runLoop(t *testing.T, l *Loop) (cancel func(), done <-chan error) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	out := make(chan error, 1)
	go func() { out <- l.Run(ctx) }()
	return stop, out
}

// waitFor polls cond until it holds or the timeout lapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// TestLoopHappyRun: one claim, three events streamed in order with seq
// 1..3, one succeeded report with the stats. The loop's clock is injected:
// event timestamps and durations come from it, not the wall.
func TestLoopHappyRun(t *testing.T) {
	cfg := cfgFor(t.TempDir(), zoneID)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{
		jobFor(uuid.Must(uuid.NewV7()), "tok-1"),
	}}
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		for _, host := range []string{"web-1", "web-2", "db-1"} {
			if err := sink(&workerpb.RunEvent{Host: host, Task: "Run playbook", State: "ok", Payload: `{"x":1}`}); err != nil {
				return StatusError, nil, err
			}
		}
		return StatusSucceeded, map[string]any{"web-1": map[string]any{"ok": 2, "changed": 1}}, nil
	}}

	l := NewLoop(cfg, client, exec, WithNow(func() time.Time { return now }))
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}

	streams := client.streamsNow()
	if len(streams) != 1 {
		t.Fatalf("streams = %d, want 1", len(streams))
	}
	events := streams[0].events()
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	for i, ev := range events {
		if want := uint64(i + 1); ev.GetSeq() != want {
			t.Errorf("event %d seq = %d, want %d", i, ev.GetSeq(), want)
		}
		if ev.GetJobId() == "" {
			t.Errorf("event %d carries no job id", i)
		}
		if ev.GetAt() == nil {
			t.Errorf("event %d carries no timestamp", i)
		} else if got := ev.GetAt().AsTime(); !got.Equal(now) {
			t.Errorf("event %d at = %s, want the injected clock's %s", i, got, now)
		}
	}
	if events[0].GetHost() != "web-1" || events[1].GetHost() != "web-2" || events[2].GetHost() != "db-1" {
		t.Errorf("events out of order: %v", events)
	}

	reports := client.reported()
	if reports[0].Status != string(StatusSucceeded) {
		t.Errorf("status = %q, want succeeded", reports[0].Status)
	}
	if reports[0].Error != "" {
		t.Errorf("error = %q, want empty", reports[0].Error)
	}
	var stats map[string]any
	if err := json.Unmarshal([]byte(reports[0].Stats), &stats); err != nil {
		t.Fatalf("stats %q not JSON: %v", reports[0].Stats, err)
	}
	if stats["web-1"] == nil {
		t.Errorf("stats missing web-1: %v", stats)
	}
}

// TestLoopExecutorFailed: the executor reports its own failure with stats;
// the loop reports failed with exactly that outcome.
func TestLoopExecutorFailed(t *testing.T) {
	cfg := cfgFor(t.TempDir())
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok")}}
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		return StatusFailed, map[string]any{"web-1": map[string]any{"failed": 1}}, nil
	}}

	cancel, done := runLoop(t, NewLoop(cfg, client, exec))
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	<-done

	reports := client.reported()
	if reports[0].Status != string(StatusFailed) {
		t.Errorf("status = %q, want failed", reports[0].Status)
	}
	var stats map[string]any
	if err := json.Unmarshal([]byte(reports[0].Stats), &stats); err != nil {
		t.Fatalf("stats %q not JSON: %v", reports[0].Stats, err)
	}
	if stats["web-1"] == nil {
		t.Errorf("stats missing web-1: %v", stats)
	}
}

// TestLoopExecutorError: an executor-level failure is reported as the error
// status with the executor's reason.
func TestLoopExecutorError(t *testing.T) {
	cfg := cfgFor(t.TempDir())
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok")}}
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		return StatusFailed, nil, errors.New("ansible-runner exited 2")
	}}

	cancel, done := runLoop(t, NewLoop(cfg, client, exec))
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	<-done

	reports := client.reported()
	if reports[0].Status != string(StatusError) {
		t.Errorf("status = %q, want error", reports[0].Status)
	}
	if reports[0].Error != "ansible-runner exited 2" {
		t.Fatalf("error = %q, want the executor's reason", reports[0].Error)
	}
}

// TestLoopUnknownStatusNormalized: a rogue Status value cannot reach the
// manager's closed status set; it becomes error.
func TestLoopUnknownStatusNormalized(t *testing.T) {
	cfg := cfgFor(t.TempDir())
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok")}}
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		return Status("somehow-fine"), nil, nil
	}}

	cancel, done := runLoop(t, NewLoop(cfg, client, exec))
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	<-done

	reports := client.reported()
	if reports[0].Status != string(StatusError) {
		t.Fatalf("status = %q, want error", reports[0].Status)
	}
	if !strings.Contains(reports[0].Error, "somehow-fine") {
		t.Fatalf("error %q must name the rejected status", reports[0].Error)
	}
}

// TestLoopCancelDirective: the manager's cancel directive cancels the
// executor's derived context and the job reports cancelled.
func TestLoopCancelDirective(t *testing.T) {
	cfg := cfgFor(t.TempDir())
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok")}}
	started := make(chan struct{})
	exec := &scriptExecutor{run: func(_ int, execCtx context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		close(started)
		<-execCtx.Done() // run until cancelled
		return StatusCancelled, nil, nil
	}}

	cancel, done := runLoop(t, NewLoop(cfg, client, exec))
	<-started
	streams := client.streamsNow()
	if len(streams) != 1 {
		t.Fatalf("streams = %d, want 1", len(streams))
	}
	streams[0].push(&workerpb.ManagerDirective{Cancel: true})
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	<-done

	reports := client.reported()
	if reports[0].Status != string(StatusCancelled) {
		t.Fatalf("status = %q, want cancelled", reports[0].Status)
	}
	if reports[0].Error != "" {
		t.Errorf("error = %q, want empty for a cancelled run", reports[0].Error)
	}
}

// TestLoopCancelMapsRawContextError: an executor that surfaces the raw
// context.Canceled still reports cancelled, not error.
func TestLoopCancelMapsRawContextError(t *testing.T) {
	cfg := cfgFor(t.TempDir())
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok")}}
	started := make(chan struct{})
	exec := &scriptExecutor{run: func(_ int, execCtx context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		close(started)
		<-execCtx.Done()
		return "", nil, execCtx.Err()
	}}

	cancel, done := runLoop(t, NewLoop(cfg, client, exec))
	<-started
	client.streamsNow()[0].push(&workerpb.ManagerDirective{Cancel: true})
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	<-done

	reports := client.reported()
	if reports[0].Status != string(StatusCancelled) {
		t.Fatalf("status = %q, want cancelled", reports[0].Status)
	}
}

// TestLoopStreamOpenFailure: an RPC failure opening the stream reports the
// job as error with the reason (the manager requeues via its reaper).
func TestLoopStreamOpenFailure(t *testing.T) {
	cfg := cfgFor(t.TempDir())
	client := &fakeWorkerClient{
		claimQueue:   []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok")},
		runEventsErr: errors.New("stream refused"),
	}
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		t.Error("executor must not run when the stream cannot open")
		return StatusSucceeded, nil, nil
	}}

	cancel, done := runLoop(t, NewLoop(cfg, client, exec))
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	<-done

	reports := client.reported()
	if reports[0].Status != string(StatusError) {
		t.Fatalf("status = %q, want error", reports[0].Status)
	}
	if !strings.Contains(reports[0].Error, "stream refused") {
		t.Fatalf("error %q must carry the reason", reports[0].Error)
	}
	if exec.count() != 0 {
		t.Fatal("executor must not run")
	}
}

// TestLoopSendErrorRespected: a failing stream Send surfaces through the
// sink — the loop forwards it instead of swallowing it, and the job ends
// with the error status and the send failure as the reason.
func TestLoopSendErrorRespected(t *testing.T) {
	cfg := cfgFor(t.TempDir())
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok")}}
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		err := sink(&workerpb.RunEvent{Host: "web-1", Task: "Run", State: "ok"})
		if err == nil {
			t.Error("sink must fail when the stream refuses sends")
		}
		return StatusFailed, nil, err
	}}

	l := NewLoop(cfg, client, exec)
	client.mu.Lock()
	client.sendErr = errors.New("send refused")
	client.mu.Unlock()
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	<-done

	reports := client.reported()
	if reports[0].Status != string(StatusError) {
		t.Fatalf("status = %q, want error", reports[0].Status)
	}
	if !strings.Contains(reports[0].Error, "send refused") {
		t.Fatalf("error %q must carry the send failure", reports[0].Error)
	}
}

// TestLoopWorkDirLifecycle: the executor gets a fresh private temp dir under
// the data dir with a persistent repo cache beside it; the temp dir is gone
// after the run, the cache is not.
func TestLoopWorkDirLifecycle(t *testing.T) {
	dir := t.TempDir()
	cfg := cfgFor(dir, zoneID)
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok")}}

	var created string
	createdDuring := make(chan struct{})
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, env RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		if env.RepoCacheDir != filepath.Join(dir, "repo-cache") {
			t.Errorf("RepoCacheDir = %q, want the persistent cache", env.RepoCacheDir)
		}
		if !strings.HasPrefix(env.WorkDir, dir+string(filepath.Separator)) {
			t.Errorf("WorkDir %q must live under DataDir", env.WorkDir)
		}
		fi, err := os.Stat(env.WorkDir)
		if err != nil {
			t.Fatalf("WorkDir missing during run: %v", err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("WorkDir perms %v are group/world accessible", fi.Mode().Perm())
		}
		if err := os.WriteFile(filepath.Join(env.WorkDir, "inventory.json"), []byte("[]"), 0o600); err != nil {
			t.Fatal(err)
		}
		created = env.WorkDir
		close(createdDuring)
		return StatusSucceeded, nil, nil
	}}

	cancel, done := runLoop(t, NewLoop(cfg, client, exec))
	<-createdDuring
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	<-done

	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("WorkDir %q must be removed after the run (stat err = %v)", created, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "repo-cache")); err != nil || !fi.IsDir() {
		t.Errorf("repo cache must persist after the run: %v", err)
	}
}

// TestLoopNoSecretsInLogs: a full fake run whose payload carries credential
// material logs none of it — ids, kinds and durations only.
func TestLoopNoSecretsInLogs(t *testing.T) {
	cfg := cfgFor(t.TempDir(), zoneID)
	var logs bytes.Buffer
	jobID := uuid.Must(uuid.NewV7())
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{{
		Job: &workerpb.Job{
			Id:           jobID.String(),
			Kind:         "run",
			OrgId:        orgID.String(),
			ZoneId:       zoneID.String(),
			WorkerToken:  "worker-token-never-logged",
			InventoryDoc: `{"all":{"hosts":{"web-1":{"password":"pulsix-inventory-secret"}}}}`,
			Connections:  map[string]string{"web-1": `{"password":"hunter2-sudo-password"}`},
			Credentials:  map[string]string{"cred-1": `{"private_key":"-----BEGIN OPENSSH PRIVATE KEY-----"}`},
			HostKeys:     map[string]string{"web-1": "SHA256:TOFUPINNEDFINGERPRINT"},
			ExtraVars:    map[string]string{"secret": "extra-var-secret"},
			KindPayload:  `{"bootstrap_password":"bootstrap-pw-secret"}`,
		},
	}}}
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		if err := sink(&workerpb.RunEvent{Host: "web-1", Task: "Run", State: "stdout", Payload: `{"line":"password is hunter2-sudo-password"}`}); err != nil {
			return StatusError, nil, err
		}
		return StatusSucceeded, nil, nil
	}}

	l := NewLoop(cfg, client, exec, WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}

	out := logs.String()
	if out == "" {
		t.Fatal("the run must log at all")
	}
	for _, s := range []string{
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"hunter2-sudo-password",
		"pulsix-inventory-secret",
		"SHA256:TOFUPINNEDFINGERPRINT",
		"extra-var-secret",
		"bootstrap-pw-secret",
		"worker-token-never-logged",
	} {
		if strings.Contains(out, s) {
			t.Fatalf("log leaks %q:\n%s", s, out)
		}
	}
	if !strings.Contains(out, jobID.String()) || !strings.Contains(out, "kind=run") {
		t.Fatalf("log must carry the job id and kind:\n%s", out)
	}
}

// TestLoopBackoffProgression: claim failures back off 1s → 2s, a successful
// claim (an empty queue) resets it, and an empty queue pauses exactly
// ClaimInterval.
func TestLoopBackoffProgression(t *testing.T) {
	cfg := cfgFor(t.TempDir())
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{nil, nil}} // two empty responses
	client.setClaimErr(errors.New("manager unreachable"))

	var mu sync.Mutex
	var sleeps []time.Duration
	sleep := func(_ context.Context, d time.Duration) error {
		mu.Lock()
		sleeps = append(sleeps, d)
		n := len(sleeps)
		mu.Unlock()
		switch n {
		case 2: // after the second failure's backoff: let the next claim succeed
			client.setClaimErr(nil)
		case 3: // that claim was an empty queue: reset done; break the next claim
			client.setClaimErr(errors.New("manager unreachable"))
		case 4: // then let it succeed again
			client.setClaimErr(nil)
		}
		return nil
	}

	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		return StatusSucceeded, nil, nil
	}}
	l := NewLoop(cfg, client, exec, WithSleep(sleep))

	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(sleeps) >= 5
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []time.Duration{
		time.Second,       // first failure
		2 * time.Second,   // second failure: doubles
		cfg.ClaimInterval, // empty queue: plain pause
		time.Second,       // failure again: reset after the successful claim
		cfg.ClaimInterval, // empty again
	}
	if len(sleeps) < len(want) {
		t.Fatalf("sleeps = %v, want at least %v", sleeps, want)
	}
	for i, w := range want {
		if sleeps[i] != w {
			t.Fatalf("sleeps = %v, want %v at index %d", sleeps, w, i)
		}
	}
}

// TestBackoff: the exponential shape is 1 s doubling, capped at 30 s —
// from the sixth failure on the delay stays at the cap.
func TestBackoff(t *testing.T) {
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second,
		8 * time.Second, 16 * time.Second,
		30 * time.Second, 30 * time.Second,
	}
	for shift, w := range want {
		if got := backoff(shift); got != w {
			t.Errorf("backoff(%d) = %s, want %s", shift, got, w)
		}
	}
	if got := backoff(1000); got != 30*time.Second {
		t.Errorf("backoff(1000) = %s, want the cap", got)
	}
}

// TestLoopUnmarshalableStatsReportedAsError: stats the JSON encoder cannot
// represent degrade the result to the error status with the failure named —
// the manager's run view is built on the stats, so a run must never be
// recorded successful without them.
func TestLoopUnmarshalableStatsReportedAsError(t *testing.T) {
	cfg := cfgFor(t.TempDir())
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok")}}
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		return StatusSucceeded, map[string]any{"web-1": make(chan int)}, nil // unmarshalable
	}}

	cancel, done := runLoop(t, NewLoop(cfg, client, exec))
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	<-done

	reports := client.reported()
	if reports[0].Status != string(StatusError) {
		t.Fatalf("status = %q, want error", reports[0].Status)
	}
	if reports[0].Error == "" || !strings.Contains(reports[0].Error, "report stats") {
		t.Fatalf("error %q must name the stats failure", reports[0].Error)
	}
}

// TestLoopDoesNotMutateExecutorEvent: the loop owns identity and time, and
// stamps them onto a CLONE of the executor's event (the documented contract
// at the sink) — the executor's own pointer must read back untouched, while
// the stream carries the stamped copy.
func TestLoopDoesNotMutateExecutorEvent(t *testing.T) {
	cfg := cfgFor(t.TempDir(), zoneID)
	jobID := uuid.Must(uuid.NewV7())
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(jobID, "tok")}}

	ev := &workerpb.RunEvent{Host: "web-1", Task: "Run playbook", State: "ok", Payload: `{"x":1}`}
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		if err := sink(ev); err != nil {
			return StatusError, nil, err
		}
		// Re-read after the loop consumed the event: a mutation would
		// corrupt a runner's own event batch.
		if ev.Host != "web-1" || ev.Task != "Run playbook" || ev.State != "ok" || ev.Payload != `{"x":1}` {
			t.Errorf("the loop mutated the executor's event: %+v", ev)
		}
		if ev.JobId != "" || ev.Seq != 0 || ev.GetAt() != nil {
			t.Errorf("the loop stamped identity onto the executor's event: job_id=%q seq=%d at=%v", ev.JobId, ev.Seq, ev.GetAt())
		}
		return StatusSucceeded, nil, nil
	}}

	cancel, done := runLoop(t, NewLoop(cfg, client, exec))
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	<-done

	events := client.streamsNow()[0].events()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	sent := events[0]
	if sent.GetJobId() != jobID.String() || sent.GetSeq() != 1 {
		t.Fatalf("streamed event = job_id %q seq %d, want the stamped copy", sent.GetJobId(), sent.GetSeq())
	}
	if sent.GetAt() == nil {
		t.Fatal("streamed event carries no timestamp")
	}
}

// TestLoopCleanShutdown: cancelling mid-wait exits Run with nil promptly.
func TestLoopCleanShutdown(t *testing.T) {
	cfg := cfgFor(t.TempDir())
	l := NewLoop(cfg, &fakeWorkerClient{}, &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		t.Error("no job is queued")
		return StatusSucceeded, nil, nil
	}})

	cancel, done := runLoop(t, l)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// TestLoopClaimsPresentConfiguredZones: the claim request names the zones
// from the config, or none when the certificate resolves them.
func TestLoopClaimsPresentConfiguredZones(t *testing.T) {
	other := uuid.Must(uuid.NewV7())
	for _, tc := range []struct {
		name  string
		zones []uuid.UUID
		want  int
	}{
		{name: "explicit zones", zones: []uuid.UUID{zoneID, other}, want: 2},
		{name: "no zones (certificate resolves)", zones: nil, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfgFor(t.TempDir(), tc.zones...)
			client := &fakeWorkerClient{}
			l := NewLoop(cfg, client, &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
				return StatusSucceeded, nil, nil
			}})
			cancel, done := runLoop(t, l)
			waitFor(t, 2*time.Second, func() bool { return len(client.claimsNow()) >= 1 })
			cancel()
			<-done
			claims := client.claimsNow()
			if len(claims[0].GetZoneIds()) != tc.want {
				t.Fatalf("zone ids = %v, want %d", claims[0].GetZoneIds(), tc.want)
			}
		})
	}
}

// TestLoopSequentialClaims: with MaxConcurrent = 1 a second queued job is
// claimed only after the first run finished.
func TestLoopSequentialClaims(t *testing.T) {
	cfg := cfgFor(t.TempDir())
	jobA, jobB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{
		jobFor(jobA, "tok-a"), jobFor(jobB, "tok-b"),
	}}
	started := make(chan struct{})
	release := make(chan struct{})
	exec := &scriptExecutor{run: func(i int, _ context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		if i == 0 {
			close(started)
			<-release // hold the run open; no second claim may happen meanwhile
		}
		return StatusSucceeded, nil, nil
	}}

	cancel, done := runLoop(t, NewLoop(cfg, client, exec))
	<-started
	time.Sleep(50 * time.Millisecond) // grace: a parallel claimer would claim here
	if got := len(client.claimsNow()); got != 1 {
		t.Fatalf("claims while run 1 in flight = %d, want 1", got)
	}
	close(release)
	waitFor(t, 2*time.Second, func() bool { return exec.count() == 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if exec.job(1).GetId() != jobB.String() {
		t.Fatalf("second run = job %s, want job B", exec.job(1).GetId())
	}
}

// TestLoopEndToEndHappy: the full worker path against the real mTLS
// harness — the claim binds the RunEvents stream via the metadata keys
// (job_id, worker_token), events land in the manager's recorder with the
// loop's seq, and the terminal report persists.
func TestLoopEndToEndHappy(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1-a", true)
	cfg.ClaimInterval = 10 * time.Millisecond
	jobID := uuid.Must(uuid.NewV7())
	h.claims.Enqueue(&grpcsrv.JobClaim{
		Job: workerpb.Job{
			Id: jobID.String(), Kind: "run", OrgId: orgID.String(), ZoneId: zoneID.String(),
			WorkerToken: "tok-" + jobID.String(),
		},
		Token: "tok-" + jobID.String(),
	})

	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		if err := sink(&workerpb.RunEvent{Host: "web-1", Task: "Run", State: "ok"}); err != nil {
			return StatusError, nil, err
		}
		if err := sink(&workerpb.RunEvent{Host: "web-1", Task: "Run", State: "changed"}); err != nil {
			return StatusError, nil, err
		}
		return StatusSucceeded, map[string]any{"web-1": map[string]any{"ok": 2}}, nil
	}}

	conn, err := h.dialWith(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	cancel, done := runLoop(t, NewLoop(cfg, workerpb.NewWorkerServiceClient(conn), exec))
	// ReportResult and the RunEvents stream are independent RPCs; the loop
	// reports only after the manager ended the stream (see
	// TestLoopReportsAfterEventStreamDrained). Both must be there before
	// the assertions.
	waitFor(t, 2*time.Second, func() bool {
		return len(h.results.Results()) == 1 && len(h.events.Events()) == 2
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}

	events := h.events.Events()
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("seqs = %d, %d; want 1, 2", events[0].Seq, events[1].Seq)
	}
	if events[0].JobID != jobID.String() || events[0].Host != "web-1" {
		t.Fatalf("event 0 = %+v", events[0])
	}
	if events[0].At.IsZero() {
		t.Fatal("event timestamp missing")
	}
	res := h.results.Results()[0]
	if res.JobID != jobID.String() || res.Status != "succeeded" {
		t.Fatalf("result = %+v", res)
	}
	if res.Stats["web-1"] == nil {
		t.Fatalf("stats = %v", res.Stats)
	}
}

// TestLoopReportsAfterEventStreamDrained: the manager's Finish reads the
// run's stored events (a connection test's result event, the sync and
// rotation outcomes), so the terminal ReportResult must not overtake the
// stream handler still storing the tail of the events. With a slow event
// store, every event is stored by the time the result lands.
func TestLoopReportsAfterEventStreamDrained(t *testing.T) {
	h := newHarness(t)
	h.events.setAppendDelay(150 * time.Millisecond)
	h.results.events = h.events
	cfg := h.issue("fra-1-a", true)
	cfg.ClaimInterval = 10 * time.Millisecond
	jobID := uuid.Must(uuid.NewV7())
	h.claims.Enqueue(&grpcsrv.JobClaim{
		Job: workerpb.Job{
			Id: jobID.String(), Kind: "connection_test", OrgId: orgID.String(), ZoneId: zoneID.String(),
			WorkerToken: "tok-" + jobID.String(),
		},
		Token: "tok-" + jobID.String(),
	})
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		for _, state := range []string{"started", "ok"} {
			if err := sink(&workerpb.RunEvent{Host: "web-1", Task: "connection_test", State: state}); err != nil {
				return StatusError, nil, err
			}
		}
		return StatusSucceeded, nil, nil
	}}

	conn, err := h.dialWith(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	cancel, done := runLoop(t, NewLoop(cfg, workerpb.NewWorkerServiceClient(conn), exec))
	waitFor(t, 5*time.Second, func() bool { return len(h.results.Results()) == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if got := h.results.storedEventsAtFinish(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("events stored when the result landed = %v, want [2]", got)
	}
}

// TestLoopEndToEndCancel: flipping the manager's cancelling state mid-run
// sends the directive through the real stream; the executor sees its
// context cancelled and reports cancelled.
func TestLoopEndToEndCancel(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1-a", true)
	cfg.ClaimInterval = 10 * time.Millisecond
	jobID := uuid.Must(uuid.NewV7())
	h.claims.Enqueue(&grpcsrv.JobClaim{
		Job: workerpb.Job{
			Id: jobID.String(), Kind: "run", OrgId: orgID.String(), ZoneId: zoneID.String(),
			WorkerToken: "tok-" + jobID.String(),
		},
		Token: "tok-" + jobID.String(),
	})

	started := make(chan struct{})
	exec := &scriptExecutor{run: func(_ int, execCtx context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		close(started)
		<-execCtx.Done()
		return StatusCancelled, nil, nil
	}}

	conn, err := h.dialWith(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	cancel, done := runLoop(t, NewLoop(cfg, workerpb.NewWorkerServiceClient(conn), exec))
	<-started
	h.events.SetCancelling(jobID, true)
	waitFor(t, 2*time.Second, func() bool {
		results := h.results.Results()
		return len(results) == 1 && results[0].Status == "cancelled"
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

// TestLoopSurvivesRedialDuringARun: a certificate renewal's redial
// (ReconnectingClient.Redial) firing WHILE a job's RunEvents stream is
// still open must not disturb the run — the stream keeps delivering
// events on the connection that opened it, and the job reports its real
// terminal status, not "error" from a broken stream (fix round 2,
// Important B — a blind 30-minute close timer previously could cut a
// run exactly like this one mid-flight).
func TestLoopSurvivesRedialDuringARun(t *testing.T) {
	h := newHarness(t)
	cfg := h.issue("fra-1-a", true)
	cfg.ClaimInterval = 10 * time.Millisecond
	jobID := uuid.Must(uuid.NewV7())
	h.claims.Enqueue(&grpcsrv.JobClaim{
		Job: workerpb.Job{
			Id: jobID.String(), Kind: "run", OrgId: orgID.String(), ZoneId: zoneID.String(),
			WorkerToken: "tok-" + jobID.String(),
		},
		Token: "tok-" + jobID.String(),
	})

	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialIdentity(t.Context(), cfg, id, []grpc.DialOption{h.bufconnDialer()})
	if err != nil {
		t.Fatal(err)
	}
	rc := NewReconnectingClient(conn, quiet())

	started := make(chan struct{})
	resume := make(chan struct{})
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		if err := sink(&workerpb.RunEvent{Host: "web-1", Task: "Run", State: "ok"}); err != nil {
			return StatusError, nil, err
		}
		close(started)
		<-resume // held open while the test redials underneath this run
		if err := sink(&workerpb.RunEvent{Host: "web-1", Task: "Run", State: "changed"}); err != nil {
			return StatusError, nil, err
		}
		return StatusSucceeded, map[string]any{"web-1": map[string]any{"ok": 2}}, nil
	}}

	cancel, done := runLoop(t, NewLoop(cfg, rc, exec))
	<-started

	rc.Redial(t.Context(), func(ctx context.Context) (*grpc.ClientConn, error) {
		return dialIdentity(ctx, cfg, id, []grpc.DialOption{h.bufconnDialer()})
	})
	close(resume)

	waitFor(t, 2*time.Second, func() bool {
		return len(h.results.Results()) == 1 && len(h.events.Events()) == 2
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}

	res := h.results.Results()[0]
	if res.Status != "succeeded" {
		t.Fatalf("result status = %q, want succeeded — the run must survive the reconnect, not end as error", res.Status)
	}
	if res.Stats["web-1"] == nil {
		t.Fatalf("stats = %v", res.Stats)
	}
	if len(h.events.Events()) != 2 {
		t.Fatalf("events = %d, want 2 (both delivered over the stream the run started with)", len(h.events.Events()))
	}
}

// TestLoopTruncatesOversizedEventPayloads: an event whose payload exceeds
// the per-event limit is still sent — the run's record keeps the event —
// but its payload is replaced by a bounded JSON document that says it was
// truncated, how big it was, and carries the payload's head. Events under
// the limit pass untouched.
func TestLoopTruncatesOversizedEventPayloads(t *testing.T) {
	cfg := cfgFor(t.TempDir(), zoneID)
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok-big")}}
	huge := `{"text":"` + strings.Repeat("z", 3<<20) + `"}`
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		if err := sink(&workerpb.RunEvent{Host: "web-1", Task: "big", State: "ok", Payload: huge}); err != nil {
			return StatusError, nil, err
		}
		if err := sink(&workerpb.RunEvent{Host: "web-1", Task: "small", State: "ok", Payload: `{"x":1}`}); err != nil {
			return StatusError, nil, err
		}
		return StatusSucceeded, map[string]any{}, nil
	}}
	l := NewLoop(cfg, client, exec)
	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(client.reported()) == 1 })
	cancel()
	<-done

	events := client.streamsNow()[0].events()
	if len(events) != 2 {
		t.Fatalf("events = %d, want both kept", len(events))
	}
	big := events[0]
	if len(big.GetPayload()) > workerpb.MaxEventPayloadBytes {
		t.Fatalf("truncated payload is %d bytes, over the %d limit", len(big.GetPayload()), workerpb.MaxEventPayloadBytes)
	}
	var doc struct {
		Truncated     bool   `json:"truncated"`
		OriginalBytes int    `json:"original_bytes"`
		Head          string `json:"head"`
	}
	if err := json.Unmarshal([]byte(big.GetPayload()), &doc); err != nil {
		t.Fatalf("truncated payload is not JSON: %v", err)
	}
	if !doc.Truncated || doc.OriginalBytes != len(huge) || !strings.HasPrefix(huge, doc.Head) || doc.Head == "" {
		t.Fatalf("truncated payload = truncated:%v original:%d head:%d bytes", doc.Truncated, doc.OriginalBytes, len(doc.Head))
	}
	if big.GetHost() != "web-1" || big.GetTask() != "big" || big.GetState() != "ok" {
		t.Fatalf("the truncated event lost its fields: %+v", big)
	}
	if events[1].GetPayload() != `{"x":1}` {
		t.Fatalf("a small payload must pass untouched: %q", events[1].GetPayload())
	}
}

// TestBoundEventPayloadCutMidRuneStaysSinglePass (backlog 2.51): the
// head's UTF-8 back-off is one strings.ToValidUTF8 pass — a multi-byte
// rune cut at the head boundary survives as replacement characters (the
// head keeps its byte bound), and an already valid head is untouched.
func TestBoundEventPayloadCutMidRuneStaysSinglePass(t *testing.T) {
	// 64 KiB of ASCII minus two bytes, then one 3-byte rune straddling the
	// boundary: the naive 64 KiB slice lands inside it.
	head := strings.Repeat("a", truncationHeadBytes-2)
	payload := head + "日" + strings.Repeat("b", 3<<20)
	bounded := BoundEventPayload(payload)
	if len(bounded) > workerpb.MaxEventPayloadBytes {
		t.Fatalf("bounded payload is %d bytes, over the %d limit", len(bounded), workerpb.MaxEventPayloadBytes)
	}
	var doc struct {
		Truncated     bool   `json:"truncated"`
		OriginalBytes int    `json:"original_bytes"`
		Head          string `json:"head"`
	}
	if err := json.Unmarshal([]byte(bounded), &doc); err != nil {
		t.Fatalf("bounded payload is not JSON: %v", err)
	}
	if !doc.Truncated || doc.OriginalBytes != len(payload) {
		t.Fatalf("truncation doc = truncated:%v original:%d", doc.Truncated, doc.OriginalBytes)
	}
	// The head is the payload's first 64 KiB with the split rune's
	// invalid tail replaced — equal up to the replacement, not a byte
	// prefix anymore.
	headPart := payload[:truncationHeadBytes-2]
	if doc.Head != headPart+"\uFFFD" {
		t.Fatalf("head = ...%q, want the %d-byte ASCII part plus the replacement character",
			doc.Head[len(doc.Head)-12:], truncationHeadBytes-2)
	}
	if len(doc.Head) != truncationHeadBytes-2+3 {
		t.Fatalf("head = %d bytes, want the %d-byte bound minus the split rune's invalid tail, plus the 3-byte replacement",
			len(doc.Head), truncationHeadBytes)
	}
	if !utf8.ValidString(doc.Head) {
		t.Fatal("head is not valid UTF-8")
	}
	if want := head + "\xEF\xBF\xBD"; doc.Head != want {
		t.Fatalf("head must end with the replacement character for the split rune")
	}

	// A valid payload passes untouched, as before.
	small := `{"x":"日"}`
	if got := BoundEventPayload(small); got != small {
		t.Fatalf("small payload rewritten: %q", got)
	}
}

// TestLoopUnauthenticatedLogDependsOnIdentityValidity: fix round 2
// (New Breakage 4) flips the "still valid" branch's default diagnosis —
// with the reconnecting client redialing immediately after every renewal
// and the server itself cycling every connection at least daily
// (MaxConnectionAge), a stale, not-yet-reconnected connection is now the
// rare cause of Unauthenticated while the certificate is still valid;
// revocation is the common one, so the message says so. A certificate
// that has actually expired gets a different, actionable recovery
// (re-enroll with a new token) instead of the revocation wording, which
// does not fit a worker that was never revoked at all.
func TestLoopUnauthenticatedLogDependsOnIdentityValidity(t *testing.T) {
	h := newHarness(t)
	icfg := h.issue("fra-1", true)
	id, err := LoadIdentity(icfg.CertPath, icfg.KeyPath)
	if err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, now time.Time) string {
		t.Helper()
		cfg := cfgFor(t.TempDir())
		client := &fakeWorkerClient{}
		client.setClaimErr(status.Error(codes.Unauthenticated, "denied"))
		var logs bytes.Buffer
		l := NewLoop(cfg, client, &scriptExecutor{}, WithIdentity(id), WithNow(func() time.Time { return now }),
			WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
			WithSleep(func(context.Context, time.Duration) error { return nil }))
		cancel, done := runLoop(t, l)
		waitFor(t, 2*time.Second, func() bool { return len(client.claimsNow()) >= 2 })
		cancel()
		<-done
		return logs.String()
	}

	valid := run(t, id.Leaf().NotBefore.Add(time.Hour))
	if !strings.Contains(valid, "revoked") {
		t.Fatalf("a still-valid certificate refused as Unauthenticated must be diagnosed as likely revoked:\n%s", valid)
	}
	if !strings.Contains(valid, "delete it") {
		t.Fatalf("the revoked diagnosis must point at deleting the worker on the Workers page:\n%s", valid)
	}

	// fix round 3: "re-enroll with a new token" alone does not work under
	// the SAME name — CreateEnrollment refuses ErrNameTaken for any
	// existing row, revoked or not — so the message must give a recovery
	// that actually works: revoke and delete the worker first (to free
	// the name), or enroll under a different name instead.
	expired := run(t, id.Leaf().NotAfter.Add(time.Hour))
	if !strings.Contains(expired, "expired") {
		t.Fatalf("an actually-expired certificate must be named as such, not diagnosed as revoked:\n%s", expired)
	}
	if !strings.Contains(expired, "revoke") || !strings.Contains(expired, "delete") {
		t.Fatalf("the expired recovery must say to revoke and delete the worker before reusing its name:\n%s", expired)
	}
	if !strings.Contains(expired, "new token") {
		t.Fatalf("the expired recovery must name re-enrolling with a new token:\n%s", expired)
	}
	if !strings.Contains(expired, "new name") {
		t.Fatalf("the expired recovery must also offer enrolling under a new name as the immediate alternative:\n%s", expired)
	}
}
