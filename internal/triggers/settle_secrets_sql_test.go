package triggers_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/triggers"
)

// TestSettleRefusesCollectedValuesOntoALaterSecret (backlog 2.139): a
// secret of a mapped name that appears after the values were collected
// stops the collected run when it is due: nothing is enqueued, the
// pending run is gone, and its deliveries are failed with a reason that
// names the variable, never the value. Run now refuses alike.
func TestSettleRefusesCollectedValuesOntoALaterSecret(t *testing.T) {
	w := newSettleWorld(t, "settle-shadow")
	c := w.create(t, 5, nil)
	w.deliver(t, c, "s3cret-value-9f")
	w.inv.names["release"] = true
	w.clock.Add(5 * time.Second)
	w.tick(t)
	if n := len(w.started()); n != 0 {
		t.Fatalf("%d runs started over a secret", n)
	}
	if p := w.pending(t, c.Trigger.ID); p != nil {
		t.Fatalf("pending after the refusal = %+v", p)
	}
	ds := w.deliveries(t, c.Trigger.ID)
	if len(ds) != 1 || ds[0].Outcome != triggers.OutcomeFailed || !strings.Contains(ds[0].Reason, "mapping target release: the inventory holds a secret") ||
		strings.Contains(ds[0].Reason, "s3cret-value-9f") {
		t.Fatalf("deliveries = %+v", ds)
	}

	w.inv.names["release"] = false
	c = w.create(t, 600, nil)
	w.deliver(t, c, "v2")
	w.inv.names["release"] = true
	res, err := w.svc.RunPending(w.scoped, w.org, c.Trigger.ID, uuid.Must(uuid.NewV7()))
	if err != nil || res.Outcome != triggers.OutcomeFailed || len(w.started()) != 0 || w.pending(t, c.Trigger.ID) != nil {
		t.Fatalf("RunPending over a secret = %+v, %v (runs %d)", res, err, len(w.started()))
	}
}

// TestSettleCheckFailureAtStart: a check that ends with the context keeps
// the pending run for the next tick, like an interrupted enqueue; any other
// check failure fails the window like a refused enqueue. Neither starts a
// run.
func TestSettleCheckFailureAtStart(t *testing.T) {
	w := newSettleWorld(t, "settle-shadow-err")
	c := w.create(t, 5, nil)
	w.deliver(t, c, "v1")
	w.scope.err = context.DeadlineExceeded
	w.clock.Add(5 * time.Second)
	w.tick(t)
	if p := w.pending(t, c.Trigger.ID); p == nil || len(w.started()) != 0 {
		t.Fatalf("an interrupted check dropped the pending run (%+v) or started one (%d)", p, len(w.started()))
	}
	w.scope.err = errors.New("boom")
	w.tick(t)
	if p := w.pending(t, c.Trigger.ID); p != nil || len(w.started()) != 0 {
		t.Fatalf("a failed check kept the pending run (%+v) or started one (%d)", p, len(w.started()))
	}
	if ds := w.deliveries(t, c.Trigger.ID); ds[0].Outcome != triggers.OutcomeFailed || strings.Contains(ds[0].Reason, "boom") ||
		!strings.Contains(ds[0].Reason, "could not be checked") {
		t.Fatalf("delivery = %+v", ds[0])
	}
}
