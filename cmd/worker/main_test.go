// The worker binary's wiring: the exit-code contract (configuration error
// 2, clean shutdown 0, anything else 1) and the dial-failure path.
package main

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/runner"
	"github.com/noixdev/pulsix/internal/worker/supervisor"
	"google.golang.org/grpc"
)

// TestRunConfigError: a broken environment is exit 2, before any network
// activity.
func TestRunConfigError(t *testing.T) {
	if code := run(nil, noProbe); code != 2 {
		t.Fatalf("run with no env: exit = %d, want 2", code)
	}
	if code := run([]string{"PULSIX_MANAGER_ADDR=http://bad"}, noProbe); code != 2 {
		t.Fatalf("run with scheme addr: exit = %d, want 2", code)
	}
}

// noProbe is a supervisor probe that passes.
func noProbe() error { return nil }

// TestRunRefusesAHostTheSupervisorCannotRunOn: a worker that will run plays
// under the supervisor checks at start that this host gives the supervisor
// what it needs, and refuses as a configuration error (exit 2) if not, by
// the probe's own words, before any network activity. Otherwise the worker
// enrolls, reports healthy and fails every playbook job (backlog 2.112).
// Off Linux the worker runs no supervisor, so there is nothing to probe.
func TestRunRefusesAHostTheSupervisorCannotRunOn(t *testing.T) {
	probed := 0
	probe := func() error {
		probed++
		return errors.New("the play supervisor needs pidfds")
	}
	var logs strings.Builder
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(restore) })

	code := run([]string{
		"PULSIX_MANAGER_ADDR=manager.invalid:8444",
		"PULSIX_WORKER_DATA_DIR=" + t.TempDir(),
	}, probe)

	if runtime.GOOS != "linux" {
		if probed != 0 {
			t.Errorf("probed %d times off Linux, want 0: no supervisor runs here", probed)
		}
		return
	}
	if probed != 1 || code != 2 {
		t.Fatalf("probed %d times, exit %d; want one probe and exit 2", probed, code)
	}
	if !strings.Contains(logs.String(), "the play supervisor needs pidfds") {
		t.Errorf("log does not carry the probe's error:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "pulsix-worker starting") {
		t.Errorf("the worker went on starting after the probe failed:\n%s", logs.String())
	}
}

// TestEntryRoutesSupervisorArg: the reserved first argument makes the
// binary the play supervisor, decided before anything reads the config or
// the environment. With no further arguments the supervisor refuses (exit 1,
// no --grace); had entry fallen through to run, the nil environment would
// have failed config loading with exit 2 instead.
func TestEntryRoutesSupervisorArg(t *testing.T) {
	if code := entry([]string{"/pulsix-worker", supervisor.Arg}, nil); code != 1 {
		t.Fatalf("entry with the supervisor arg: exit = %d, want 1 (the supervisor's refusal)", code)
	}
}

// TestEntryWithoutSupervisorArgRunsTheWorker: anything else starts the
// worker as before, so a broken environment is still exit 2.
func TestEntryWithoutSupervisorArgRunsTheWorker(t *testing.T) {
	for _, args := range [][]string{
		{"/pulsix-worker"},
		{"/pulsix-worker", "serve"},
		// The reserved argument only counts in first position.
		{"/pulsix-worker", "x", supervisor.Arg},
	} {
		if code := entry(args, nil); code != 2 {
			t.Errorf("entry %v: exit = %d, want 2 (configuration error)", args, code)
		}
	}
}

// failingClient is a WorkerServiceClient whose every claim fails — enough
// to drive the loop into its clean-shutdown path without any transport.
type failingClient struct{}

func (failingClient) ClaimJob(context.Context, *workerpb.ClaimJobRequest, ...grpc.CallOption) (*workerpb.ClaimJobResponse, error) {
	return nil, errors.New("unreachable")
}

func (failingClient) RunEvents(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[workerpb.RunEvent, workerpb.ManagerDirective], error) {
	return nil, errors.New("unreachable")
}

func (failingClient) ReportResult(context.Context, *workerpb.ReportResultRequest, ...grpc.CallOption) (*workerpb.ReportResultResponse, error) {
	return nil, errors.New("unreachable")
}

func (failingClient) Enroll(context.Context, *workerpb.EnrollRequest, ...grpc.CallOption) (*workerpb.EnrollResponse, error) {
	return nil, errors.New("manager unreachable")
}

func (failingClient) RenewCertificate(context.Context, *workerpb.RenewCertificateRequest, ...grpc.CallOption) (*workerpb.RenewCertificateResponse, error) {
	return nil, errors.New("manager unreachable")
}

// TestRunWorkerCleanShutdown: a loop whose claims fail still exits 0 when
// its context is cancelled — the shutdown, not the claim failure, decides
// the exit code. The executor is the real wiring (the ansible-runner
// executor behind the kind dispatch); no claim ever reaches it here.
func TestRunWorkerCleanShutdown(t *testing.T) {
	runExec := runner.NewRunner("ansible-runner", "ansible-playbook", "git", 0, nil)
	loop := worker.NewLoop(worker.Config{
		ManagerAddr: "manager:8444", CertPath: "cert.pem", KeyPath: "key.pem", CAPath: "ca.pem",
		DataDir: t.TempDir(), Zones: []uuid.UUID{uuid.Must(uuid.NewV7())},
		ClaimInterval: 5 * time.Millisecond, MaxConcurrent: 1,
	}, failingClient{}, worker.NewCompositeExecutor(map[string]worker.Executor{
		"run":   runExec,
		"adhoc": runExec,
	}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- workerLoop(ctx, loop) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("workerLoop exit = %d, want 0 on clean shutdown", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workerLoop did not return after cancellation")
	}
}
