package runrequests_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/triggers"
)

// fakeTriggerSource is the trigger service as the resolver sees it: stored
// triggers, the inputs it validated (filling in the defaults the service
// fills in) and what it applied.
type fakeTriggerSource struct {
	byID        map[uuid.UUID]triggers.Trigger
	validateErr error
	applyErr    error
	validated   []triggers.Input
	created     []triggers.Input
	createdAs   []uuid.UUID // id, actor
	updated     []triggers.Input
	updatedRev  []int64
	rotatedRev  []int64
	enabled     []uuid.UUID
	enabledRev  []int64
	accepted    []uuid.UUID
	acceptedRev []int64
}

func (f *fakeTriggerSource) Get(_ context.Context, _, id uuid.UUID) (triggers.Trigger, error) {
	t, ok := f.byID[id]
	if !ok {
		return triggers.Trigger{}, triggers.ErrNotFound
	}
	return t, nil
}

func (f *fakeTriggerSource) Validate(_ context.Context, _, id uuid.UUID, in triggers.Input) (triggers.Trigger, error) {
	f.validated = append(f.validated, in)
	if f.validateErr != nil {
		return triggers.Trigger{}, f.validateErr
	}
	t := triggers.Trigger{ID: id, Kind: in.Kind, TemplateID: in.TemplateID, RepoID: in.RepoID, Name: in.Name, Enabled: in.Enabled,
		Preset: in.Preset, TimestampHeader: in.TimestampHeader, Mapping: in.Mapping, OnActive: in.OnActive, RateLimit: in.RateLimit,
		SettleSeconds: in.SettleSeconds, MaxWaitSeconds: in.MaxWaitSeconds}
	if t.OnActive == "" {
		t.OnActive = triggers.OnActiveQueue
	}
	if t.RateLimit == (triggers.RateLimit{}) {
		t.RateLimit = triggers.DefaultRateLimit()
	}
	for _, c := range in.AllowCIDRs {
		t.AllowCIDRs = append(t.AllowCIDRs, netip.MustParsePrefix(c))
	}
	return t, nil
}

func (f *fakeTriggerSource) CreateAs(_ context.Context, _, id, actor uuid.UUID, in triggers.Input) (triggers.Created, error) {
	if f.applyErr != nil {
		return triggers.Created{}, f.applyErr
	}
	f.created, f.createdAs = append(f.created, in), append(f.createdAs, id, actor)
	return triggers.Created{Trigger: triggers.Trigger{ID: id}, Secret: "new-secret"}, nil
}

func (f *fakeTriggerSource) Update(_ context.Context, _, id uuid.UUID, revision int64, in triggers.Input) (triggers.Trigger, error) {
	if f.applyErr != nil {
		return triggers.Trigger{}, f.applyErr
	}
	f.updated, f.updatedRev = append(f.updated, in), append(f.updatedRev, revision)
	return triggers.Trigger{ID: id}, nil
}

func (f *fakeTriggerSource) RotateSecretAt(_ context.Context, _, id uuid.UUID, revision int64) (triggers.Created, error) {
	if f.applyErr != nil {
		return triggers.Created{}, f.applyErr
	}
	f.rotatedRev = append(f.rotatedRev, revision)
	return triggers.Created{Trigger: triggers.Trigger{ID: id}, Secret: "rotated-secret"}, nil
}

func (f *fakeTriggerSource) URL(t triggers.Trigger) string {
	if t.Kind == triggers.KindRepoSync {
		return "https://px.example.test/api/v1/hooks/repos/" + t.ID.String()
	}
	return "https://px.example.test/api/v1/hooks/jobs/" + t.ID.String()
}

// triggerWorld is the resolver's world with a stored template run trigger
// of template X (disabled, revision 4) and a repository sync trigger of
// the world's repository.
type triggerWorld struct {
	*world
	run, sync uuid.UUID
	trigs     *fakeTriggerSource
}

func newTriggerWorld() *triggerWorld {
	w := &triggerWorld{world: newWorld(), run: id(), sync: id()}
	w.trigs = &fakeTriggerSource{byID: map[uuid.UUID]triggers.Trigger{
		w.run: {ID: w.run, Kind: triggers.KindTemplateRun, TemplateID: &w.x, Name: "deploy hook", Enabled: false, Preset: triggers.PresetGitHub,
			AllowCIDRs: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, Mapping: triggers.Mapping{ExtraVars: map[string]string{"tag": "$.ref"}},
			OnActive: triggers.OnActiveQueue, RateLimit: triggers.RateLimit{Count: 5, Per: "1m"}, Revision: 4},
		w.sync: {ID: w.sync, Kind: triggers.KindRepoSync, RepoID: &w.repo, Name: "push", Enabled: true, Preset: triggers.PresetGitea,
			OnActive: triggers.OnActiveSkip, RateLimit: triggers.DefaultRateLimit(), Revision: 2},
	}}
	return w
}

func (w *triggerWorld) resolver() *runrequests.Resolver {
	return runrequests.NewResolver(runrequests.Sources{
		Templates: w.tmpls, Playbooks: w.pbs, Repos: w.repos, Jobs: w.jobs, Hosts: w.hosts, SSH: w.ssh,
		Credentials: w.creds, Vars: w.inv, VarsKey: w.key, Settings: w.settings, FollowUpRuns: w.rows, Overrides: w.ovr,
		Commits: w.commits, SyncJobs: w.syncs, RepoSyncs: w.repoSyncs,
		InventorySecrets: w.invSecrets, PlaybookVars: w.pbVars, Agents: w.agents, Triggers: w.trigs,
	})
}

func (w *triggerWorld) runSpec() *runrequests.TriggerSpec {
	return &runrequests.TriggerSpec{Kind: triggers.KindTemplateRun, TemplateID: &w.x, Name: "release hook", Enabled: true, Preset: triggers.PresetPulsix,
		AllowCIDRs: []string{"198.51.100.7/32"}, Mapping: triggers.Mapping{ExtraVars: map[string]string{"version": "$.release.tag"}, Limit: "$.hosts"}}
}

// A create is checked by the trigger service and stored as the service
// would store it (its defaults filled in); a template run trigger's
// request names the template and its revision, a repository sync
// trigger's none.
func TestValidateTriggerCreate(t *testing.T) {
	w := newTriggerWorld()
	ch, tmpl, rev, err := w.resolver().ValidateTrigger(context.Background(), id(), id(), runrequests.TriggerInput{Action: runrequests.TriggerCreate, Spec: w.runSpec()})
	if err != nil {
		t.Fatal(err)
	}
	if tmpl != w.x || rev != 3 || ch.Action != runrequests.TriggerCreate || ch.Kind != triggers.KindTemplateRun {
		t.Fatalf("change = %+v, template %v rev %d", ch, tmpl, rev)
	}
	s := ch.Spec
	if s.Name != "release hook" || s.OnActive != triggers.OnActiveQueue || s.RateLimit != triggers.DefaultRateLimit() ||
		len(s.AllowCIDRs) != 1 || s.AllowCIDRs[0] != "198.51.100.7/32" || s.Mapping.ExtraVars["version"] != "$.release.tag" {
		t.Fatalf("spec = %+v", s)
	}
	in := w.trigs.validated[0]
	if in.Kind != triggers.KindTemplateRun || *in.TemplateID != w.x || in.Mapping.Limit != "$.hosts" || !in.Enabled {
		t.Fatalf("validated = %+v", in)
	}
	repoSpec := &runrequests.TriggerSpec{Kind: triggers.KindRepoSync, RepoID: &w.repo, Name: "push 2", Enabled: true, Preset: triggers.PresetGitHub}
	ch, tmpl, rev, err = w.resolver().ValidateTrigger(context.Background(), id(), id(), runrequests.TriggerInput{Action: runrequests.TriggerCreate, Spec: repoSpec})
	if err != nil || tmpl != uuid.Nil || rev != 0 || ch.Kind != triggers.KindRepoSync {
		t.Fatalf("repo sync = %+v %v %d %v", ch, tmpl, rev, err)
	}
}

// An update merges its patch into the trigger as it is now: what it leaves
// out keeps the trigger's value, a list or mapping it gives replaces the
// trigger's.
func TestValidateTriggerUpdateMergesThePatch(t *testing.T) {
	w := newTriggerWorld()
	name, cidrs := "renamed", []string{}
	ch, _, _, err := w.resolver().ValidateTrigger(context.Background(), id(), w.run, runrequests.TriggerInput{
		Action: runrequests.TriggerUpdate, TriggerID: w.run, Patch: &runrequests.TriggerPatch{Name: &name, AllowCIDRs: &cidrs},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := ch.Spec
	if s.Name != "renamed" || s.Enabled || s.Preset != triggers.PresetGitHub || len(s.AllowCIDRs) != 0 || s.Mapping.ExtraVars["tag"] != "$.ref" ||
		s.RateLimit.Count != 5 || *s.TemplateID != w.x || ch.Kind != triggers.KindTemplateRun {
		t.Fatalf("merged = %+v", s)
	}
	on := true
	mapping := triggers.Mapping{Limit: "$.host"}
	ch, _, _, err = w.resolver().ValidateTrigger(context.Background(), id(), w.run, runrequests.TriggerInput{
		Action: runrequests.TriggerUpdate, TriggerID: w.run, Patch: &runrequests.TriggerPatch{Enabled: &on, Mapping: &mapping},
	})
	if err != nil || !ch.Spec.Enabled || len(ch.Spec.Mapping.ExtraVars) != 0 || ch.Spec.Mapping.Limit != "$.host" || len(ch.Spec.AllowCIDRs) != 1 {
		t.Fatalf("enable and new mapping = %+v %v", ch.Spec, err)
	}
}

func TestValidateTriggerRefusals(t *testing.T) {
	cases := map[string]struct {
		err  error
		in   func(w *triggerWorld) runrequests.TriggerInput
		want error
	}{
		"a name taken": {err: triggers.ErrNameTaken, want: runrequests.ErrInvalidInput,
			in: func(w *triggerWorld) runrequests.TriggerInput {
				return runrequests.TriggerInput{Action: runrequests.TriggerCreate, Spec: w.runSpec()}
			}},
		"a refused setting": {err: triggers.ErrInvalidInput, want: runrequests.ErrInvalidInput,
			in: func(w *triggerWorld) runrequests.TriggerInput {
				return runrequests.TriggerInput{Action: runrequests.TriggerCreate, Spec: w.runSpec()}
			}},
		"an update of no trigger": {want: runrequests.ErrNotFound,
			in: func(*triggerWorld) runrequests.TriggerInput {
				return runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: id(), Patch: &runrequests.TriggerPatch{}}
			}},
		"a rotation of no trigger": {want: runrequests.ErrNotFound,
			in: func(*triggerWorld) runrequests.TriggerInput {
				return runrequests.TriggerInput{Action: runrequests.TriggerRotate, TriggerID: id()}
			}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newTriggerWorld()
			w.trigs.validateErr = c.err
			in := c.in(w)
			target := in.TriggerID
			if target == uuid.Nil {
				target = id()
			}
			if _, _, _, err := w.resolver().ValidateTrigger(context.Background(), id(), target, in); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// A rotation changes nothing but the secret: it names the trigger's kind
// and template, and validates nothing.
func TestValidateTriggerRotate(t *testing.T) {
	w := newTriggerWorld()
	ch, tmpl, rev, err := w.resolver().ValidateTrigger(context.Background(), id(), w.run, runrequests.TriggerInput{Action: runrequests.TriggerRotate, TriggerID: w.run})
	if err != nil || ch.Kind != triggers.KindTemplateRun || ch.Spec != nil || tmpl != w.x || rev != 3 || len(w.trigs.validated) != 0 {
		t.Fatalf("rotate = %+v %v %d %v", ch, tmpl, rev, err)
	}
}

func triggerRequest(kind triggers.Kind, ch runrequests.TriggerChange, target, template uuid.UUID) runrequests.Request {
	ch.Kind = kind
	return runrequests.Request{ID: id(), Kind: runrequests.KindTrigger, TemplateID: template, Trigger: &ch, TargetID: &target}
}

// The picture of an update shows the trigger as it is (with its revision)
// next to how the approval leaves it, the template the trigger runs, and
// the URL senders post to; never a secret.
func TestTriggerPictureOfAnUpdate(t *testing.T) {
	w := newTriggerWorld()
	r := w.resolver()
	name := "renamed"
	ch, _, _, err := r.ValidateTrigger(context.Background(), id(), w.run, runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: w.run, Patch: &runrequests.TriggerPatch{Name: &name}})
	if err != nil {
		t.Fatal(err)
	}
	req := triggerRequest(triggers.KindTemplateRun, ch, w.run, w.x)
	p, err := r.Picture(context.Background(), id(), req)
	if err != nil {
		t.Fatal(err)
	}
	pt := p.Trigger
	if p.TemplateName != "X" || p.TemplateRevision != 3 || pt == nil || pt.Action != runrequests.TriggerUpdate || pt.Gone ||
		pt.URL != "https://px.example.test/api/v1/hooks/jobs/"+w.run.String() || pt.TriggerID != w.run.String() {
		t.Fatalf("picture = %+v / %+v", p, pt)
	}
	cur, next := pt.Current, pt.New
	if cur == nil || next == nil || cur.Name != "deploy hook" || cur.Revision != 4 || cur.Enabled || next.Name != "renamed" || next.Revision != 0 ||
		cur.TemplateName != "X" || cur.SignatureHeader != "X-Hub-Signature-256" || cur.AllowCIDRs[0] != "192.0.2.0/24" || cur.Mapping.ExtraVars["tag"] != "$.ref" {
		t.Fatalf("current %+v, new %+v", cur, next)
	}
	before := runrequests.Fingerprint(req.ID, p)
	// The trigger changed since: the picture, and so the fingerprint, too.
	stored := w.trigs.byID[w.run]
	stored.Revision++
	w.trigs.byID[w.run] = stored
	if p, _ = r.Picture(context.Background(), id(), req); runrequests.Fingerprint(req.ID, p) == before {
		t.Fatal("the fingerprint ignores a change of the trigger")
	}
	// So does a change of its template.
	before = runrequests.Fingerprint(req.ID, p)
	tmpl := w.tmpls.fakeTemplates[w.x]
	tmpl.Revision++
	w.tmpls.fakeTemplates[w.x] = tmpl
	if p, _ = r.Picture(context.Background(), id(), req); runrequests.Fingerprint(req.ID, p) == before {
		t.Fatal("the fingerprint ignores a change of the template")
	}
}

// A repository sync trigger's picture names no template; its states name
// the repository with what decides its sync (URL, branch). A repository
// or trigger that is gone makes the request unapprovable.
func TestTriggerPictureOfARepositorySync(t *testing.T) {
	w := newTriggerWorld()
	r := w.resolver()
	req := triggerRequest(triggers.KindRepoSync, runrequests.TriggerChange{Action: runrequests.TriggerRotate}, w.sync, uuid.Nil)
	p, err := r.Picture(context.Background(), id(), req)
	if err != nil {
		t.Fatal(err)
	}
	pt := p.Trigger
	if p.TemplateID != "" || p.TemplateName != "" || pt.New != nil || pt.Current == nil || pt.Gone ||
		pt.Current.RepoName != "infra" || pt.Current.RepoURL != "https://git.example.test/infra.git" || pt.Current.RepoBranch != "main" ||
		pt.URL != "https://px.example.test/api/v1/hooks/repos/"+w.sync.String() {
		t.Fatalf("picture = %+v / %+v", p, pt)
	}
	before := runrequests.Fingerprint(req.ID, p)
	repo := w.repos[w.repo]
	repo.Branch = "release"
	w.repos[w.repo] = repo
	if p, _ = r.Picture(context.Background(), id(), req); runrequests.Fingerprint(req.ID, p) == before {
		t.Fatal("the fingerprint ignores a change of the repository's branch")
	}
	delete(w.repos, w.repo)
	if p, err = r.Picture(context.Background(), id(), req); err != nil || !p.Trigger.Gone {
		t.Fatalf("repository gone = %+v %v", p.Trigger, err)
	}
	gone := triggerRequest(triggers.KindTemplateRun, runrequests.TriggerChange{Action: runrequests.TriggerRotate}, id(), w.x)
	if p, err = r.Picture(context.Background(), id(), gone); err != nil || !p.Trigger.Gone || p.Trigger.Current != nil {
		t.Fatalf("trigger gone = %+v %v", p.Trigger, err)
	}
}

// The approval applies exactly what was pictured: a create under the
// request's target as the approver, an update and a rotation at the
// revision the person saw; a create and a rotation hand back the secret.
func TestApplyTrigger(t *testing.T) {
	w := newTriggerWorld()
	r := w.resolver()
	actor := id()
	target := id()
	create := triggerRequest(triggers.KindTemplateRun, runrequests.TriggerChange{Action: runrequests.TriggerCreate, Spec: w.runSpec()}, target, w.x)
	secret, err := r.ApplyTrigger(context.Background(), id(), create, runrequests.PictureTrigger{Action: runrequests.TriggerCreate}, actor)
	if err != nil || secret != "new-secret" || w.trigs.createdAs[0] != target || w.trigs.createdAs[1] != actor || w.trigs.created[0].Name != "release hook" {
		t.Fatalf("create = %q %v %+v", secret, err, w.trigs)
	}
	update := triggerRequest(triggers.KindTemplateRun, runrequests.TriggerChange{Action: runrequests.TriggerUpdate, Spec: w.runSpec()}, w.run, w.x)
	secret, err = r.ApplyTrigger(context.Background(), id(), update, runrequests.PictureTrigger{Action: runrequests.TriggerUpdate, Current: &runrequests.TriggerState{Revision: 4}}, actor)
	if err != nil || secret != "" || w.trigs.updatedRev[0] != 4 || len(w.trigs.updated[0].AllowCIDRs) != 1 {
		t.Fatalf("update = %q %v %+v", secret, err, w.trigs)
	}
	rotate := triggerRequest(triggers.KindTemplateRun, runrequests.TriggerChange{Action: runrequests.TriggerRotate}, w.run, w.x)
	secret, err = r.ApplyTrigger(context.Background(), id(), rotate, runrequests.PictureTrigger{Action: runrequests.TriggerRotate, Current: &runrequests.TriggerState{Revision: 4}}, actor)
	if err != nil || secret != "rotated-secret" || w.trigs.rotatedRev[0] != 4 {
		t.Fatalf("rotate = %q %v", secret, err)
	}
	if _, err := r.ApplyTrigger(context.Background(), id(), rotate, runrequests.PictureTrigger{Action: runrequests.TriggerRotate}, actor); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("rotation of a gone trigger: %v", err)
	}
	w.trigs.applyErr = triggers.ErrRevisionMismatch
	if _, err := r.ApplyTrigger(context.Background(), id(), rotate, runrequests.PictureTrigger{Action: runrequests.TriggerRotate, Current: &runrequests.TriggerState{Revision: 4}}, actor); !errors.Is(err, runrequests.ErrChanged) {
		t.Fatalf("a trigger changed since: %v", err)
	}
	w.trigs.applyErr = triggers.ErrNotFound
	if _, err := r.ApplyTrigger(context.Background(), id(), rotate, runrequests.PictureTrigger{Action: runrequests.TriggerRotate, Current: &runrequests.TriggerState{Revision: 4}}, actor); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("a trigger deleted since: %v", err)
	}
}

// An update that leaves enabled out keeps the trigger's enabled state as
// it is when the person approves: a trigger a person disabled after the
// request stays disabled (the picture shows it, and the approval stores
// it), so an assistant's old rename never switches it back on.
func TestUpdateKeepsADisabledTriggerDisabled(t *testing.T) {
	w := newTriggerWorld()
	stored := w.trigs.byID[w.run]
	stored.Enabled = true
	w.trigs.byID[w.run] = stored
	r := w.resolver()
	name := "renamed"
	ch, _, _, err := r.ValidateTrigger(context.Background(), id(), w.run, runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: w.run, Patch: &runrequests.TriggerPatch{Name: &name}})
	if err != nil || !ch.KeepEnabled || !ch.Spec.Enabled {
		t.Fatalf("change = %+v %v", ch, err)
	}
	stored.Enabled, stored.Revision = false, stored.Revision+1
	w.trigs.byID[w.run] = stored
	req := triggerRequest(triggers.KindTemplateRun, ch, w.run, w.x)
	p, err := r.Picture(context.Background(), id(), req)
	if err != nil || p.Trigger.New.Enabled || p.Trigger.Current.Enabled {
		t.Fatalf("picture = %+v %v", p.Trigger, err)
	}
	if _, err := r.ApplyTrigger(context.Background(), id(), req, *p.Trigger, id()); err != nil || w.trigs.updated[0].Enabled {
		t.Fatalf("applied = %+v %v", w.trigs.updated, err)
	}
	// A patch that sets enabled is the request's own choice.
	on := true
	ch, _, _, err = r.ValidateTrigger(context.Background(), id(), w.run, runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: w.run, Patch: &runrequests.TriggerPatch{Enabled: &on}})
	if err != nil || ch.KeepEnabled || !ch.Spec.Enabled {
		t.Fatalf("enable = %+v %v", ch, err)
	}
}

// An update that leaves the trigger as it is is refused: there is nothing
// for a person to approve.
func TestTriggerUpdateThatChangesNothingIsRefused(t *testing.T) {
	same, off := "deploy hook", false
	cidrs := []string{"192.0.2.0/24"}
	for name, patch := range map[string]runrequests.TriggerPatch{
		"no field":          {},
		"the values it has": {Name: &same, Enabled: &off, AllowCIDRs: &cidrs},
	} {
		t.Run(name, func(t *testing.T) {
			w := newTriggerWorld()
			_, _, _, err := w.resolver().ValidateTrigger(context.Background(), id(), w.run, runrequests.TriggerInput{Action: runrequests.TriggerUpdate, TriggerID: w.run, Patch: &patch})
			if !errors.Is(err, runrequests.ErrInvalidInput) || !strings.Contains(err.Error(), "nothing to change") {
				t.Fatalf("err = %v, want nothing to change", err)
			}
		})
	}
}
