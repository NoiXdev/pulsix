package runrequests_test

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/templates"
)

// scopedRunner answers LimitedRunTargets per template.
type scopedRunner struct {
	fakeJobRunner
	scope map[uuid.UUID][]uuid.UUID
}

func (s *scopedRunner) LimitedRunTargets(_ context.Context, _, templateID uuid.UUID) ([]uuid.UUID, error) {
	return s.scope[templateID], nil
}

// hostVars renders each asked host's vars into the document and returns
// each template's extra vars, so the digest follows exactly the hosts and
// the template it is asked about.
type hostVars struct {
	byHost map[uuid.UUID]map[string]any
	extra  map[uuid.UUID]map[string]any
	// renders and extras count the reads.
	renders, extras int
}

func (v *hostVars) InventoryVars(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (render.Inventory, error) {
	v.renders++
	hv := map[string]any{}
	for _, id := range ids {
		hv[id.String()] = v.byHost[id]
	}
	return render.Inventory{Doc: map[string]any{"_meta": map[string]any{"hostvars": hv}}}, nil
}

func (v *hostVars) ExtraVars(_ context.Context, _, templateID uuid.UUID) (map[string]any, error) {
	v.extras++
	return v.extra[templateID], nil
}

// bindingFx is a run of X on h1 and h2 whose follow-up Y targets h2 and
// h3, and Y's follow-up Z targets every host.
type bindingFx struct {
	org, x, y, z, h1, h2, h3 uuid.UUID
	vars                     *hostVars
	tmpls                    fakeTemplates
	runner                   *scopedRunner
	start                    bool
}

func newBindingFx() *bindingFx {
	f := &bindingFx{org: id(), x: id(), y: id(), z: id(), h1: id(), h2: id(), h3: id(), start: true}
	f.tmpls = fakeTemplates{
		f.x: {ID: f.x, Name: "X", FollowUps: []templates.FollowUp{{TemplateID: f.y, Name: "Y"}}},
		f.y: {ID: f.y, Name: "Y", Revision: 2, ExtraVars: map[string]any{"port": 8080}, FollowUps: []templates.FollowUp{{TemplateID: f.z, Name: "Z"}}},
		f.z: {ID: f.z, Name: "Z", Revision: 5},
	}
	f.runner = &scopedRunner{scope: map[uuid.UUID][]uuid.UUID{
		f.x: {f.h1, f.h2}, f.y: {f.h3, f.h2}, f.z: {f.h1, f.h2, f.h3},
	}}
	f.vars = &hostVars{byHost: map[uuid.UUID]map[string]any{
		f.h1: {"role": "db"}, f.h2: {"role": "web"}, f.h3: {"role": "cache"},
	}, extra: map[uuid.UUID]map[string]any{f.y: {"port": 8080}}}
	return f
}

func (f *bindingFx) resolver() *runrequests.Resolver {
	var calls []string
	f.runner.calls = &calls
	return runrequests.NewResolver(runrequests.Sources{
		Templates: f.tmpls, Jobs: f.runner, Credentials: fakeCreds{}, Vars: f.vars, VarsKey: testVarsKey,
		Settings: fakeSettings{f.start}, FollowUpRuns: fakeFollowUpRuns{calls: &calls},
		Hosts: hostList{hs: []hosts.Host{{ID: f.h1, Name: "db-1"}, {ID: f.h2, Name: "web-1"}, {ID: f.h3, Name: "cache-1"}}}, SSH: sshMap{},
		SyncJobs: &fakeSyncJobs{pins: map[uuid.UUID]string{}},
	})
}

func (f *bindingFx) picture(t *testing.T, req runrequests.Request) runrequests.Picture {
	t.Helper()
	p, err := f.resolver().Picture(context.Background(), f.org, req)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func hostIDStrings(hs []runrequests.PictureHost) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.ID
	}
	return out
}

// TestPictureBindsFollowUpsToTheirParentsHosts is backlog 2.143: a
// follow-up the approval would start shows the hosts it can run on (its
// template's targets among its parent's), its extra vars masked and the
// digest of the values it receives; a deeper follow-up is bound within
// its own parent's hosts. A value of a host outside a follow-up's hosts
// does not change its binding.
func TestPictureBindsFollowUpsToTheirParentsHosts(t *testing.T) {
	f := newBindingFx()
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: f.x, Status: runrequests.StatusPending}
	p := f.picture(t, req)
	if p.FollowUps == nil || len(p.FollowUps.Templates) != 1 {
		t.Fatalf("follow-ups = %+v", p.FollowUps)
	}
	y := p.FollowUps.Templates[0]
	if !slices.Equal(hostIDStrings(y.Targets), []string{f.h2.String()}) || y.Targets[0].Name != "web-1" || y.VarsDigest == "" ||
		y.ExtraVars["port"] != 8080 {
		t.Fatalf("Y = %+v, want bound to web-1 with its extra vars", y)
	}
	if len(y.FollowUps) != 1 {
		t.Fatalf("Y's follow-ups = %+v", y.FollowUps)
	}
	if z := y.FollowUps[0]; !slices.Equal(hostIDStrings(z.Targets), []string{f.h2.String()}) || z.VarsDigest == "" {
		t.Fatalf("Z = %+v, want bound within Y's hosts", z)
	}

	f.vars.byHost[f.h1] = map[string]any{"role": "primary"}
	again := f.picture(t, req).FollowUps.Templates[0]
	if again.VarsDigest != y.VarsDigest {
		t.Fatal("a value of a host outside Y's hosts changed Y's binding")
	}
	f.vars.byHost[f.h2] = map[string]any{"role": "edge"}
	changed := f.picture(t, req).FollowUps.Templates[0]
	if changed.VarsDigest == y.VarsDigest ||
		runrequests.FollowUpFingerprint(req.ID, changed) == runrequests.FollowUpFingerprint(req.ID, y) {
		t.Fatal("a value of Y's host did not change Y's binding")
	}
}

// TestBindFollowUpMatchesThePicture: the claim's binding of a follow-up,
// computed from the parent's hosts the approval stored, is the picture's.
func TestBindFollowUpMatchesThePicture(t *testing.T) {
	f := newBindingFx()
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: f.x, Status: runrequests.StatusPending}
	y := f.picture(t, req).FollowUps.Templates[0]
	got, err := f.resolver().BindFollowUp(context.Background(), f.org, req.ID, f.y, []uuid.UUID{f.h2, f.h1})
	if err != nil {
		t.Fatal(err)
	}
	if runrequests.FollowUpFingerprint(req.ID, got) != runrequests.FollowUpFingerprint(req.ID, y) {
		t.Fatalf("claim binding %+v differs from the picture's %+v", got, y)
	}
}

// TestPictureOfALegacyApprovalBindsNoFollowUps: a request approved before
// follow-ups were bound (no stored bindings) keeps its picture, and so its
// fingerprint; one approved by this version, a pending one, and none with
// the setting off.
func TestPictureOfALegacyApprovalBindsNoFollowUps(t *testing.T) {
	f := newBindingFx()
	legacy := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: f.x, Status: runrequests.StatusApproved}
	if y := f.picture(t, legacy).FollowUps.Templates[0]; y.VarsDigest != "" || y.Targets != nil || y.ExtraVars != nil {
		t.Fatalf("legacy approval: Y = %+v, want unbound", y)
	}
	bound := legacy
	bound.FollowUpBindings = &runrequests.FollowUpBindings{Nodes: map[string]runrequests.FollowUpBinding{}}
	if y := f.picture(t, bound).FollowUps.Templates[0]; y.VarsDigest == "" {
		t.Fatalf("approval of this version: Y = %+v, want bound", y)
	}
	f.start = false
	pending := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: f.x, Status: runrequests.StatusPending}
	if y := f.picture(t, pending).FollowUps.Templates[0]; y.VarsDigest != "" {
		t.Fatalf("setting off: Y = %+v, want unbound", y)
	}
}

var _ runrequests.JobRunner = (*scopedRunner)(nil)

// TestPictureRendersEachHostSetOnce is review M1: within one picture the
// follow-ups bound to the same hosts share one render of their inventory
// (Y and Z are both bound to web-1), and each template's extra vars are
// read once; the bindings are the same as computed one by one at a claim.
func TestPictureRendersEachHostSetOnce(t *testing.T) {
	f := newBindingFx()
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: f.x, Status: runrequests.StatusPending}
	p := f.picture(t, req)
	if f.vars.renders != 2 || f.vars.extras != 3 {
		t.Fatalf("renders %d, extra var reads %d; want 2 (the run's hosts, web-1) and 3 (X, Y, Z)", f.vars.renders, f.vars.extras)
	}
	z := p.FollowUps.Templates[0].FollowUps[0]
	got, err := f.resolver().BindFollowUp(context.Background(), f.org, req.ID, f.z, []uuid.UUID{f.h2})
	if err != nil {
		t.Fatal(err)
	}
	if runrequests.FollowUpFingerprint(req.ID, got) != runrequests.FollowUpFingerprint(req.ID, z) {
		t.Fatalf("Z at its claim %+v differs from the picture's %+v", got, z)
	}
}
