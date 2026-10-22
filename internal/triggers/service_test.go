package triggers_test

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/triggers"
)

var ctx = context.Background()

type fakeBox struct{}

func (fakeBox) SealAAD(plain, aad []byte) ([]byte, error) {
	return []byte("sealed:" + string(aad) + ":" + string(plain)), nil
}

func (fakeBox) OpenAAD(sealed, aad []byte) ([]byte, error) {
	rest, ok := strings.CutPrefix(string(sealed), "sealed:"+string(aad)+":")
	if !ok {
		return nil, errors.New("wrong aad")
	}
	return []byte(rest), nil
}

type fakeStore struct {
	mu        sync.Mutex
	byID      map[uuid.UUID]triggers.Trigger
	secrets   map[uuid.UUID][]byte
	fills     int
	lastLimit int
}

func newFakeStore() *fakeStore {
	return &fakeStore{byID: map[uuid.UUID]triggers.Trigger{}, secrets: map[uuid.UUID][]byte{}}
}

func (f *fakeStore) Create(_ context.Context, t triggers.Trigger, sealed []byte) (triggers.Trigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.byID {
		if o.OrgID == t.OrgID && o.Name == t.Name {
			return triggers.Trigger{}, triggers.ErrNameTaken
		}
	}
	t.Revision = 1
	f.byID[t.ID], f.secrets[t.ID] = t, sealed
	f.fills++
	return t, nil
}

func (f *fakeStore) Get(_ context.Context, orgID, id uuid.UUID) (triggers.Trigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.byID[id]
	if !ok || t.OrgID != orgID {
		return triggers.Trigger{}, triggers.ErrNotFound
	}
	return t, nil
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID, lf triggers.ListFilter) ([]triggers.Trigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []triggers.Trigger
	for _, t := range f.byID {
		if t.OrgID == orgID && (lf.TemplateID == nil || (t.TemplateID != nil && *t.TemplateID == *lf.TemplateID)) {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeStore) Update(_ context.Context, t triggers.Trigger, revision int64) (triggers.Trigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.byID[t.ID]
	if !ok || cur.OrgID != t.OrgID {
		return triggers.Trigger{}, triggers.ErrNotFound
	}
	if cur.Revision != revision {
		return triggers.Trigger{}, triggers.ErrRevisionMismatch
	}
	t.Revision = revision + 1
	f.byID[t.ID] = t
	f.fills++
	return t, nil
}

func (f *fakeStore) SetSecret(_ context.Context, orgID, id uuid.UUID, sealed []byte) (triggers.Trigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.byID[id]
	if !ok || t.OrgID != orgID {
		return triggers.Trigger{}, triggers.ErrNotFound
	}
	f.secrets[id] = sealed
	return t, nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.byID[id]; !ok || t.OrgID != orgID {
		return triggers.ErrNotFound
	}
	delete(f.byID, id)
	return nil
}

func (f *fakeStore) Deliveries(_ context.Context, _, _ uuid.UUID, limit int) ([]triggers.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastLimit = limit
	return nil, nil
}

func (f *fakeStore) Resolve(_ context.Context, id uuid.UUID) (triggers.Trigger, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.byID[id]
	if !ok {
		return triggers.Trigger{}, nil, triggers.ErrNotFound
	}
	return t, f.secrets[id], nil
}

func (f *fakeStore) TakeToken(context.Context, uuid.UUID, uuid.UUID, triggers.RateLimit, time.Time) (bool, time.Duration, error) {
	return true, 0, nil
}

func (f *fakeStore) RecordDelivery(context.Context, triggers.Delivery, bool) error { return nil }

func (f *fakeStore) NoteRateLimited(context.Context, uuid.UUID, uuid.UUID, triggers.Outcome, time.Time) error {
	return nil
}

type fakeTemplates struct {
	byID map[uuid.UUID]templates.Template
	// opts records the options every ValidateOverrides call received.
	opts *[]templates.OverrideOptions
}

func (f fakeTemplates) Get(_ context.Context, orgID, id uuid.UUID) (templates.Template, error) {
	t, ok := f.byID[id]
	if !ok || t.OrgID != orgID {
		return templates.Template{}, templates.ErrNotFound
	}
	return t, nil
}

func (f fakeTemplates) ValidateOverrides(_ context.Context, _, _ uuid.UUID, in templates.Overrides, opt templates.OverrideOptions) (templates.Overrides, error) {
	if f.opts != nil {
		*f.opts = append(*f.opts, opt)
	}
	return in, nil
}

type fakeRepos struct{ byID map[uuid.UUID]repos.Repo }

func (f fakeRepos) Get(_ context.Context, orgID, id uuid.UUID) (repos.Repo, error) {
	r, ok := f.byID[id]
	if !ok || r.OrgID != orgID {
		return repos.Repo{}, repos.ErrNotFound
	}
	return r, nil
}

type world struct {
	svc      *triggers.Service
	store    *fakeStore
	tmpls    fakeTemplates
	repos    fakeRepos
	org      uuid.UUID
	actor    uuid.UUID
	template uuid.UUID
	repo     uuid.UUID
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{store: newFakeStore(), org: uuid.Must(uuid.NewV7()), actor: uuid.Must(uuid.NewV7()), template: uuid.Must(uuid.NewV7()), repo: uuid.Must(uuid.NewV7())}
	sc := &schema.Schema{Fields: []schema.Field{{Name: "release", Type: schema.TypeString}, {Name: "token", Type: schema.TypeSecret}}}
	w.tmpls = fakeTemplates{byID: map[uuid.UUID]templates.Template{w.template: {ID: w.template, OrgID: w.org, ExtraVarsSchema: sc}}, opts: &[]templates.OverrideOptions{}}
	w.repos = fakeRepos{byID: map[uuid.UUID]repos.Repo{w.repo: {ID: w.repo, OrgID: w.org}}}
	w.svc = triggers.NewService(triggers.Deps{
		Store:     w.store,
		Templates: w.tmpls,
		Repos:     w.repos,
		Box:       fakeBox{}, PublicURL: "https://pulsix.example.test/",
		Now: func() time.Time { return time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC) }, Rand: rand.Reader,
	})
	return w
}

func (w *world) templateInput() triggers.Input {
	return triggers.Input{Kind: triggers.KindTemplateRun, TemplateID: &w.template, Name: "deploy on push", Enabled: true, Preset: triggers.PresetGitHub,
		Mapping: triggers.Mapping{ExtraVars: map[string]string{"release": "$.ref"}}}
}

// TestNewServiceDefaultsRand covers Task 9 review Minor 5: Deps' doc
// comment promises Rand defaults when nil, but NewService did not default
// it, so a Deps built without one panicked on the first newID/newSecret
// call instead of using crypto/rand as documented.
func TestNewServiceDefaultsRand(t *testing.T) {
	w := newWorld(t)
	w.svc = triggers.NewService(triggers.Deps{
		Store: w.store, Templates: w.tmpls, Repos: w.repos, Box: fakeBox{}, PublicURL: "https://pulsix.example.test/",
		Now: func() time.Time { return time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC) },
	})
	if _, err := w.svc.Create(ctx, w.org, w.actor, w.templateInput()); err != nil {
		t.Fatalf("Create with a nil Rand = %v, want it to default to crypto/rand", err)
	}
}

func TestCreateReturnsTheSecretOnceAndSealsIt(t *testing.T) {
	w := newWorld(t)
	c, err := w.svc.Create(ctx, w.org, w.actor, w.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Secret) < 40 || c.URL != "https://pulsix.example.test/api/v1/hooks/jobs/"+c.Trigger.ID.String() {
		t.Fatalf("created = %+v", c)
	}
	sealed := string(w.store.secrets[c.Trigger.ID])
	if sealed != "sealed:trigger:"+w.org.String()+":"+c.Trigger.ID.String()+":"+c.Secret {
		t.Fatalf("sealed secret = %q (AAD trigger:<org>:<id>)", sealed)
	}
	if c.Trigger.RateLimit != (triggers.RateLimit{Count: 10, Per: "1m"}) || c.Trigger.OnActive != triggers.OnActiveQueue || w.store.fills != 1 {
		t.Fatalf("defaults = %+v, fills %d", c.Trigger, w.store.fills)
	}
	got, _ := w.svc.Get(ctx, w.org, c.Trigger.ID)
	if got.Auth() != triggers.AuthHMAC || got.SignatureHeader() != "X-Hub-Signature-256" {
		t.Fatalf("preset derivation = %s %s", got.Auth(), got.SignatureHeader())
	}
}

func TestCreateValidation(t *testing.T) {
	w := newWorld(t)
	other := uuid.Must(uuid.NewV7())
	cases := []struct {
		name   string
		mutate func(*triggers.Input)
	}{
		{"empty name", func(in *triggers.Input) { in.Name = "" }},
		{"unknown preset", func(in *triggers.Input) { in.Preset = "bitbucket" }},
		{"unknown template", func(in *triggers.Input) { in.TemplateID = &other }},
		{"template run with a repo", func(in *triggers.Input) { in.RepoID = &w.repo }},
		{"bad cidr", func(in *triggers.Input) { in.AllowCIDRs = []string{"10.0.0.0/33"} }},
		{"bad timestamp header", func(in *triggers.Input) { in.TimestampHeader = "X Bad" }},
		{"timestamp header on a preset that cannot use it", func(in *triggers.Input) { in.TimestampHeader = "X-Pulsix-Timestamp" }},
		{"rate limit count", func(in *triggers.Input) { in.RateLimit = triggers.RateLimit{Count: 0, Per: "1m"} }},
		{"rate limit per", func(in *triggers.Input) { in.RateLimit = triggers.RateLimit{Count: 5, Per: "48h"} }},
		{"on_active", func(in *triggers.Input) { in.OnActive = "wait" }},
		{"descendant path", func(in *triggers.Input) { in.Mapping.ExtraVars = map[string]string{"release": "$..ref"} }},
		{"secret target", func(in *triggers.Input) { in.Mapping.ExtraVars = map[string]string{"token": "$.t"} }},
		{"undeclared target", func(in *triggers.Input) { in.Mapping.ExtraVars = map[string]string{"colour": "$.c"} }},
		{"denylisted target", func(in *triggers.Input) { in.Mapping.ExtraVars = map[string]string{"ansible_connection": "$.c"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := w.templateInput()
			c.mutate(&in)
			if _, err := w.svc.Create(ctx, w.org, w.actor, in); !errors.Is(err, triggers.ErrInvalidInput) {
				t.Fatalf("Create = %v, want ErrInvalidInput", err)
			}
		})
	}
}

// TestTimestampHeaderOnlyForPulsix covers Task 9 review Minor 3: GitHub and
// Gitea never sign "<timestamp>.<body>", and GitLab's and bearer's token
// does not bind a timestamp either, so a timestamp header only means
// anything for the pulsix preset — every other preset must refuse it.
func TestTimestampHeaderOnlyForPulsix(t *testing.T) {
	w := newWorld(t)
	for _, p := range []triggers.Preset{triggers.PresetGitHub, triggers.PresetGitea, triggers.PresetGitLab, triggers.PresetBearer} {
		in := w.templateInput()
		in.Preset, in.TimestampHeader = p, "X-Pulsix-Timestamp"
		if _, err := w.svc.Create(ctx, w.org, w.actor, in); !errors.Is(err, triggers.ErrInvalidInput) {
			t.Fatalf("preset %s with a timestamp header: %v, want ErrInvalidInput", p, err)
		}
	}
	in := w.templateInput()
	in.Preset, in.TimestampHeader = triggers.PresetPulsix, "X-Pulsix-Timestamp"
	if _, err := w.svc.Create(ctx, w.org, w.actor, in); err != nil {
		t.Fatalf("pulsix with a timestamp header = %v, want no error", err)
	}
}

func TestRepoSyncTriggersTakeNoMapping(t *testing.T) {
	w := newWorld(t)
	in := triggers.Input{Kind: triggers.KindRepoSync, RepoID: &w.repo, Name: "sync on push", Enabled: true, Preset: triggers.PresetGitea}
	c, err := w.svc.Create(ctx, w.org, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(c.URL, "/api/v1/hooks/repos/"+c.Trigger.ID.String()) {
		t.Fatalf("url = %s", c.URL)
	}
	in.Name, in.Mapping = "with mapping", triggers.Mapping{Limit: "$.x"}
	if _, err := w.svc.Create(ctx, w.org, w.actor, in); !errors.Is(err, triggers.ErrInvalidInput) {
		t.Fatalf("repo_sync with mapping: %v", err)
	}
}

// TestDeliveriesClampsTheLimit covers Task 9 review Minor 6: Service.Deliveries
// passed its limit straight through to the store, which cast it to int32
// with a //nolint comment claiming the API clamps it — a business rule
// that belonged in the service, not a trusted assumption about callers.
func TestDeliveriesClampsTheLimit(t *testing.T) {
	w := newWorld(t)
	c, err := w.svc.Create(ctx, w.org, w.actor, w.templateInput())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want int }{
		{0, 1},
		{-5, 1},
		{5, 5},
		{500, 500},
		{100000, 500},
	}
	for _, tc := range cases {
		if _, err := w.svc.Deliveries(ctx, w.org, c.Trigger.ID, tc.in); err != nil {
			t.Fatal(err)
		}
		if w.store.lastLimit != tc.want {
			t.Fatalf("Deliveries(%d): store saw limit %d, want %d", tc.in, w.store.lastLimit, tc.want)
		}
	}
}

func TestRotateSecretAndUpdate(t *testing.T) {
	w := newWorld(t)
	c, _ := w.svc.Create(ctx, w.org, w.actor, w.templateInput())
	r, err := w.svc.RotateSecret(ctx, w.org, c.Trigger.ID)
	if err != nil || r.Secret == c.Secret || r.Secret == "" {
		t.Fatalf("rotate = %+v, %v", r, err)
	}
	in := w.templateInput()
	in.Enabled, in.AllowCIDRs = false, []string{"140.82.112.0/20"}
	up, err := w.svc.Update(ctx, w.org, c.Trigger.ID, c.Trigger.Revision, in)
	if err != nil || up.Enabled || len(up.AllowCIDRs) != 1 {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if _, err := w.svc.Update(ctx, w.org, c.Trigger.ID, c.Trigger.Revision, in); !errors.Is(err, triggers.ErrRevisionMismatch) {
		t.Fatalf("stale update: %v", err)
	}
	in.Kind, in.RepoID, in.TemplateID = triggers.KindRepoSync, &w.repo, nil
	if _, err := w.svc.Update(ctx, w.org, c.Trigger.ID, up.Revision, in); !errors.Is(err, triggers.ErrInvalidInput) {
		t.Fatalf("changing the kind: %v", err)
	}
}
