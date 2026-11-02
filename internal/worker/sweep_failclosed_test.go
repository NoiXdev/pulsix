package worker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker/supervisor"
)

// The sweep runs as the uid it clears, so a leftover play process can kill
// it before it acts (backlog 7.35). The worker therefore checks from its
// own uid whether any process of the play uid is left, sweeps again, and
// stops rather than run another job next to a survivor.

func sweepLoop(t *testing.T, alive func(uint32) ([]int, error), client *fakeWorkerClient) (*Loop, string) {
	t.Helper()
	helper, record := fakeSetuidHelper(t)
	cfg := cfgFor(t.TempDir(), zoneID)
	cfg.PlayUID = 65533
	cfg.SetuidHelper = helper
	cfg.PlaySupervisor = "/usr/local/bin/pulsix-worker"
	exec := &scriptExecutor{run: func(_ int, _ context.Context, _ *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
		return StatusSucceeded, nil, nil
	}}
	return NewLoop(cfg, client, exec, withPlayUIDProbe(alive)), record
}

func sweepRuns(t *testing.T, record string) int {
	t.Helper()
	raw, _ := os.ReadFile(record)
	return strings.Count(string(raw), supervisor.SweepArg)
}

func TestSweepFailsClosedWhenAPlayUIDProcessSurvives(t *testing.T) {
	l, record := sweepLoop(t, func(uid uint32) ([]int, error) {
		if uid != 65533 {
			t.Errorf("probed uid %d, want the play uid", uid)
		}
		return []int{4242}, nil
	}, &fakeWorkerClient{})

	err := l.sweepPlayUID(context.Background())
	if err == nil || !strings.Contains(err.Error(), "4242") {
		t.Fatalf("sweepPlayUID with a survivor = %v, want an error naming it", err)
	}
	if n := sweepRuns(t, record); n != sweepAttempts {
		t.Fatalf("the sweep ran %d times, want %d attempts before giving up", n, sweepAttempts)
	}
}

func TestSweepRetriesUntilThePlayUIDIsClear(t *testing.T) {
	calls := 0
	l, record := sweepLoop(t, func(uint32) ([]int, error) {
		calls++
		if calls == 1 {
			return []int{4242}, nil
		}
		return nil, nil
	}, &fakeWorkerClient{})

	if err := l.sweepPlayUID(context.Background()); err != nil {
		t.Fatalf("sweepPlayUID = %v, want success once the uid is clear", err)
	}
	if n := sweepRuns(t, record); n != 2 {
		t.Fatalf("the sweep ran %d times, want 2", n)
	}
}

func TestSweepFailsClosedWhenTheProbeFails(t *testing.T) {
	l, _ := sweepLoop(t, func(uint32) ([]int, error) { return nil, os.ErrPermission }, &fakeWorkerClient{})
	if err := l.sweepPlayUID(context.Background()); err == nil {
		t.Fatal("sweepPlayUID with an unreadable process table = nil, want an error")
	}
}

func TestLoopRefusesToStartNextToASurvivor(t *testing.T) {
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{jobFor(uuid.Must(uuid.NewV7()), "tok")}}
	l, _ := sweepLoop(t, func(uint32) ([]int, error) { return []int{7}, nil }, client)

	err := l.Run(context.Background())
	if err == nil {
		t.Fatal("Run started next to a surviving play process")
	}
	if n := claimCount(client); n != 0 {
		t.Fatalf("Run claimed %d job(s) after its start-up sweep failed", n)
	}
}

func TestLoopStopsWhenTheSweepAfterAJobFails(t *testing.T) {
	client := &fakeWorkerClient{claimQueue: []*workerpb.ClaimJobResponse{
		jobFor(uuid.Must(uuid.NewV7()), "tok"),
		jobFor(uuid.Must(uuid.NewV7()), "tok2"),
	}}
	probes := 0
	l, _ := sweepLoop(t, func(uint32) ([]int, error) {
		probes++
		if probes == 1 {
			return nil, nil // the start-up sweep finds nothing
		}
		return []int{99}, nil // the job left a survivor
	}, client)

	done := make(chan error, 1)
	go func() { done <- l.Run(context.Background()) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "99") {
			t.Fatalf("Run = %v, want it to stop naming the survivor", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run kept going after the sweep failed")
	}
	if n := claimCount(client); n != 1 {
		t.Fatalf("Run claimed %d jobs, want only the one before the failed sweep", n)
	}
}

func claimCount(f *fakeWorkerClient) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.claims)
}
