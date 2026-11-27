package notify_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/outbound"
)

// countPreset renders n requests keyed k0…k(n-1), each body its key.
type countPreset struct {
	openPreset
	n int
}

func (p countPreset) Render(notify.Event, notify.Target) ([]notify.Request, error) {
	out := make([]notify.Request, p.n)
	for i := range out {
		key := fmt.Sprintf("k%d", i)
		out[i] = notify.Request{Key: key, Body: []byte(`"` + key + `"`)}
	}
	return out, nil
}

// keyedReceiver answers each body (a key) with its status, after delay.
type keyedReceiver struct {
	mu     sync.Mutex
	delay  time.Duration
	status map[string]int
	got    []string
}

func (r *keyedReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	time.Sleep(r.delay)
	buf := make([]byte, 64)
	n, _ := req.Body.Read(buf)
	key := strings.Trim(string(buf[:n]), `"`)
	r.mu.Lock()
	r.got = append(r.got, key)
	status := r.status[key]
	r.mu.Unlock()
	if status == 0 {
		status = http.StatusAccepted
	}
	w.WriteHeader(status)
}

func (r *keyedReceiver) received() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

func countChannel(t *testing.T, w *world, ps notify.Presets, url string) notify.Channel {
	t.Helper()
	w.svc = notify.NewService(notify.Deps{Channels: w.channels, Box: fakeBox{}, Mail: w.mail, Presets: ps, Rand: rand.Reader})
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, presetInput("count "+uuid.NewString()[:6], "count", url+"/x/send", ""))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func shortClient(timeout time.Duration) *http.Client {
	return outbound.NewClient(outbound.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, nil), timeout, nil)
}

// TestPresetSendResumesAfterTheBudget (review I2): a 50-request delivery
// to a slow service stops at its send budget, well under the lease, and
// names what it still owes; the retries send only that, so every request
// arrives exactly once.
func TestPresetSendResumesAfterTheBudget(t *testing.T) {
	rec := &keyedReceiver{delay: 30 * time.Millisecond}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	ps := notify.Presets{"count": countPreset{n: 50}}
	w := newWorld(t)
	c := countChannel(t, w, ps, srv.URL)
	budget := 400 * time.Millisecond
	sender := notify.NewChannelSender(fakeBox{}, w.mail, shortClient(100*time.Millisecond), nil, ps, notify.WithSendBudget(budget))
	claim := notify.Claimed{Channel: c, Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7())}}
	attempts := 0
	for {
		attempts++
		start := time.Now()
		err := sender.Send(ctx, claim, notify.Event{Kind: notify.EventTest})
		if took := time.Since(start); took > budget+200*time.Millisecond {
			t.Fatalf("attempt %d took %v", attempts, took)
		}
		if err == nil {
			break
		}
		var partial *notify.PartialSendError
		if !errors.As(err, &partial) || notify.IsPermanent(err) || !strings.Contains(err.Error(), "time ran out") {
			t.Fatalf("attempt %d: %v", attempts, err)
		}
		claim.Delivery.PendingRecipients = partial.Pending
		if attempts > 20 {
			t.Fatal("never finished")
		}
	}
	got := rec.received()
	if attempts < 2 || len(got) != 50 {
		t.Fatalf("%d attempts, %d requests received", attempts, len(got))
	}
	slices.Sort(got)
	if len(slices.Compact(got)) != 50 {
		t.Fatalf("duplicates: %v", got)
	}
}

// TestPresetSendContinuesPastAFailure (review M1): one refused request
// does not drop the rest; the error names the failed ones (no URL); the
// retry sends only those, and ends permanently when only refusals remain.
func TestPresetSendContinuesPastAFailure(t *testing.T) {
	rec := &keyedReceiver{status: map[string]int{"k1": http.StatusBadRequest, "k3": http.StatusServiceUnavailable}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	ps := notify.Presets{"count": countPreset{n: 5}}
	w := newWorld(t)
	c := countChannel(t, w, ps, srv.URL)
	sender := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil, ps)
	claim := notify.Claimed{Channel: c, Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7())}}
	err := sender.Send(ctx, claim, notify.Event{Kind: notify.EventTest})
	var partial *notify.PartialSendError
	if !errors.As(err, &partial) || notify.IsPermanent(err) || !slices.Equal(partial.Pending, []string{"count:k1", "count:k3"}) ||
		!strings.Contains(err.Error(), "request 2: the endpoint answered 400") || !strings.Contains(err.Error(), "request 4: the endpoint answered 503") ||
		strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("first = %v", err)
	}
	if got := rec.received(); len(got) != 5 {
		t.Fatalf("received %v", got)
	}
	rec.mu.Lock()
	rec.status["k3"] = 0
	rec.mu.Unlock()
	claim.Delivery.PendingRecipients = partial.Pending
	err = sender.Send(ctx, claim, notify.Event{Kind: notify.EventTest})
	if !notify.IsPermanent(err) || !errors.As(err, &partial) || !slices.Equal(partial.Pending, []string{"count:k1"}) || !strings.HasPrefix(err.Error(), "1 of 5 requests still owed") {
		t.Fatalf("retry = %v", err)
	}
	if got := rec.received(); !slices.Equal(got[5:], []string{"k1", "k3"}) {
		t.Fatalf("retry sent %v", got[5:])
	}
}

// stateAware is a preset that follows alert state: its body lists the
// alerts closed at send time.
type stateAware struct{ openPreset }

func (stateAware) FollowsAlertState() {}

func (stateAware) Render(ev notify.Event, t notify.Target) ([]notify.Request, error) {
	var closed []string
	for _, it := range ev.Alerts.Items {
		if t.Closed[it.AlertID] {
			closed = append(closed, it.HostName)
		}
	}
	return []notify.Request{{Body: []byte(`"` + strings.Join(closed, ",") + `"`)}}, nil
}

type fakeStates struct {
	mu     sync.Mutex
	closed map[uuid.UUID]bool
	err    error
	orgs   []uuid.UUID
}

func (f *fakeStates) ClosedAlerts(_ context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orgs = append(f.orgs, orgID)
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		if f.closed[id] {
			out[id] = true
		}
	}
	return out, f.err
}

// TestPresetSendReadsAlertStatesAtSendTime (review I1): a preset that
// follows alert state sees which alerts are closed when the message goes
// out, read for the channel's organization; a failed read is retried.
// Other presets never cause the read.
func TestPresetSendReadsAlertStatesAtSendTime(t *testing.T) {
	rec := &keyedReceiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	a, b := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	ev := notify.Event{Kind: notify.EventAlertBatch, Alerts: &notify.AlertBatch{Count: 2, Items: []notify.AlertItem{
		{AlertID: a, HostName: "a", Event: "opened"}, {AlertID: b, HostName: "b", Event: "opened"},
	}}}
	ps := notify.Presets{"aware": stateAware{}, "fake": fakePreset{}}
	states := &fakeStates{closed: map[uuid.UUID]bool{b: true}}
	w := newWorld(t)
	w.svc = notify.NewService(notify.Deps{Channels: w.channels, Box: fakeBox{}, Mail: w.mail, Presets: ps, Rand: rand.Reader})
	c, err := w.svc.CreateChannel(ctx, w.org, w.actor, presetInput("aware", "aware", srv.URL+"/x/send", ""))
	if err != nil {
		t.Fatal(err)
	}
	sender := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil, ps, notify.WithAlertStates(states))
	claim := notify.Claimed{Channel: c, Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7())}}
	if err := sender.Send(ctx, claim, ev); err != nil {
		t.Fatal(err)
	}
	if got := rec.received(); len(got) != 1 || got[0] != "b" || len(states.orgs) != 1 || states.orgs[0] != w.org {
		t.Fatalf("received %v, read for %v", got, states.orgs)
	}
	states.err = errors.New("db down")
	if err := sender.Send(ctx, claim, ev); err == nil || notify.IsPermanent(err) {
		t.Fatalf("failed read = %v", err)
	}
	other, err := w.svc.CreateChannel(ctx, w.org, w.actor, presetInput("chat", "fake", srv.URL+"/y/send", "room"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(ctx, notify.Claimed{Channel: other, Delivery: claim.Delivery}, ev); err != nil || len(states.orgs) != 2 {
		t.Fatalf("other preset: %v, reads %d", err, len(states.orgs))
	}
}

// TestDispatcherKeepsGoingPastASlowService (review I2): a delivery of many
// requests to a slow service ends at its budget with its progress handed
// to the retry, and the channel claimed beside it is delivered in the
// same tick.
func TestDispatcherKeepsGoingPastASlowService(t *testing.T) {
	slow := &keyedReceiver{delay: 30 * time.Millisecond}
	slowSrv := httptest.NewServer(slow)
	defer slowSrv.Close()
	fast := &receiver{}
	fastSrv := httptest.NewServer(fast)
	defer fastSrv.Close()
	ps := notify.Presets{"count": countPreset{n: 50}}
	w := newWorld(t)
	slowCh := countChannel(t, w, ps, slowSrv.URL)
	fastCh := webhookChannel(t, w, fastSrv.URL+"/hook", "", false)
	payload := []byte(`{"kind":"test"}`)
	q := &fakeQueue{pending: []notify.Claimed{
		{Channel: slowCh, Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, Attempts: 1, Payload: payload}},
		{Channel: fastCh, Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, Attempts: 1, Payload: payload}},
	}}
	budget := 300 * time.Millisecond
	sender := notify.NewChannelSender(fakeBox{}, w.mail, shortClient(100*time.Millisecond), nil, ps, notify.WithSendBudget(budget))
	start := time.Now()
	if _, err := notify.NewDispatcher(q, sender, time.Second, nil, nil, quiet()).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > budget+time.Second {
		t.Fatalf("tick took %v", took)
	}
	slowOut, fastOut := q.outcomes[q.pendingIDs(0)], q.outcomes[q.pendingIDs(1)]
	if fastOut.kind != "complete" || len(fast.bodies) != 1 {
		t.Fatalf("fast = %+v", fastOut)
	}
	if sent := len(slow.received()); slowOut.kind != "retry" || len(slowOut.pending) != 50-sent || sent == 0 || sent == 50 {
		t.Fatalf("slow = %+v after %d sent", slowOut, sent)
	}
}

// TestPresetSendOwedKeysOfAnotherPreset (re-review N1): owed keys stored
// by one preset name no request of another (or of an older version),
// so the delivery sends everything instead of nothing.
func TestPresetSendOwedKeysOfAnotherPreset(t *testing.T) {
	rec := &keyedReceiver{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	ps := notify.Presets{"count": countPreset{n: 3}}
	w := newWorld(t)
	c := countChannel(t, w, ps, srv.URL)
	sender := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil, ps)
	for _, owed := range [][]string{{"pagerduty:alert:3"}, {"k1"}, {"0"}} {
		before := len(rec.received())
		claim := notify.Claimed{Channel: c, Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7()), PendingRecipients: owed}}
		if err := sender.Send(ctx, claim, notify.Event{Kind: notify.EventTest}); err != nil {
			t.Fatal(err)
		}
		if got := rec.received()[before:]; !slices.Equal(got, []string{"k0", "k1", "k2"}) {
			t.Fatalf("owed %v: sent %v", owed, got)
		}
	}
}

// TestDispatcherFinalFailureKeepsWhatIsOwed (re-review nit): the last
// attempt's owed keys go with the final failure, for a later Resend.
func TestDispatcherFinalFailureKeepsWhatIsOwed(t *testing.T) {
	rec := &keyedReceiver{status: map[string]int{"k1": http.StatusServiceUnavailable}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	ps := notify.Presets{"count": countPreset{n: 3}}
	w := newWorld(t)
	c := countChannel(t, w, ps, srv.URL)
	q := &fakeQueue{pending: []notify.Claimed{{Channel: c, Delivery: notify.Delivery{
		ID: uuid.Must(uuid.NewV7()), OrgID: w.org, Attempts: notify.MaxAttempts, Payload: []byte(`{"kind":"test"}`),
	}}}}
	sender := notify.NewChannelSender(fakeBox{}, w.mail, loopbackClient(), nil, ps)
	if _, err := notify.NewDispatcher(q, sender, time.Second, nil, nil, quiet()).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if o := q.outcomes[q.pendingIDs(0)]; o.kind != "fail" || !slices.Equal(o.pending, []string{"count:k1"}) {
		t.Fatalf("outcome = %+v", o)
	}
}
