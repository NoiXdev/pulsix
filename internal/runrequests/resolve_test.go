package runrequests_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/chain"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
)

// The resolver's sources as plain fakes, each with an error knob.
type (
	failingTemplates struct {
		fakeTemplates
		fail map[uuid.UUID]error
	}
	playbookMap map[uuid.UUID]playbooks.Playbook
	repoMap     map[uuid.UUID]repos.Repo
	credMap     map[uuid.UUID]credentials.Credential
	hostList    struct {
		hs  []hosts.Host
		err error
	}
	sshMap struct {
		ssh      map[uuid.UUID]sshconf.HostSSH
		bastions map[uuid.UUID]sshconf.Bastion
	}
	// runVars stands in for the real values a run receives, secrets in
	// the clear.
	runVars struct {
		doc      map[string]any
		conns    map[string]render.Connection
		secrets  []string
		err      error
		extra    map[string]any
		extraErr error
	}
	targetRunner struct {
		targets    []uuid.UUID
		targetsErr error
		retryTmpl  uuid.UUID
		failed     []uuid.UUID
		retryErr   error
	}
	switchable struct {
		on  bool
		err error
	}
	groupRows struct {
		rows []chain.FollowUp
		err  error
	}
)

func (f failingTemplates) Get(ctx context.Context, org, tid uuid.UUID) (templates.Template, error) {
	if err := f.fail[tid]; err != nil {
		return templates.Template{}, err
	}
	return f.fakeTemplates.Get(ctx, org, tid)
}

func (m playbookMap) Get(_ context.Context, _, id uuid.UUID) (playbooks.Playbook, error) {
	if p, ok := m[id]; ok {
		return p, nil
	}
	return playbooks.Playbook{}, playbooks.ErrNotFound
}

func (m repoMap) Get(_ context.Context, _, id uuid.UUID) (repos.Repo, error) {
	if r, ok := m[id]; ok {
		return r, nil
	}
	return repos.Repo{}, repos.ErrNotFound
}

func (m credMap) Get(_ context.Context, _, id uuid.UUID) (credentials.Credential, error) {
	if c, ok := m[id]; ok {
		return c, nil
	}
	return credentials.Credential{}, errors.New("no such credential")
}

func (h hostList) ListHostsByIDs(context.Context, uuid.UUID, []uuid.UUID) ([]hosts.Host, error) {
	return h.hs, h.err
}

func (s sshMap) HostSSH(_ context.Context, _, id uuid.UUID) (sshconf.HostSSH, error) {
	if c, ok := s.ssh[id]; ok {
		return c, nil
	}
	return sshconf.HostSSH{}, errors.New("no ssh settings")
}

func (s sshMap) GetBastion(_ context.Context, _, id uuid.UUID) (sshconf.Bastion, error) {
	if b, ok := s.bastions[id]; ok {
		return b, nil
	}
	return sshconf.Bastion{}, errors.New("no such bastion")
}

func (v *runVars) InventoryVars(context.Context, uuid.UUID, []uuid.UUID) (render.Inventory, error) {
	return render.Inventory{Doc: v.doc, Connections: v.conns, Secrets: v.secrets}, v.err
}

func (v *runVars) ExtraVars(context.Context, uuid.UUID, uuid.UUID) (map[string]any, error) {
	return v.extra, v.extraErr
}

func (r *targetRunner) RunTargets(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return r.targets, r.targetsErr
}

func (r *targetRunner) RetryTargets(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, uuid.UUID, []uuid.UUID, error) {
	return r.retryTmpl, uuid.Nil, r.failed, r.retryErr
}

func (r *targetRunner) EnqueueRunWith(context.Context, uuid.UUID, uuid.UUID, jobs.RunRequest) ([]jobs.Job, error) {
	return nil, nil
}

func (r *targetRunner) RetryFailedInto(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, uuid.UUID, *[]uuid.UUID) ([]jobs.Job, error) {
	return nil, nil
}

func (s switchable) StartsFollowUps(context.Context, uuid.UUID) (bool, error) { return s.on, s.err }

func (g groupRows) ListForGroup(context.Context, uuid.UUID, uuid.UUID) ([]chain.FollowUp, error) {
	return g.rows, g.err
}

func (g groupRows) ClearUnstarted(context.Context, uuid.UUID, uuid.UUID) error { return nil }

// world is a whole organization for the resolver: template X runs a repo
// playbook on web-1 (ssh, credential, named bastion), web-2 (no ssh
// settings) and web-3 (an unknown bastion), with follow-up Y.
type world struct {
	tmpls                failingTemplates
	pbs                  playbookMap
	repos                repoMap
	creds                credMap
	hosts                hostList
	ssh                  sshMap
	inv                  *runVars
	key                  []byte
	jobs                 *targetRunner
	settings             switchable
	rows                 groupRows
	x, y, pb, repo       uuid.UUID
	h1, h2, h3, cred     uuid.UUID
	bastion, lostBastion uuid.UUID
	repoVault, tmplVault uuid.UUID
	gitCred              uuid.UUID
}

func newWorld() *world {
	w := &world{x: id(), y: id(), pb: id(), repo: id(), h1: id(), h2: id(), h3: id(), cred: id(),
		bastion: id(), lostBastion: id(), repoVault: id(), tmplVault: id(), gitCred: id()}
	path := "site.yml"
	w.tmpls = failingTemplates{fail: map[uuid.UUID]error{}, fakeTemplates: fakeTemplates{
		w.x: {ID: w.x, Name: "X", Revision: 3, Limit: "web", PlaybookID: w.pb, CredentialID: &w.cred,
			ExtraVars:        map[string]any{"tag": "v1"},
			VaultCredentials: []templates.VaultCredential{{CredentialID: w.tmplVault, Label: "prod"}},
			FollowUps:        []templates.FollowUp{{TemplateID: w.y, Name: "Y"}}},
		w.y: {ID: w.y, Name: "Y", Revision: 5},
	}}
	w.pbs = playbookMap{w.pb: {ID: w.pb, Source: "repo", Path: &path, RepoID: &w.repo}}
	w.repos = repoMap{w.repo: {ID: w.repo, Name: "infra", URL: "https://git.example.test/infra.git", Branch: "main", Subpath: "ansible",
		CredentialID: &w.gitCred, VaultIdentities: []repos.VaultIdentity{{CredentialID: w.repoVault, Label: "dev"}}}}
	w.creds = credMap{w.cred: {Name: "host-login"}, w.repoVault: {Name: "dev-vault"}, w.tmplVault: {Name: "prod-vault"}, w.gitCred: {Name: "git-deploy"}}
	w.hosts = hostList{hs: []hosts.Host{{ID: w.h1, Name: "web-1", Address: "10.0.0.1"}, {ID: w.h2, Name: "web-2", Address: "10.0.0.2"},
		{ID: w.h3, Name: "web-3", Address: "10.0.0.3"}}}
	w.ssh = sshMap{
		ssh: map[uuid.UUID]sshconf.HostSSH{
			w.h1: {Username: "ansible", Port: 22, CredentialID: &w.cred, BastionID: &w.bastion, BastionMode: sshconf.ModeInherit},
			w.h3: {Username: "root", Port: 2222, BastionID: &w.lostBastion},
		},
		bastions: map[uuid.UUID]sshconf.Bastion{w.bastion: {Name: "jump-1"}},
	}
	w.inv = &runVars{doc: map[string]any{"all": map[string]any{"vars": map[string]any{"env": "prod"}}}, extra: map[string]any{"tag": "v1"}}
	w.key = runrequests.VarsDigestKey([32]byte{1, 2, 3})
	w.jobs = &targetRunner{targets: []uuid.UUID{w.h1, w.h2, w.h3}, retryTmpl: w.x, failed: []uuid.UUID{w.h2}}
	return w
}

func (w *world) resolver() *runrequests.Resolver {
	return runrequests.NewResolver(runrequests.Sources{
		Templates: w.tmpls, Playbooks: w.pbs, Repos: w.repos, Jobs: w.jobs, Hosts: w.hosts, SSH: w.ssh,
		Credentials: w.creds, Vars: w.inv, VarsKey: w.key, Settings: w.settings, FollowUpRuns: w.rows,
	})
}

func (w *world) picture(t *testing.T, req runrequests.Request) runrequests.Picture {
	t.Helper()
	p, err := w.resolver().Picture(context.Background(), id(), req)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPictureOfARepoRun(t *testing.T) {
	w := newWorld()
	p := w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x})

	if p.TemplateID != w.x.String() || p.TemplateName != "X" || p.TemplateRevision != 3 || p.Limit != "web" ||
		p.SSHCredential != "host-login" || p.SSHCredentialID != w.cred.String() || p.ExtraVars["tag"] != "v1" || p.OnlyHost != "" || p.Retry != nil {
		t.Fatalf("template part = %+v", p)
	}
	if p.Playbook != (runrequests.PicturePlaybook{ID: w.pb.String(), Source: "repo", Path: "site.yml"}) {
		t.Fatalf("playbook = %+v", p.Playbook)
	}
	if p.Repo == nil || *p.Repo != (runrequests.PictureRepo{ID: w.repo.String(), Name: "infra", URL: "https://git.example.test/infra.git", Branch: "main", Subpath: "ansible",
		GitCredentialID: w.gitCred.String(), GitCredential: "git-deploy"}) {
		t.Fatalf("repo = %+v", p.Repo)
	}
	if len(p.VaultIdentities) != 2 || p.VaultIdentities[0] != (runrequests.PictureVault{Label: "dev", CredentialID: w.repoVault.String(), Credential: "dev-vault"}) ||
		p.VaultIdentities[1] != (runrequests.PictureVault{Label: "prod", CredentialID: w.tmplVault.String(), Credential: "prod-vault"}) {
		t.Fatalf("vault identities = %+v", p.VaultIdentities)
	}
	want := []runrequests.PictureHost{
		{ID: w.h1.String(), Name: "web-1", Address: "10.0.0.1", SSHUser: "ansible", SSHPort: 22, JumpHost: "jump-1 (inherit)", JumpHostID: w.bastion.String(),
			Credential: "host-login", CredentialID: w.cred.String()},
		{ID: w.h2.String(), Name: "web-2", Address: "10.0.0.2"},
		{ID: w.h3.String(), Name: "web-3", Address: "10.0.0.3", SSHUser: "root", SSHPort: 2222, JumpHost: w.lostBastion.String(), JumpHostID: w.lostBastion.String()},
	}
	if len(p.Targets) != len(want) {
		t.Fatalf("targets = %+v", p.Targets)
	}
	for i := range want {
		if p.Targets[i] != want[i] {
			t.Fatalf("target %d = %+v, want %+v", i, p.Targets[i], want[i])
		}
	}
	if p.VarsDigest == "" || p.FollowUps == nil || len(p.FollowUps.Templates) != 1 || p.FollowUps.Templates[0].TemplateID != w.y.String() {
		t.Fatalf("digest %q, follow-ups %+v", p.VarsDigest, p.FollowUps)
	}
}

func TestPictureOfAnInlineRunOnOneHost(t *testing.T) {
	w := newWorld()
	inline := "- hosts: all\n"
	w.pbs[w.pb] = playbooks.Playbook{ID: w.pb, Source: "editor", Inline: &inline}
	tmpl := w.tmpls.fakeTemplates[w.x]
	tmpl.ExtraVars, tmpl.CredentialID, tmpl.VaultCredentials = nil, nil, nil
	w.tmpls.fakeTemplates[w.x] = tmpl
	w.hosts.hs = w.hosts.hs[1:2]

	p := w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x, HostID: &w.h2})
	sum := sha256.Sum256([]byte(inline))
	if p.Playbook.InlineDigest != hex.EncodeToString(sum[:]) {
		t.Fatalf("inline digest = %q", p.Playbook.InlineDigest)
	}
	if p.Repo != nil || p.Playbook.Path != "" || p.SSHCredential != "" || len(p.VaultIdentities) != 0 {
		t.Fatalf("inline picture = %+v", p)
	}
	if p.ExtraVars == nil || len(p.ExtraVars) != 0 {
		t.Fatalf("extra vars = %#v, want an empty map", p.ExtraVars)
	}
	if p.OnlyHost != w.h2.String() || len(p.Targets) != 1 || p.Targets[0].ID != w.h2.String() {
		t.Fatalf("one host = %q %+v", p.OnlyHost, p.Targets)
	}

	// No playbook at all, and an unknown credential shows by id.
	tmpl.PlaybookID, tmpl.CredentialID = uuid.Nil, &w.repo
	w.tmpls.fakeTemplates[w.x] = tmpl
	p = w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x})
	if p.Playbook != (runrequests.PicturePlaybook{}) || p.SSHCredential != w.repo.String() || p.SSHCredentialID != w.repo.String() {
		t.Fatalf("no playbook = %+v, credential %q", p.Playbook, p.SSHCredential)
	}
}

func TestPictureOfARetry(t *testing.T) {
	w := newWorld()
	job := id()
	req := runrequests.Request{Kind: runrequests.KindRetryFailed, TemplateID: w.x, JobID: &job}
	w.hosts.hs = w.hosts.hs[1:2]

	p := w.picture(t, req)
	if p.Retry == nil || p.Retry.JobID != job.String() || len(p.Retry.FailedHosts) != 1 || p.Retry.FailedHosts[0] != w.h2.String() ||
		len(p.Targets) != 1 || p.Targets[0].ID != w.h2.String() {
		t.Fatalf("retry = %+v, targets %+v", p.Retry, p.Targets)
	}

	// A retry that can no longer run still shows, with no hosts, no
	// inventory and no follow-ups.
	w.jobs.retryErr = jobs.ErrNothingToRetry
	w.inv.err = errors.New("must not render")
	p = w.picture(t, req)
	if p.Retry == nil || p.Retry.FailedHosts == nil || len(p.Retry.FailedHosts) != 0 || len(p.Targets) != 0 || p.FollowUps != nil || p.VarsDigest == "" {
		t.Fatalf("retry that cannot run = %+v", p)
	}
}

func TestVarsDigestFollowsInventoryAndExtraVars(t *testing.T) {
	w := newWorld()
	req := runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x}
	base := w.picture(t, req).VarsDigest
	if again := w.picture(t, req).VarsDigest; again != base {
		t.Fatalf("digest not stable: %s vs %s", base, again)
	}
	w.inv.doc = map[string]any{"all": map[string]any{"vars": map[string]any{"env": "staging"}}}
	changedInv := w.picture(t, req).VarsDigest
	w.inv.extra = map[string]any{"tag": "v2"}
	changedVars := w.picture(t, req).VarsDigest
	if changedInv == base || changedVars == changedInv || changedVars == base {
		t.Fatalf("digests base %s, inventory %s, vars %s must all differ", base, changedInv, changedVars)
	}
}

func TestPictureErrors(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name   string
		mutate func(*world)
		is     error
	}{
		{"template gone", func(w *world) { delete(w.tmpls.fakeTemplates, w.x) }, runrequests.ErrNotFound},
		{"playbook gone", func(w *world) { delete(w.pbs, w.pb) }, runrequests.ErrNotFound},
		{"repo gone", func(w *world) { delete(w.repos, w.repo) }, runrequests.ErrNotFound},
		{"targets gone", func(w *world) { w.jobs.targetsErr = jobs.ErrNotFound }, runrequests.ErrNotFound},
		{"targets fail", func(w *world) { w.jobs.targetsErr = boom }, boom},
		{"hosts fail", func(w *world) { w.hosts.err = boom }, boom},
		{"inventory fails", func(w *world) { w.inv.err = boom }, boom},
		{"extra vars fail", func(w *world) { w.inv.extraErr = boom }, boom},
		{"no digest key", func(w *world) { w.key = nil }, runrequests.ErrNoVarsKey},
		{"setting fails", func(w *world) { w.settings.err = boom }, boom},
		{"follow-up template fails", func(w *world) { w.settings.on = true; w.tmpls.fail[w.y] = boom }, boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			tc.mutate(w)
			if _, err := w.resolver().Picture(context.Background(), id(), runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x}); !errors.Is(err, tc.is) {
				t.Fatalf("picture = %v, want %v", err, tc.is)
			}
		})
	}

	t.Run("retry rows fail", func(t *testing.T) {
		w := newWorld()
		w.rows.err = boom
		job := id()
		if _, err := w.resolver().Picture(context.Background(), id(), runrequests.Request{Kind: runrequests.KindRetryFailed, TemplateID: w.x, JobID: &job}); !errors.Is(err, boom) {
			t.Fatalf("picture = %v, want boom", err)
		}
	})
}

func TestValidateRun(t *testing.T) {
	w := newWorld()
	ctx, org := context.Background(), id()
	if rev, err := w.resolver().ValidateRun(ctx, org, w.x, nil); err != nil || rev != 3 {
		t.Fatalf("whole run = %d %v", rev, err)
	}
	if rev, err := w.resolver().ValidateRun(ctx, org, w.x, &w.h2); err != nil || rev != 3 {
		t.Fatalf("one target = %d %v", rev, err)
	}
	if _, err := w.resolver().ValidateRun(ctx, org, w.x, &w.pb); !errors.Is(err, runrequests.ErrInvalidInput) {
		t.Fatalf("host outside the targets = %v", err)
	}
	if _, err := w.resolver().ValidateRun(ctx, org, id(), nil); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("unknown template = %v", err)
	}
	w.jobs.targetsErr = jobs.ErrNotFound
	if _, err := w.resolver().ValidateRun(ctx, org, w.x, &w.h2); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("targets gone = %v", err)
	}
}

func TestValidateRetry(t *testing.T) {
	ctx, org, job := context.Background(), id(), id()
	w := newWorld()
	if tmpl, rev, err := w.resolver().ValidateRetry(ctx, org, job); err != nil || tmpl != w.x || rev != 3 {
		t.Fatalf("retry = %s %d %v", tmpl, rev, err)
	}
	for _, tc := range []struct {
		name string
		err  error
		is   error
	}{
		{"nothing to retry", jobs.ErrNothingToRetry, runrequests.ErrInvalidInput},
		{"still running", jobs.ErrNotRunning, runrequests.ErrInvalidInput},
		{"invalid", jobs.ErrInvalidInput, runrequests.ErrInvalidInput},
		{"job gone", jobs.ErrNotFound, runrequests.ErrNotFound},
	} {
		w.jobs.retryErr = tc.err
		if _, _, err := w.resolver().ValidateRetry(ctx, org, job); !errors.Is(err, tc.is) {
			t.Fatalf("%s = %v, want %v", tc.name, err, tc.is)
		}
	}
	w.jobs.retryErr = nil
	delete(w.tmpls.fakeTemplates, w.x)
	if _, _, err := w.resolver().ValidateRetry(ctx, org, job); !errors.Is(err, runrequests.ErrNotFound) {
		t.Fatalf("template gone = %v", err)
	}
}
