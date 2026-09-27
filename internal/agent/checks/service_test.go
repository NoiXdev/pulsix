// package checks: the service plugin's tests run entirely against an
// injected unitLister fixture — no real systemd, no real D-Bus, on any
// platform this suite runs on.
package checks

import (
	"context"
	"errors"
	"testing"
)

type fixtureUnitLister struct {
	states []unitState
	err    error
	called bool
}

func (f *fixtureUnitLister) ListUnits(_ context.Context, units []string) ([]unitState, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	return f.states, nil
}

func TestServiceCollectHappyPath(t *testing.T) {
	lister := &fixtureUnitLister{states: []unitState{
		{Name: "sshd", LoadState: "loaded", ActiveState: "active"},
		{Name: "cron", LoadState: "loaded", ActiveState: "failed"},
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
	lister := &fixtureUnitLister{states: []unitState{
		{Name: "sshd", LoadState: "loaded", ActiveState: "active"},
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

// TestServiceCollectNotFoundLoadStateReportsUnknown: systemd itself can
// answer a query for a made-up unit with a synthesized entry (LoadState
// "not-found") rather than omitting it — that must map to "unknown" too.
func TestServiceCollectNotFoundLoadStateReportsUnknown(t *testing.T) {
	lister := &fixtureUnitLister{states: []unitState{
		{Name: "ghost.service", LoadState: "not-found", ActiveState: "inactive"},
	}}
	p := servicePlugin{Lister: lister}
	obs, err := p.Collect(context.Background(), map[string]any{"units": []string{"ghost.service"}})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if o := findObs(t, obs, "ghost.service", "service.state"); o.Label == nil || *o.Label != "unknown" {
		t.Errorf("label = %v, want unknown", o.Label)
	}
}

// TestServiceCollectUnrecognizedActiveStateReportsUnknown: an ActiveState
// outside the plugin's known four (e.g. systemd's transient
// "deactivating") is not a judgement call the agent makes — it collapses
// to "unknown" like any other state it does not recognize.
func TestServiceCollectUnrecognizedActiveStateReportsUnknown(t *testing.T) {
	lister := &fixtureUnitLister{states: []unitState{
		{Name: "sshd", LoadState: "loaded", ActiveState: "deactivating"},
	}}
	p := servicePlugin{Lister: lister}
	obs, err := p.Collect(context.Background(), map[string]any{"units": []string{"sshd"}})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if o := findObs(t, obs, "sshd", "service.state"); o.Label == nil || *o.Label != "unknown" {
		t.Errorf("label = %v, want unknown", o.Label)
	}
}

// TestServiceCollectListerFaultFailsTheWholeBatch: ListUnits is one batched
// D-Bus round trip for every requested unit — its fault (no systemd, the
// non-Linux stub, a dead connection) fails every unit together, since
// there is no per-unit call to isolate.
func TestServiceCollectListerFaultFailsTheWholeBatch(t *testing.T) {
	boom := errors.New("boom")
	p := servicePlugin{Lister: &fixtureUnitLister{err: boom}}
	obs, err := p.Collect(context.Background(), map[string]any{"units": []string{"sshd"}})
	if err == nil {
		t.Fatal("a lister fault must surface")
	}
	if obs != nil {
		t.Fatalf("obs = %+v, want none", obs)
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

func TestServiceStateLabel(t *testing.T) {
	cases := []struct {
		name string
		in   unitState
		want string
	}{
		{name: "active", in: unitState{Name: "a", LoadState: "loaded", ActiveState: "active"}, want: "active"},
		{name: "inactive", in: unitState{Name: "a", LoadState: "loaded", ActiveState: "inactive"}, want: "inactive"},
		{name: "failed", in: unitState{Name: "a", LoadState: "loaded", ActiveState: "failed"}, want: "failed"},
		{name: "activating", in: unitState{Name: "a", LoadState: "loaded", ActiveState: "activating"}, want: "activating"},
		{name: "not-found", in: unitState{Name: "a", LoadState: "not-found", ActiveState: "inactive"}, want: "unknown"},
		{name: "zero value (never resolved)", in: unitState{}, want: "unknown"},
		{name: "unrecognized active state", in: unitState{Name: "a", LoadState: "loaded", ActiveState: "reloading"}, want: "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serviceStateLabel(tc.in); got != tc.want {
				t.Errorf("serviceStateLabel(%+v) = %q, want %q", tc.in, got, tc.want)
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
	_ unitLister = &fixtureUnitLister{}
	_ Plugin     = servicePlugin{}
)
