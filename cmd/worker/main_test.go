// The worker binary's wiring: the exit-code contract (configuration error
// 2, clean shutdown 0, anything else 1) and the dial-failure path.
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
	"google.golang.org/grpc"
)

// TestRunConfigError: a broken environment is exit 2, before any network
// activity.
func TestRunConfigError(t *testing.T) {
	if code := run(nil); code != 2 {
		t.Fatalf("run with no env: exit = %d, want 2", code)
	}
	if code := run([]string{"PULSIX_MANAGER_ADDR=http://bad"}); code != 2 {
		t.Fatalf("run with scheme addr: exit = %d, want 2", code)
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

// TestRunWorkerCleanShutdown: a loop whose claims fail still exits 0 when
// its context is cancelled — the shutdown, not the claim failure, decides
// the exit code.
func TestRunWorkerCleanShutdown(t *testing.T) {
	loop := worker.NewLoop(worker.Config{
		ManagerAddr: "manager:8444", CertPath: "cert.pem", KeyPath: "key.pem", CAPath: "ca.pem",
		DataDir: t.TempDir(), Zones: []uuid.UUID{uuid.Must(uuid.NewV7())},
		ClaimInterval: 5 * time.Millisecond, MaxConcurrent: 1,
	}, failingClient{}, stubExecutor{})

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

// TestStubExecutorReportsError: the placeholder executor never pretends a
// job succeeded.
func TestStubExecutor(t *testing.T) {
	status, stats, err := stubExecutor{}.Run(t.Context(), &workerpb.Job{Kind: "run"}, worker.RunEnv{}, func(*workerpb.RunEvent) error { return nil })
	if status != worker.StatusError {
		t.Fatalf("status = %q, want error", status)
	}
	if stats != nil {
		t.Fatalf("stats = %v, want nil", stats)
	}
	if err == nil {
		t.Fatal("the stub must return an error")
	}
}
