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
		{name: "limit of names and globs", ov: limitOf("web-1:web-*:!web-3")},
		{name: "limit reading a file", ov: limitOf("@/etc/passwd"), is: runrequests.ErrInvalidInput, msg: "limit"},
		{name: "limit as a regex", ov: limitOf("~.*"), is: runrequests.ErrInvalidInput, msg: "limit"},
		{name: "limit with a subscript", ov: limitOf("web[0]"), is: runrequests.ErrInvalidInput, msg: "limit"},
		{name: "limit with an intersection", ov: limitOf("web-*:&db"), is: runrequests.ErrInvalidInput, msg: "limit"},
		{name: "limit with a ? glob", ov: limitOf("web-?"), is: runrequests.ErrInvalidInput, msg: "only host names and * wildcards"},
		{name: "limit matching no target", ov: limitOf("db-*"), is: runrequests.ErrInvalidInput, msg: "no host"},
		{name: "hosts and a limit with nothing in common", ov: func(w *world) runrequests.Overrides {
			l := "web-1"
			return runrequests.Overrides{HostIDs: []uuid.UUID{w.h2}, Limit: &l}
		}, is: runrequests.ErrInvalidInput, msg: "no host"},
		{name: "reserved ansible key", ov: vars(map[string]any{"version": map[string]any{"__ansible_vault": "$ANSIBLE_VAULT;1.1"}}),
			is: runrequests.ErrInvalidInput, msg: "version"},
		{name: "too deep", ov: vars(map[string]any{"version": deep(40)}), is: runrequests.ErrInvalidInput, msg: "version"},
		{name: "secret marker as a value", ov: vars(map[string]any{"version": map[string]any{"$secret": "keep"}}),
			is: runrequests.ErrInvalidInput, msg: "version"},
		{name: "interpreter on a template without a schema", ov: vars(map[string]any{"ansible_python_interpreter": "sh -c id #"}),
			prep: noSchema, is: runrequests.ErrInvalidInput, msg: "ansible_python_interpreter"},
		{name: "become executable, other case", ov: vars(map[string]any{"Ansible_Become_Exe": "x"}),
			prep: noSchema, is: runrequests.ErrInvalidInput, msg: "Ansible_Become_Exe"},
		{name: "magic variable", ov: vars(map[string]any{"hostvars": "x"}), prep: noSchema, is: runrequests.ErrInvalidInput, msg: "hostvars"},
		{name: "playbook keyword", ov: vars(map[string]any{"environment": map[string]any{"PATH": "/tmp"}}), prep: noSchema,
			is: runrequests.ErrInvalidInput, msg: "environment"},
		{name: "pulsix name", ov: vars(map[string]any{"pulsix_agent_enroll_token": "x"}), prep: noSchema,
			is: runrequests.ErrInvalidInput, msg: "pulsix_agent_enroll_token"},
		{name: "template with a malformed marker", ov: vars(map[string]any{"release": "1"}),
			prep: func(w *world) {
				noSchema(w)
				tmpl := w.tmpls.fakeTemplates[w.x]
				tmpl.ExtraVars = map[string]any{"cfg": map[string]any{"__ansible_unsafe": float64(1)}}
				w.tmpls.fakeTemplates[w.x] = tmpl
			},
			is: runrequests.ErrInvalidInput, msg: "template extra var cfg"},
		{name: "sealed marker in a list", ov: vars(map[string]any{"version": []any{map[string]any{"$sealed": "x"}}}),
			is: runrequests.ErrInvalidInput, msg: "version"},
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
			// A refusal never echoes the value it refused.
			for _, v := range []string{"ANSIBLE_VAULT", "/etc/passwd", "keep"} {
				if strings.Contains(err.Error(), v) && v != tc.msg {
					t.Fatalf("validate = %v echoes %q", err, v)
				}
			}
		})
	}

	// The template validation is asked with secrets refused and the
	// limit held to the webhook rules.
	w := newWorld()
	if _, err := w.resolver().ValidateRun(ctx, org, w.x, nil, &runrequests.Overrides{ExtraVars: map[string]any{"version": "1"}}); err != nil {
		t.Fatal(err)
	}
	if len(w.ovr.opts) != 1 || !w.ovr.opts[0].RefuseSecrets || !w.ovr.opts[0].StrictLimit {
		t.Fatalf("template validation options = %+v", w.ovr.opts)
	}
}

// noSchema takes the schema off the world's template X.
func noSchema(w *world) {
	tmpl := w.tmpls.fakeTemplates[w.x]
	tmpl.ExtraVarsSchema = nil
	w.tmpls.fakeTemplates[w.x] = tmpl
}

// deep is a value nested n lists deep.
func deep(n int) any {
	var v any = "x"
	for range n {
		v = []any{v}
	}
	return v
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
	if want := []string{w.h3.String(), w.h1.String()}; !slices.Equal(p.RequestedHostIDs, want) {
		t.Fatalf("requested hosts = %v, want %v", p.RequestedHostIDs, want)
	}
	// The targets are the template's narrowed to the requested hosts and
	// to the request's limit: web-3 is requested, but the limit drops it.
	if got := targetIDs(p); !slices.Equal(got, []string{w.h1.String()}) {
		t.Fatalf("targets = %v, want h1", got)
	}
	// A dry run starts no follow-ups, so the picture offers none.
	if p.FollowUps != nil {
		t.Fatalf("follow-ups of a dry run = %+v", p.FollowUps)
	}

	// A limit on its own narrows the targets too.
	p = w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x, Overrides: &runrequests.Overrides{Limit: strPtr("web-*:!web-2")}})
	if got := targetIDs(p); !slices.Equal(got, []string{w.h1.String(), w.h3.String()}) {
		t.Fatalf("targets under a limit = %v, want h1 and h3", got)
	}

	// The one host of host_id is held to the limit too.
	if _, err := w.resolver().ValidateRun(context.Background(), id(), w.x, &w.h2, &runrequests.Overrides{Limit: strPtr("web-1")}); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("host_id outside the limit = %v, want ErrInvalidInput", err)
	}
	p = w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x, HostID: &w.h2, Overrides: &runrequests.Overrides{Limit: strPtr("web-1")}})
	if got := targetIDs(p); len(got) != 0 {
		t.Fatalf("targets of host_id outside the limit = %v, want none", got)
	}

	// Without overrides nothing of it shows.
	p = w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x})
	if p.CheckMode || p.Diff || p.LimitOverride != "" || p.ExtraVarsOverride != nil || p.RequestedHostIDs != nil || p.FollowUps == nil {
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
		Commits: w.commits, SyncJobs: w.syncs,
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
	// The vars travel untrusted (inert at the claim); the request's limit
	// never reaches Ansible: it is resolved into the hosts the run keeps
	// to, and the template's own limit applies on top as for any run.
	if ov == nil || !ov.Check || !ov.Diff || ov.ExtraVars["version"] != "1.2" || !ov.Untrusted || ov.Limit != nil {
		t.Fatalf("run overrides = %+v", ov)
	}
	if !slices.Equal(got.OnlyHosts, []uuid.UUID{w.h1}) || got.OnlyHost != nil {
		t.Fatalf("only hosts = %v (only host %v), want h1", got.OnlyHosts, got.OnlyHost)
	}
	// A dry run carries no follow-up list: the enqueue refuses one.
	if got.FollowUps != nil {
		t.Fatalf("follow-ups of a dry run = %v", *got.FollowUps)
	}
	// The overrides are checked again at the approval.
	if len(w.ovr.calls) != 1 {
		t.Fatalf("template validation calls = %d, want 1", len(w.ovr.calls))
	}

	// One host and a dry run: the host becomes the run's host list.
	rec.enqueued = nil
	one := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x, HostID: &w.h2, Overrides: &runrequests.Overrides{Check: true}}
	if err := res.Run(ctx, org, one, actor, "k1", group, nil); err != nil {
		t.Fatal(err)
	}
	if got := rec.enqueued; got.OnlyHost != nil || !slices.Equal(got.OnlyHosts, []uuid.UUID{w.h2}) || got.Overrides.Untrusted {
		t.Fatalf("one-host dry run = %+v", got)
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

func targetIDs(p runrequests.Picture) []string {
	var out []string
	for _, h := range p.Targets {
		out = append(out, h.ID)
	}
	return out
}
