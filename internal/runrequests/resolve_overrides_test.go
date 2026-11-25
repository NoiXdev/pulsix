package runrequests_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/templates"
)

// overrideChecker stands in for templates.Service.ValidateOverrides: it
// records what it was asked and fails with err.
type overrideChecker struct {
	calls []templates.Overrides
	opts  []templates.OverrideOptions
	err   error
}

func (c *overrideChecker) ValidateOverrides(_ context.Context, _, _ uuid.UUID, in templates.Overrides, opt templates.OverrideOptions) (templates.Overrides, error) {
	c.calls, c.opts = append(c.calls, in), append(c.opts, opt)
	return in, c.err
}

// enqueueRecorder is targetRunner that keeps the last enqueue.
type enqueueRecorder struct {
	*targetRunner
	enqueued *jobs.RunRequest
}

func (e *enqueueRecorder) EnqueueRunWith(_ context.Context, _, _ uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error) {
	e.enqueued = &req
	return nil, nil
}

func strPtr(s string) *string { return &s }

func TestValidateRunChecksTheOverrides(t *testing.T) {
	ctx, org := context.Background(), id()
	secretSchema := &schema.Schema{Fields: []schema.Field{
		{Name: "version", Type: schema.TypeString},
		{Name: "api_token", Type: schema.TypeSecret},
		{Name: "db", Type: schema.TypeObject, Fields: []schema.Field{{Name: "password", Type: schema.TypeSecret}}},
	}}
	for _, tc := range []struct {
		name string
		ov   func(w *world) runrequests.Overrides
		prep func(w *world)
		is   error
		msg  string
	}{
		{name: "plain vars and a host", ov: func(w *world) runrequests.Overrides {
			return runrequests.Overrides{ExtraVars: map[string]any{"version": "1.2"}, HostIDs: []uuid.UUID{w.h2}}
		}},
		{name: "secret var", ov: vars(map[string]any{"version": "1.2", "api_token": "x"}), is: runrequests.ErrInvalidInput, msg: "api_token"},
		{name: "var holding a secret", ov: vars(map[string]any{"db": map[string]any{"password": "x"}}), is: runrequests.ErrInvalidInput, msg: "db"},
		{name: "host outside the targets", ov: func(w *world) runrequests.Overrides {
			return runrequests.Overrides{HostIDs: []uuid.UUID{w.h2, foreignHost}}
		}, is: runrequests.ErrInvalidInput, msg: foreignHost.String()},
		{name: "template validation", ov: vars(map[string]any{"version": 3}),
			prep: func(w *world) {
				w.ovr.err = &schema.ValidationError{Problems: []schema.Problem{{Path: "version", Message: "must be a string"}}}
			},
			is: runrequests.ErrInvalidInput, msg: "must be a string"},
		{name: "limit under the template's", ov: limitOf("web-1")},
		{name: "limit of several terms under the template's", ov: limitOf("web-1:web-2"), is: runrequests.ErrInvalidInput, msg: "limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			tmpl := w.tmpls.fakeTemplates[w.x]
			tmpl.ExtraVarsSchema = secretSchema
			w.tmpls.fakeTemplates[w.x] = tmpl
			if tc.prep != nil {
				tc.prep(w)
			}
			ov := tc.ov(w)
			_, err := w.resolver().ValidateRun(ctx, org, w.x, nil, &ov)
			if tc.is == nil {
				if err != nil {
					t.Fatalf("validate = %v", err)
				}
				return
			}
			if !errors.Is(err, tc.is) || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("validate = %v, want %v naming %q", err, tc.is, tc.msg)
			}
		})
	}

	// The template validation is asked with secrets refused.
	w := newWorld()
	if _, err := w.resolver().ValidateRun(ctx, org, w.x, nil, &runrequests.Overrides{ExtraVars: map[string]any{"version": "1"}}); err != nil {
		t.Fatal(err)
	}
	if len(w.ovr.opts) != 1 || !w.ovr.opts[0].RefuseSecrets {
		t.Fatalf("template validation options = %+v", w.ovr.opts)
	}
}

// foreignHost is no host of any world.
var foreignHost = uuid.MustParse("0192f000-0000-7000-8000-0000000000ff")

func vars(v map[string]any) func(*world) runrequests.Overrides {
	return func(*world) runrequests.Overrides { return runrequests.Overrides{ExtraVars: v} }
}

func limitOf(l string) func(*world) runrequests.Overrides {
	return func(*world) runrequests.Overrides { return runrequests.Overrides{Limit: &l} }
}

func TestPictureShowsTheOverrides(t *testing.T) {
	w := newWorld()
	w.settings.on = true
	req := runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x, Overrides: &runrequests.Overrides{
		Check: true, Diff: true, ExtraVars: map[string]any{"version": "1.2"}, HostIDs: []uuid.UUID{w.h3, w.h1}, Limit: strPtr("web-1"),
	}}
	p := w.picture(t, req)
	if !p.CheckMode || !p.Diff || p.LimitOverride != "web-1" || p.ExtraVarsOverride["version"] != "1.2" || p.Limit != "web" {
		t.Fatalf("overrides in the picture = %+v", p)
	}
	if want := []string{w.h3.String(), w.h1.String()}; !slices.Equal(p.Hosts, want) {
		t.Fatalf("hosts = %v, want %v", p.Hosts, want)
	}
	// The targets are the template's narrowed to the requested hosts.
	var targets []string
	for _, h := range p.Targets {
		targets = append(targets, h.ID)
	}
	if !slices.Equal(targets, []string{w.h1.String(), w.h3.String()}) {
		t.Fatalf("targets = %v, want h1 and h3", targets)
	}
	// A dry run starts no follow-ups, so the picture offers none.
	if p.FollowUps != nil {
		t.Fatalf("follow-ups of a dry run = %+v", p.FollowUps)
	}

	// Without overrides nothing of it shows.
	p = w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x})
	if p.CheckMode || p.Diff || p.LimitOverride != "" || p.ExtraVarsOverride != nil || p.Hosts != nil || p.FollowUps == nil {
		t.Fatalf("picture without overrides = %+v", p)
	}
}

func TestRunEnqueuesTheOverrides(t *testing.T) {
	ctx, org, actor, group := context.Background(), id(), id(), id()
	w := newWorld()
	rec := &enqueueRecorder{targetRunner: w.jobs}
	res := runrequests.NewResolver(runrequests.Sources{
		Templates: w.tmpls, Playbooks: w.pbs, Repos: w.repos, Jobs: rec, Hosts: w.hosts, SSH: w.ssh,
		Credentials: w.creds, Vars: w.inv, VarsKey: w.key, Settings: w.settings, FollowUpRuns: w.rows, Overrides: w.ovr,
	})
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x, Overrides: &runrequests.Overrides{
		Check: true, Diff: true, ExtraVars: map[string]any{"version": "1.2"}, HostIDs: []uuid.UUID{w.h3, w.h1}, Limit: strPtr("web-1"),
	}}
	if err := res.Run(ctx, org, req, actor, "k", group, []uuid.UUID{}); err != nil {
		t.Fatal(err)
	}
	got := rec.enqueued
	if got == nil || got.Trigger != jobs.TriggerManual || got.RunGroupID != group || got.IdempotencyKey != "k" {
		t.Fatalf("enqueued = %+v", got)
	}
	ov := got.Overrides
	// The template's limit "web" is intersected with the request's, so the
	// override only narrows.
	if ov == nil || !ov.Check || !ov.Diff || ov.ExtraVars["version"] != "1.2" || ov.Limit == nil || *ov.Limit != "web:&web-1" {
		t.Fatalf("run overrides = %+v", ov)
	}
	if !slices.Equal(got.OnlyHosts, []uuid.UUID{w.h1, w.h3}) {
		t.Fatalf("only hosts = %v, want h1 and h3", got.OnlyHosts)
	}
	// A dry run carries no follow-up list: the enqueue refuses one.
	if got.FollowUps != nil {
		t.Fatalf("follow-ups of a dry run = %v", *got.FollowUps)
	}
	// The overrides are checked again at the approval.
	if len(w.ovr.calls) != 1 {
		t.Fatalf("template validation calls = %d, want 1", len(w.ovr.calls))
	}

	// A request without overrides enqueues exactly as before.
	rec.enqueued = nil
	if err := res.Run(ctx, org, runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x, HostID: &w.h2}, actor, "k2", group, nil); err != nil {
		t.Fatal(err)
	}
	if got := rec.enqueued; got.Overrides != nil || got.OnlyHosts != nil || got.OnlyHost == nil || *got.OnlyHost != w.h2 || got.FollowUps == nil {
		t.Fatalf("plain enqueue = %+v", got)
	}

	// A secret key that became one after the request is refused at approval.
	tmpl := w.tmpls.fakeTemplates[w.x]
	tmpl.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "version", Type: schema.TypeSecret}}}
	w.tmpls.fakeTemplates[w.x] = tmpl
	rec.enqueued = nil
	if err := res.Run(ctx, org, req, actor, "k3", group, nil); !errors.Is(err, runrequests.ErrInvalidInput) || rec.enqueued != nil {
		t.Fatalf("run with a now-secret var = %v (enqueued %v)", err, rec.enqueued)
	}
}

func TestRunLimit(t *testing.T) {
	for _, tc := range []struct {
		tmpl, override, want string
		bad                  bool
	}{
		{"", "web-1:web-2", "web-1:web-2", false},
		{"web", "web-1", "web:&web-1", false},
		{"web", "!web-1", "web:!web-1", false},
		{"web", "&db", "web:&db", false},
		{"web,db", "web-1", "web,db,&web-1", false},
		{"web", "web-1:web-2", "", true},
		{"web", "web-1,web-2", "", true},
	} {
		got, err := runrequests.RunLimit(tc.tmpl, tc.override)
		if tc.bad {
			if !errors.Is(err, runrequests.ErrInvalidInput) {
				t.Errorf("RunLimit(%q, %q) = %q %v, want ErrInvalidInput", tc.tmpl, tc.override, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("RunLimit(%q, %q) = %q %v, want %q", tc.tmpl, tc.override, got, err, tc.want)
		}
	}
}
