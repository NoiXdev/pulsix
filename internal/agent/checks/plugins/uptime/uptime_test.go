package uptime

import (
	"context"
	"errors"
	"testing"
)

type fixtureUptime struct {
	secs uint64
	err  error
}

func (f fixtureUptime) Uptime(context.Context) (uint64, error) { return f.secs, f.err }

func TestUptimeCollectReportsSeconds(t *testing.T) {
	p := uptimePlugin{Uptime: fixtureUptime{secs: 86400}}
	obs, err := p.Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("got %d observations, want 1: %+v", len(obs), obs)
	}
	o := findObs(t, obs, "", "uptime.seconds")
	if o.Value == nil || *o.Value != 86400 || o.Err != "" {
		t.Fatalf("uptime.seconds = %+v, want 86400", o)
	}
}

func TestUptimeCollectProviderErrorIsAnErrObservation(t *testing.T) {
	p := uptimePlugin{Uptime: fixtureUptime{err: errors.New("no /proc")}}
	obs, err := p.Collect(context.Background(), nil)
	if err == nil {
		t.Fatal("err = nil, want the advisory error")
	}
	o := findObs(t, obs, "", "uptime.seconds")
	if o.Err == "" || o.Value != nil {
		t.Fatalf("uptime.seconds = %+v, want an error observation", o)
	}
}

func TestUptimeCollectDeadContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	obs, err := uptimePlugin{Uptime: fixtureUptime{secs: 1}}.Collect(ctx, nil)
	if err == nil || obs != nil {
		t.Fatalf("got %+v, %v; want nothing and the context error", obs, err)
	}
}

// TestNewWiresTheHostProvider: New is what the registry hands the
// scheduler, so it must name itself "uptime" and read the real host
// (gopsutil) rather than a nil provider that would report nothing. The
// provider itself is not called: these tests never touch the real host.
func TestNewWiresTheHostProvider(t *testing.T) {
	p, ok := New().(uptimePlugin)
	if !ok {
		t.Fatalf("New() = %T, want uptimePlugin", New())
	}
	if p.Name() != "uptime" {
		t.Errorf("Name() = %q, want uptime", p.Name())
	}
	if _, ok := p.Uptime.(gopsutilUptime); !ok {
		t.Errorf("Uptime = %T, want gopsutilUptime", p.Uptime)
	}
}
