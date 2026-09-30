package rules

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
)

// stuckStore is a batch that never finishes on its own: ProcessDue blocks
// until its context ends, which is what a batch blocked acquiring a second
// pool connection looks like from the outside (see batchDeadlineFactor).
type stuckStore struct{ entered chan struct{} }

func (s *stuckStore) ProcessDue(ctx context.Context, _ time.Time, _ int, _ func(context.Context, Due) error) (int, error) {
	close(s.entered)
	<-ctx.Done()
	return 0, ctx.Err()
}

func (s *stuckStore) Load(context.Context, Due) (HostLoad, error) { return HostLoad{}, nil }

func (s *stuckStore) Windows(context.Context, Due, []WindowRequest, time.Time) (map[WindowRequest]float64, error) {
	return nil, nil
}

func (s *stuckStore) Apply(context.Context, Due, Result) error { return nil }

// TestTheBatchDeadlineCutsAStuckBatchLoose is the effect the deadline
// exists for: a batch that would otherwise hold its connection and its row
// locks for ever ends by itself, and Tick returns so that Run can tick
// again. This test lives inside the package so it can shorten
// deadlineFloor — at its real 10 seconds, proving this would mean waiting
// ten of them on every run.
//
// Without the deadline this test does not fail with a message; it hangs
// until the package timeout, which is exactly the production failure in
// miniature.
func TestTheBatchDeadlineCutsAStuckBatchLoose(t *testing.T) {
	store := &stuckStore{entered: make(chan struct{})}
	tk := NewTicker(store,
		WithInterval(time.Millisecond),
		WithTickerLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	tk.deadlineFloor = 50 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, err := tk.Tick(context.Background())
		done <- err
	}()

	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the batch never started")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Tick returned %v, want the batch deadline's own error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stuck batch was never cut loose; Tick put no deadline on ProcessDue")
	}
}

// TestBatchDeadline pins the formula itself, including that the floor and
// the multiple each win where they should.
func TestBatchDeadline(t *testing.T) {
	cases := []struct {
		interval time.Duration
		floor    time.Duration
		want     time.Duration
	}{
		{interval: time.Hour, floor: minBatchDeadline, want: 4 * time.Hour},
		{interval: DefaultEvalInterval, floor: minBatchDeadline, want: time.Minute},
		{interval: time.Second, floor: minBatchDeadline, want: minBatchDeadline},
	}
	for _, tc := range cases {
		tk := NewTicker(nil, WithInterval(tc.interval))
		tk.deadlineFloor = tc.floor
		if got := tk.batchDeadline(); got != tc.want {
			t.Fatalf("batchDeadline(interval=%v, floor=%v) = %v, want %v", tc.interval, tc.floor, got, tc.want)
		}
	}
}

// TestApplyOutsideAClaimIsRefused: the write path may not fall back to the
// pool the way the reads do — four autocommitted statements with no row
// lock is precisely the atomicity this store exists to provide.
func TestApplyOutsideAClaimIsRefused(t *testing.T) {
	store := &EvalSQLStore{}
	err := store.Apply(context.Background(), Due{OrgID: uuid.Must(uuid.NewV7()), HostID: uuid.Must(uuid.NewV7())}, Result{})
	if !errors.Is(err, errNoClaimTx) {
		t.Fatalf("Apply outside a claim = %v, want errNoClaimTx", err)
	}
}
