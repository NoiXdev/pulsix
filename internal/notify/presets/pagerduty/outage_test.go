package pagerduty_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/notify/presets/pagerduty"
	"github.com/noixdev/pulsix/internal/outbound"
)

// memChannels is the smallest channel store a Service needs.
type memChannels struct{ m map[uuid.UUID]notify.Channel }

func (s *memChannels) CreateChannel(_ context.Context, c notify.Channel) (notify.Channel, error) {
	c.Revision = 1
	s.m[c.ID] = c
	return c, nil
}

func (s *memChannels) GetChannel(_ context.Context, _, id uuid.UUID) (notify.Channel, error) {
	return s.m[id], nil
}

func (s *memChannels) ListChannels(context.Context, uuid.UUID) ([]notify.Channel, error) {
	return nil, nil
}

func (s *memChannels) UpdateChannel(_ context.Context, c notify.Channel, _ int64) (notify.Channel, error) {
	s.m[c.ID] = c
	return c, nil
}

func (s *memChannels) DeleteChannel(context.Context, uuid.UUID, uuid.UUID) error { return nil }

// prefixBox "seals" by prefixing the associated data.
type prefixBox struct{}

func (prefixBox) SealAAD(plain, aad []byte) ([]byte, error) {
	return append(append([]byte{}, aad...), plain...), nil
}

func (prefixBox) OpenAAD(sealed, aad []byte) ([]byte, error) {
	if !bytes.HasPrefix(sealed, aad) {
		return nil, errors.New("wrong aad")
	}
	return sealed[len(aad):], nil
}

type noMail struct{}

func (noMail) SendMany(context.Context, []string, string, string) error { return nil }
func (noMail) Configured(context.Context) (bool, error)                 { return false, nil }

type closedSet struct {
	mu     sync.Mutex
	closed map[uuid.UUID]bool
}

func (c *closedSet) ClosedAlerts(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		out[id] = c.closed[id]
	}
	return out, nil
}

// pagerFake answers 503 while down and records the events it accepts.
type pagerFake struct {
	mu       sync.Mutex
	down     bool
	accepted []string
}

func (p *pagerFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var e struct {
		EventAction string `json:"event_action"`
	}
	_ = json.Unmarshal(body, &e)
	p.accepted = append(p.accepted, e.EventAction)
	w.WriteHeader(http.StatusAccepted)
}

// TestPagerDutyOutageNeverReopensAClosedAlert (review I1): during an
// outage an alert opens and closes; the resolve gets through first and
// the opening's retry comes after it. It is sent as a resolve too, so no
// incident is left open for an alert that is closed.
func TestPagerDutyOutageNeverReopensAClosedAlert(t *testing.T) {
	fake := &pagerFake{down: true}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ps := notify.Presets{"pagerduty": pagerduty.New(srv.URL)}
	store := &memChannels{m: map[uuid.UUID]notify.Channel{}}
	svc := notify.NewService(notify.Deps{Channels: store, Box: prefixBox{}, Mail: noMail{}, Presets: ps})
	org := uuid.Must(uuid.NewV7())
	ch, err := svc.CreateChannel(context.Background(), org, uuid.Nil, notify.Input{
		Kind: notify.KindWebhook, Name: "pager", Enabled: true,
		Webhook: &notify.WebhookInput{Preset: "pagerduty", Secrets: map[string]string{"routing_key": key}},
	})
	if err != nil {
		t.Fatal(err)
	}
	states := &closedSet{closed: map[uuid.UUID]bool{}}
	client := outbound.NewClient(outbound.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, nil), 2*time.Second, nil)
	sender := notify.NewChannelSender(prefixBox{}, noMail{}, client, nil, ps, notify.WithAlertStates(states))
	alert := uuid.Must(uuid.NewV7())
	batch := func(event string) notify.Event {
		return notify.Event{Kind: notify.EventAlertBatch, Org: notify.OrgRef{ID: org, Name: "Acme"}, Alerts: &notify.AlertBatch{Count: 1, Items: []notify.AlertItem{
			{AlertID: alert, HostName: "web-1", CheckKey: "cpu", Metric: "used_percent", Severity: "crit", Event: event},
		}}}
	}
	send := func(ev notify.Event) error {
		return sender.Send(context.Background(), notify.Claimed{Channel: ch, Delivery: notify.Delivery{ID: uuid.Must(uuid.NewV7())}}, ev)
	}

	if err := send(batch("opened")); err == nil || notify.IsPermanent(err) {
		t.Fatalf("during the outage = %v", err)
	}
	states.closed[alert] = true // the alert closes while the opening waits
	fake.mu.Lock()
	fake.down = false
	fake.mu.Unlock()
	if err := send(batch("closed")); err != nil {
		t.Fatal(err)
	}
	if err := send(batch("opened")); err != nil { // the retry, now late
		t.Fatal(err)
	}
	if len(fake.accepted) != 2 || fake.accepted[0] != "resolve" || fake.accepted[1] != "resolve" {
		t.Fatalf("accepted %v", fake.accepted)
	}
}
