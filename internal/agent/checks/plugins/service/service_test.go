// package service: the service plugin's tests run entirely against an
// injected unitLister fixture — no real systemd, no real D-Bus, on any
// platform this suite runs on.
package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

type fixtureUnitLister struct {
	results map[string]unitResult
	err     error
	called  bool
}

func (f *fixtureUnitLister) States(_ context.Context, _ []string) (map[string]unitResult, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

func TestServiceCollectHappyPath(t *testing.T) {
	lister := &fixtureUnitLister{results: map[string]unitResult{
		"sshd": {Label: "active"},
		"cron": {Label: "failed"},
	}}
	p := servicePlugin{Lister: lister}
	obs, err := p.Collect(context.Background(), map[string]any{"units": []string{"sshd", "cron"}})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2: %+v", len(obs), obs)
	}
	if o := findObs(t, obs, "sshd", "service.state"); o.Label == nil || *o.Label != "active" {
		t.Errorf("sshd label = %v, want active", o.Label)
	}
	if o := findObs(t, obs, "cron", "service.state"); o.Label == nil || *o.Label != "failed" {
		t.Errorf("cron label = %v, want failed", o.Label)
	}
	for _, o := range obs {
		if o.Value != nil {
			t.Errorf("service.state must be a label, not a value: %+v", o)
		}
	}
}

// TestServiceCollectEmptyUnitsListCollectsNothing pins the "empty units"
// case the brief calls out: nothing to check is not an error, and the
// lister — which would error if it were ever asked — must never be
// called.
func TestServiceCollectEmptyUnitsListCollectsNothing(t *testing.T) {
	cases := []map[string]any{
		{"units": []string{}},
		{}, // the key absent entirely
	}
	for _, params := range cases {
		lister := &fixtureUnitLister{err: errors.New("must not be called")}
		p := servicePlugin{Lister: lister}
		obs, err := p.Collect(context.Background(), params)
		if err != nil {
			t.Fatalf("collect(%v): %v", params, err)
		}
		if len(obs) != 0 {
			t.Fatalf("collect(%v) = %+v, want none", params, obs)
		}
		if lister.called {
			t.Errorf("collect(%v) called the lister with nothing to check", params)
		}
	}
}

// TestServiceCollectUnknownUnitReportsUnknownLabel is the "unknown unit"
// case the brief calls out: a unit the lister never resolved is a valid
// observation ("unknown"), never an error.
func TestServiceCollectUnknownUnitReportsUnknownLabel(t *testing.T) {
	lister := &fixtureUnitLister{results: map[string]unitResult{
		"sshd": {Label: "active"},
		// "ghost" is deliberately absent from the response.
	}}
	p := servicePlugin{Lister: lister}
	obs, err := p.Collect(context.Background(), map[string]any{"units": []string{"sshd", "ghost"}})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if o := findObs(t, obs, "ghost", "service.state"); o.Label == nil || *o.Label != "unknown" {
		t.Errorf("ghost label = %v, want unknown", o.Label)
	}
	if o := findObs(t, obs, "sshd", "service.state"); o.Label == nil || *o.Label != "active" {
		t.Errorf("sshd label = %v, want active (unaffected by ghost's absence)", o.Label)
	}
}

func TestServiceCollectPerUnitError(t *testing.T) {
	lister := &fixtureUnitLister{results: map[string]unitResult{
		"a": {Label: "active"},
		"b": {Err: errors.New("access denied")},
	}}
	p := servicePlugin{Lister: lister}
	obs, err := p.Collect(context.Background(), map[string]any{"units": []string{"a", "b", "c"}})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(obs) != 3 {
		t.Fatalf("obs = %+v, want 3", obs)
	}
	if o := findObs(t, obs, "a", "service.state"); o.Label == nil || *o.Label != "active" {
		t.Errorf("a = %+v, want active", o)
	}
	o := findObs(t, obs, "b", "service.state")
	if o.Err == "" || o.Label != nil || !strings.Contains(o.Err, "access denied") {
		t.Errorf("b = %+v, want an error observation carrying access denied", o)
	}
	if o := findObs(t, obs, "c", "service.state"); o.Label == nil || *o.Label != "unknown" {
		t.Errorf("c = %+v, want unknown", o)
	}
}

func TestServiceCollectKeepsTheRequestedName(t *testing.T) {
	lister := &fixtureUnitLister{results: map[string]unitResult{"W3svc": {Label: "active"}}}
	p := servicePlugin{Lister: lister}
	obs, err := p.Collect(context.Background(), map[string]any{"units": []string{"W3svc"}})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if o := findObs(t, obs, "W3svc", "service.state"); o.Label == nil || *o.Label != "active" {
		t.Errorf("W3svc = %+v, want active", o)
	}
}

// TestServiceCollectListerFaultFailsTheWholeBatch: ListUnits is one batched
// D-Bus round trip for every requested unit — its fault (no systemd, the
// non-Linux stub, a dead connection) fails every unit together, since
// there is no per-unit call to isolate. Every requested unit still gets
// its own service.state error observation rather than nothing at all, so
// the manager shows "collection failed" for each instead of leaving every
// one of them at its last-known state.
func TestServiceCollectListerFaultFailsTheWholeBatch(t *testing.T) {
	boom := errors.New("boom")
	p := servicePlugin{Lister: &fixtureUnitLister{err: boom}}
	obs, err := p.Collect(context.Background(), map[string]any{"units": []string{"sshd", "nginx"}})
	if err == nil {
		t.Fatal("a lister fault must surface")
	}
	if len(obs) != 2 {
		t.Fatalf("obs = %+v, want one error observation per requested unit", obs)
	}
	for _, u := range []string{"sshd", "nginx"} {
		o := findObs(t, obs, u, "service.state")
		if o.Err == "" || o.Value != nil || o.Label != nil {
			t.Errorf("%s service.state = %+v, want an error observation carrying no value or label", u, o)
		}
		if !strings.Contains(o.Err, "boom") {
			t.Errorf("%s error = %q, want it to carry the underlying fault", u, o.Err)
		}
	}
}

func TestServiceCollectNoListerConfiguredIsAnError(t *testing.T) {
	p := servicePlugin{}
	_, err := p.Collect(context.Background(), map[string]any{"units": []string{"sshd"}})
	if err == nil {
		t.Fatal("a plugin with no lister configured must error rather than silently report nothing")
	}
}

func TestServiceCollectMalformedUnitsParamIsAnError(t *testing.T) {
	p := servicePlugin{Lister: &fixtureUnitLister{}}
	_, err := p.Collect(context.Background(), map[string]any{"units": 5})
	if err == nil {
		t.Fatal("a malformed units param must be an error")
	}
}

func TestServiceCollectContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := servicePlugin{Lister: &fixtureUnitLister{}}
	obs, err := p.Collect(ctx, map[string]any{"units": []string{"sshd"}})
	if obs != nil {
		t.Fatalf("cancelled collection returned %v", obs)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestSystemdLabel(t *testing.T) {
	cases := []struct {
		name, load, active, want string
	}{
		{"active", "loaded", "active", "active"},
		{"inactive", "loaded", "inactive", "inactive"},
		{"failed", "loaded", "failed", "failed"},
		{"activating", "loaded", "activating", "activating"},
		{"not-found", "not-found", "inactive", "unknown"},
		{"unrecognized active state", "loaded", "reloading", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := systemdLabel(tc.load, tc.active); got != tc.want {
				t.Errorf("systemdLabel(%q, %q) = %q, want %q", tc.load, tc.active, got, tc.want)
			}
		})
	}
}

func TestServiceName(t *testing.T) {
	if (servicePlugin{}).Name() != "service" {
		t.Fatalf("Name() = %q, want service", (servicePlugin{}).Name())
	}
}

var (
	_ unitLister    = &fixtureUnitLister{}
	_ checks.Plugin = servicePlugin{}
)
