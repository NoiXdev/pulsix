package main

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func recorder() (*[]svcState, func(svcState)) {
	var got []svcState
	return &got, func(s svcState) { got = append(got, s) }
}

func TestServeReportsRunningThenStopped(t *testing.T) {
	got, report := recorder()
	stop := make(chan struct{})
	started := make(chan struct{})
	body := func(ctx context.Context) int {
		close(started)
		<-ctx.Done()
		return 0
	}
	go func() { <-started; close(stop) }()
	if code := serve(stop, report, body, time.Second); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	want := []svcState{svcRunning, svcStopPending, svcStopped}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("reports = %v, want %v", *got, want)
	}
}

func TestServeGivesUpAfterStopWait(t *testing.T) {
	got, report := recorder()
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	stop := make(chan struct{})
	close(stop)
	start := time.Now()
	code := serve(stop, report, func(context.Context) int { <-block; return 0 }, 50*time.Millisecond)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if (*got)[len(*got)-1] != svcStopped {
		t.Fatalf("last report = %v", *got)
	}
}

func TestServePassesTheExitCode(t *testing.T) {
	got, report := recorder()
	code := serve(make(chan struct{}), report, func(context.Context) int { return 2 }, time.Second)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if (*got)[len(*got)-1] != svcStopped {
		t.Fatalf("last report = %v", *got)
	}
}
