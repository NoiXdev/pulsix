package agentenroll_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agentenroll"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

var ctx = context.Background()

type fakeTemplates map[uuid.UUID]templates.Template

func (f fakeTemplates) Get(_ context.Context, _, id uuid.UUID) (templates.Template, error) {
	t, ok := f[id]
	if !ok {
		return templates.Template{}, templates.ErrNotFound
	}
	return t, nil
}

type fakeTargets struct {
	targets  []uuid.UUID
	retry    uuid.UUID // the retried job's template
	failed   []uuid.UUID
	retryErr error
}

func (f *fakeTargets) LimitedRunTargets(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return f.targets, nil
}

func (f *fakeTargets) RetryTargets(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, uuid.UUID, []uuid.UUID, error) {
	return f.retry, uuid.Nil, f.failed, f.retryErr
}

type fakeAgents map[uuid.UUID]bool

func (f fakeAgents) HostsWithAgent(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		if f[id] {
			out[id] = true
		}
	}
	return out, nil
}

type fakeHosts []hosts.Host

func (f fakeHosts) ListHostsByIDs(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]hosts.Host, error) {
	var out []hosts.Host
	for _, h := range f {
		if slices.Contains(ids, h.ID) {
			out = append(out, h)
		}
	}
	return out, nil
}

type world struct {
	svc            *agentenroll.Service
	tmpls          fakeTemplates
	targets        *fakeTargets
	agents         fakeAgents
	org, on, off   uuid.UUID
	h1, h2, h3, jb uuid.UUID
}

func id() uuid.UUID { return uuid.Must(uuid.NewV7()) }

// newWorld: template on enrols agents, off does not; it targets web-1
// (which runs an agent), web-2 and web-3.
func newWorld() *world {
	w := &world{org: id(), on: id(), off: id(), h1: id(), h2: id(), h3: id(), jb: id()}
	w.tmpls = fakeTemplates{w.on: {ID: w.on, AgentEnrollment: true}, w.off: {ID: w.off}}
	w.targets = &fakeTargets{targets: []uuid.UUID{w.h3, w.h1, w.h2}, retry: w.on, failed: []uuid.UUID{w.h1, w.h3}}
	w.agents = fakeAgents{w.h1: true}
	hs := fakeHosts{{ID: w.h1, Name: "web-1"}, {ID: w.h2, Name: "web-2"}, {ID: w.h3, Name: "web-3"}}
	w.svc = agentenroll.NewService(w.tmpls, w.targets, w.agents, hs)
	return w
}

func ids(hs []agentenroll.Host) []uuid.UUID {
	out := make([]uuid.UUID, len(hs))
	for i, h := range hs {
		out[i] = h.ID
	}
	return out
}

// The preview of a run lists the targets without an agent by name; a
// template without the flag enrols nothing; one host narrows it.
func TestRunPreview(t *testing.T) {
	w := newWorld()
	p, err := w.svc.RunPreview(ctx, w.org, w.on, nil)
	if err != nil || !p.Enrols || !slices.Equal(ids(p.Hosts), []uuid.UUID{w.h2, w.h3}) || p.Hosts[0].Name != "web-2" {
		t.Fatalf("preview = %+v, %v; want web-2, web-3", p, err)
	}
	if p, err := w.svc.RunPreview(ctx, w.org, w.on, &w.h3); err != nil || !slices.Equal(ids(p.Hosts), []uuid.UUID{w.h3}) {
		t.Fatalf("one host = %+v, %v; want web-3", p, err)
	}
	if p, err := w.svc.RunPreview(ctx, w.org, w.on, &w.h1); err != nil || !p.Enrols || p.Hosts == nil || len(p.Hosts) != 0 {
		t.Fatalf("one host with an agent = %+v, %v; want enrols, none", p, err)
	}
	if p, err := w.svc.RunPreview(ctx, w.org, w.off, nil); err != nil || p.Enrols || len(p.Hosts) != 0 {
		t.Fatalf("without the flag = %+v, %v; want nothing", p, err)
	}
	if _, err := w.svc.RunPreview(ctx, w.org, id(), nil); !errors.Is(err, agentenroll.ErrNotFound) {
		t.Fatalf("unknown template = %v, want ErrNotFound", err)
	}
}

// A retry previews its failed hosts without an agent.
func TestRetryPreview(t *testing.T) {
	w := newWorld()
	p, err := w.svc.RetryPreview(ctx, w.org, w.jb)
	if err != nil || !p.Enrols || !slices.Equal(ids(p.Hosts), []uuid.UUID{w.h3}) {
		t.Fatalf("retry preview = %+v, %v; want web-3", p, err)
	}
	w.targets.retryErr = jobs.ErrNothingToRetry
	if _, err := w.svc.RetryPreview(ctx, w.org, w.jb); !errors.Is(err, jobs.ErrNothingToRetry) {
		t.Fatalf("a job with nothing to retry = %v", err)
	}
}

// ConfirmRun (spec 2026-11-25 B, controller ruling C): a run of a
// template with agent enrollment starts only with the list its person
// confirmed, which must be the targets without an agent now (any order),
// and only for a person who may enrol agents; a template without the flag
// takes no list.
func TestConfirmRun(t *testing.T) {
	w := newWorld()
	list := func(us ...uuid.UUID) *[]uuid.UUID { return &us }

	got, err := w.svc.ConfirmRun(ctx, w.org, w.on, nil, list(w.h3, w.h2), true)
	if err != nil || !slices.Equal(got, []uuid.UUID{w.h2, w.h3}) {
		t.Fatalf("confirmed = %v, %v; want web-2, web-3", got, err)
	}
	if _, err := w.svc.ConfirmRun(ctx, w.org, w.on, nil, list(w.h3, w.h2), false); !errors.Is(err, agentenroll.ErrNotAllowed) {
		t.Fatalf("without leave = %v, want ErrNotAllowed", err)
	}
	var ce *agentenroll.ConfirmError
	if _, err := w.svc.ConfirmRun(ctx, w.org, w.on, nil, nil, true); !errors.As(err, &ce) || !ce.Missing || !slices.Equal(ids(ce.Current), []uuid.UUID{w.h2, w.h3}) {
		t.Fatalf("without a list = %v, want a ConfirmError asking for web-2, web-3", err)
	}
	for name, l := range map[string]*[]uuid.UUID{"a host short": list(w.h2), "an extra host": list(w.h1, w.h2, w.h3), "empty": list()} {
		if _, err := w.svc.ConfirmRun(ctx, w.org, w.on, nil, l, true); !errors.As(err, &ce) || ce.Missing || len(ce.Current) != 2 {
			t.Errorf("%s = %v, want a ConfirmError with the current list", name, err)
		}
	}
	// Every target with an agent: the confirmed empty list is kept as such.
	got, err = w.svc.ConfirmRun(ctx, w.org, w.on, &w.h1, list(), true)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("an empty confirmed list = %#v, %v; want an empty list", got, err)
	}

	if got, err := w.svc.ConfirmRun(ctx, w.org, w.off, nil, nil, false); err != nil || got != nil {
		t.Fatalf("without the flag = %v, %v; want no list", got, err)
	}
	if _, err := w.svc.ConfirmRun(ctx, w.org, w.off, nil, list(), true); !errors.Is(err, agentenroll.ErrNotEnrolling) {
		t.Fatalf("a list for a template without the flag = %v, want ErrNotEnrolling", err)
	}
	if got, err := w.svc.ConfirmRun(ctx, w.org, id(), nil, nil, false); err != nil || got != nil {
		t.Fatalf("an unknown template = %v, %v; want nothing (the run answers for it)", got, err)
	}
}

// ConfirmRetry is ConfirmRun for the failed hosts a retry runs.
func TestConfirmRetry(t *testing.T) {
	w := newWorld()
	got, err := w.svc.ConfirmRetry(ctx, w.org, w.jb, &[]uuid.UUID{w.h3}, true)
	if err != nil || !slices.Equal(got, []uuid.UUID{w.h3}) {
		t.Fatalf("confirmed = %v, %v", got, err)
	}
	var ce *agentenroll.ConfirmError
	if _, err := w.svc.ConfirmRetry(ctx, w.org, w.jb, nil, true); !errors.As(err, &ce) || !ce.Missing {
		t.Fatalf("without a list = %v", err)
	}
	if _, err := w.svc.ConfirmRetry(ctx, w.org, w.jb, &[]uuid.UUID{w.h3}, false); !errors.Is(err, agentenroll.ErrNotAllowed) {
		t.Fatalf("without leave = %v", err)
	}
	w.targets.retry = w.off
	if got, err := w.svc.ConfirmRetry(ctx, w.org, w.jb, nil, false); err != nil || got != nil {
		t.Fatalf("a retry of a template without the flag = %v, %v", got, err)
	}
	w.targets.retryErr = jobs.ErrNothingToRetry
	if got, err := w.svc.ConfirmRetry(ctx, w.org, w.jb, nil, false); err != nil || got != nil {
		t.Fatalf("a retry that cannot run = %v, %v; want nothing (the retry answers for it)", got, err)
	}
}
