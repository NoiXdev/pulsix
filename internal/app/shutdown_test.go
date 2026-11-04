package app

import (
	"context"
	"testing"
	"time"
)

// fakeBackground stands in for the account service's background work at
// shutdown. It records how Close called it, and checks that the database
// is still open at that point, since the work it waits for writes there.
type fakeBackground struct {
	a           *App
	block       bool
	called      bool
	hadDeadline bool
	dbOpen      bool
}

func (f *fakeBackground) Shutdown(ctx context.Context) error {
	f.called = true
	_, f.hadDeadline = ctx.Deadline()
	f.dbOpen = f.a.db.Pool().Ping(ctx) == nil
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

// TestCloseWaitsForBackgroundWork: Close lets the reset requests still in
// the background finish before it closes the database, with a deadline.
func TestCloseWaitsForBackgroundWork(t *testing.T) {
	a, err := Build(context.Background(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if a.background == nil {
		a.Close()
		t.Fatal("Build wired no background work to wait for at shutdown")
	}
	fake := &fakeBackground{a: a}
	a.background = fake
	a.Close()
	if !fake.called || !fake.hadDeadline || !fake.dbOpen {
		t.Fatalf("Close: called %v, with a deadline %v, database open %v; want all three", fake.called, fake.hadDeadline, fake.dbOpen)
	}
}

// TestCloseBoundsTheBackgroundWait: background work that does not finish
// cannot hold Close up past backgroundDrainTimeout.
func TestCloseBoundsTheBackgroundWait(t *testing.T) {
	prev := backgroundDrainTimeout
	backgroundDrainTimeout = 20 * time.Millisecond
	t.Cleanup(func() { backgroundDrainTimeout = prev })

	a, err := Build(context.Background(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeBackground{a: a, block: true}
	a.background = fake
	done := make(chan struct{})
	go func() {
		a.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close waited for background work past its bound")
	}
	if !fake.called {
		t.Fatal("Close never waited for the background work")
	}
}
