// Tests for the thin adapters that sit between app-owned services and the
// ports other packages declare (hosts -> agents/checks, checks ->
// alertroutes, repos/templates -> jobs). They run against the real stores
// through testdb, because what each adapter promises is a mapping — the
// right fields, the right sentinel, the organization boundary — that a fake
// would only restate.
package app

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/grpcsrv"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/rules"
	"github.com/noixdev/pulsix/internal/templates"
)

// seedOrg creates an organization row and returns its id.
func seedOrg(t *testing.T, d *db.DB, slug string) uuid.UUID {
	t.Helper()
	org, err := gen.New(d.Pool()).CreateOrganization(context.Background(), gen.CreateOrganizationParams{
		ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug,
	})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// TestAgentHostProvisioner pins the three provisioning calls the agents
// claim makes: GetHost is an existence check scoped to the organization
// (another organization's host is not found), CreateHost makes an unmanaged,
// unmonitored host in the token's zone and returns its id, and SetMonitored
// flips only the monitored capability.
func TestAgentHostProvisioner(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org := seedOrg(t, d, "app-agent-prov")
	other := seedOrg(t, d, "app-agent-prov-other")
	svc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	prov := NewAgentHostProvisioner(svc)

	zone, err := svc.CreateZone(ctx, org, "edge", "Edge")
	if err != nil {
		t.Fatal(err)
	}

	id, err := prov.CreateHost(ctx, org, zone.ID, "web-1")
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	h, err := svc.GetHost(ctx, org, id)
	if err != nil {
		t.Fatal(err)
	}
	if h.Name != "web-1" || h.ZoneID != zone.ID || h.Managed || h.Monitored {
		t.Fatalf("created host = %+v, want web-1 in the zone, unmanaged and unmonitored", h)
	}

	if err := prov.GetHost(ctx, org, id); err != nil {
		t.Fatalf("GetHost of the created host: %v", err)
	}
	if err := prov.GetHost(ctx, other, id); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("GetHost across organizations = %v, want hosts.ErrNotFound", err)
	}
	if err := prov.GetHost(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("GetHost of an unknown id = %v, want hosts.ErrNotFound", err)
	}

	// A pre-created, managed host keeps managed when enrollment flips
	// monitored.
	managed, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "db-1", ZoneID: zone.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, hid := range []uuid.UUID{id, managed.ID} {
		if err := prov.SetMonitored(ctx, org, hid); err != nil {
			t.Fatalf("SetMonitored: %v", err)
		}
	}
	if got, _ := svc.GetHost(ctx, org, id); !got.Monitored || got.Managed {
		t.Fatalf("enrolled host = %+v, want monitored and still unmanaged", got)
	}
	if got, _ := svc.GetHost(ctx, org, managed.ID); !got.Monitored || !got.Managed {
		t.Fatalf("pre-created managed host = %+v, want monitored and still managed", got)
	}
	if err := prov.SetMonitored(ctx, other, id); !errors.Is(err, hosts.ErrNotFound) {
		t.Fatalf("SetMonitored across organizations = %v, want hosts.ErrNotFound", err)
	}
	if _, err := prov.CreateHost(ctx, org, uuid.Must(uuid.NewV7()), "ghost"); err == nil {
		t.Fatal("CreateHost into an unknown zone must fail")
	}
}

// TestChecksHostAdaptersAndAlertRouteGroups drives the checks service
// through its two host ports (selector matcher, monitored roster) and the
// alert follower's group lookup, with real hosts: the selector resolves to
// exactly the hosts it matches in this organization, the roster holds only
// monitored hosts, a bad selector is checks.ErrInvalidInput, and the alert
// routes see every group the host belongs to — unmanaged members included.
func TestChecksHostAdaptersAndAlertRouteGroups(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org := seedOrg(t, d, "app-checks-ports")
	other := seedOrg(t, d, "app-checks-ports-other")
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	matcher := NewChecksHostMatcher(hostsSvc)
	roster := NewChecksHostRoster(hostsSvc)

	zone, err := hostsSvc.CreateZone(ctx, org, "z", "Z")
	if err != nil {
		t.Fatal(err)
	}
	otherZone, err := hostsSvc.CreateZone(ctx, other, "z", "Z")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(org, zone uuid.UUID, name string, monitored bool, env string) uuid.UUID {
		t.Helper()
		h, err := hostsSvc.CreateHost(ctx, org, hosts.HostInput{Name: name, ZoneID: zone, Monitored: monitored})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := hostsSvc.SetTags(ctx, org, h.ID, []hosts.Tag{{Key: "env", Value: env}}, nil); err != nil {
			t.Fatal(err)
		}
		return h.ID
	}
	prodMonitored := mk(org, zone.ID, "prod-1", true, "prod")
	prodUnmonitored := mk(org, zone.ID, "prod-2", false, "prod")
	stagingMonitored := mk(org, zone.ID, "stg-1", true, "staging")
	foreignProd := mk(other, otherZone.ID, "foreign-prod", true, "prod")

	got, err := matcher.MatchingHosts(ctx, org, "env=prod")
	if err != nil {
		t.Fatalf("MatchingHosts: %v", err)
	}
	slices.SortFunc(got, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	want := []uuid.UUID{prodMonitored, prodUnmonitored}
	slices.SortFunc(want, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	if !slices.Equal(got, want) {
		t.Fatalf("MatchingHosts(env=prod) = %v, want both prod hosts of this organization %v", got, want)
	}
	if slices.Contains(got, foreignProd) {
		t.Fatal("MatchingHosts returned another organization's host")
	}
	none, err := matcher.MatchingHosts(ctx, org, "env=nowhere")
	if err != nil || len(none) != 0 {
		t.Fatalf("MatchingHosts(env=nowhere) = %v, %v, want empty", none, err)
	}
	if _, err := matcher.MatchingHosts(ctx, org, "env ="); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("MatchingHosts(bad selector) = %v, want checks.ErrInvalidInput", err)
	}

	monitored, err := roster.MonitoredHosts(ctx, org)
	if err != nil {
		t.Fatalf("MonitoredHosts: %v", err)
	}
	if len(monitored) != 2 || !slices.Contains(monitored, prodMonitored) || !slices.Contains(monitored, stagingMonitored) {
		t.Fatalf("MonitoredHosts = %v, want exactly the two monitored hosts of this organization", monitored)
	}
	empty, err := roster.MonitoredHosts(ctx, uuid.Must(uuid.NewV7()))
	if err != nil || len(empty) != 0 {
		t.Fatalf("MonitoredHosts of an organization without hosts = %v, %v, want empty", empty, err)
	}

	// Alert routing: groups are a selector over the hosts, and the lookup
	// answers with group ids for managed and unmanaged members alike.
	checksSvc := checks.NewService(checks.NewSQLStore(d), checks.WithMatcher(matcher), checks.WithHostRoster(roster))
	prodGroup, err := checksSvc.CreateGroup(ctx, org, checks.GroupInput{Name: "prod", Selector: "env=prod", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checksSvc.CreateGroup(ctx, org, checks.GroupInput{Name: "staging", Selector: "env=staging", Priority: 2}); err != nil {
		t.Fatal(err)
	}
	routes := NewAlertRouteGroups(checksSvc)
	ids, err := routes.GroupsForHost(ctx, org, prodUnmonitored)
	if err != nil {
		t.Fatalf("GroupsForHost: %v", err)
	}
	if len(ids) != 1 || ids[0] != prodGroup.ID {
		t.Fatalf("GroupsForHost(unmanaged prod host) = %v, want only the prod group %v", ids, prodGroup.ID)
	}
	if ids, err := routes.GroupsForHost(ctx, org, uuid.Must(uuid.NewV7())); err != nil || len(ids) != 0 {
		t.Fatalf("GroupsForHost(unknown host) = %v, %v, want empty", ids, err)
	}
}

// TestAlertRouteGroupsPropagatesFailure: a resolver outage is returned, not
// softened into "the host is in no group", which would quietly drop every
// group-scoped route's alerts.
func TestAlertRouteGroupsPropagatesFailure(t *testing.T) {
	boom := errors.New("resolver down")
	got, err := alertRouteGroups{checks: &fakeCheckResolver{groupsErr: boom}}.GroupsForHost(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	if !errors.Is(err, boom) || got != nil {
		t.Fatalf("GroupsForHost = %v, %v, want nil and the resolver's error", got, err)
	}
}

// TestItemCheckKeys: the key set is the distinct check keys of the rule
// catalogue — one entry per key however many metrics share it, and every
// catalogue entry's key is present.
func TestItemCheckKeys(t *testing.T) {
	keys := ItemCheckKeys()
	entries := rules.Catalogue()
	if len(entries) == 0 || len(keys) == 0 {
		t.Fatalf("catalogue has %d entries, key set %d", len(entries), len(keys))
	}
	distinct := map[string]struct{}{}
	for _, e := range entries {
		if _, ok := keys[e.CheckKey]; !ok {
			t.Fatalf("check key %q missing from the set", e.CheckKey)
		}
		distinct[e.CheckKey] = struct{}{}
	}
	if len(keys) != len(distinct) {
		t.Fatalf("key set has %d keys, catalogue has %d distinct", len(keys), len(distinct))
	}
}

// TestVaultIdentityConversions pins the two conversion directions between
// jobs.VaultIdentity and repos.VaultIdentity (every field carried over,
// order kept, an empty input yields an empty — not nil — slice) and the
// resolver built on them: the union by label, sorted, the template winning
// a collision, FromTemplate set by origin rather than by what the caller
// claimed.
func TestVaultIdentityConversions(t *testing.T) {
	a, b, c := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	repoIDs := []repos.VaultIdentity{{CredentialID: a, Label: "x", FromTemplate: true}, {CredentialID: b, Label: "y"}}
	asJobs := toJobsVaultIdentities(repoIDs)
	wantJobs := []jobs.VaultIdentity{{CredentialID: a, Label: "x", FromTemplate: true}, {CredentialID: b, Label: "y"}}
	if !slices.Equal(asJobs, wantJobs) {
		t.Fatalf("toJobsVaultIdentities = %+v, want %+v", asJobs, wantJobs)
	}
	if back := toRepoVaultIdentities(asJobs); !slices.Equal(back, repoIDs) {
		t.Fatalf("toRepoVaultIdentities round trip = %+v, want %+v", back, repoIDs)
	}
	if got := toJobsVaultIdentities(nil); got == nil || len(got) != 0 {
		t.Fatalf("toJobsVaultIdentities(nil) = %#v, want an empty slice", got)
	}
	if got := toRepoVaultIdentities(nil); got == nil || len(got) != 0 {
		t.Fatalf("toRepoVaultIdentities(nil) = %#v, want an empty slice", got)
	}

	got := vaultResolver{}.ResolveVaultIdentities(
		[]jobs.VaultIdentity{{CredentialID: a, Label: "shared", FromTemplate: true}, {CredentialID: b, Label: "repo-only"}},
		[]jobs.VaultIdentity{{CredentialID: c, Label: "shared"}},
	)
	want := []jobs.VaultIdentity{
		{CredentialID: b, Label: "repo-only"},
		{CredentialID: c, Label: "shared", FromTemplate: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ResolveVaultIdentities = %+v, want %+v", got, want)
	}
	if got := (vaultResolver{}).ResolveVaultIdentities(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("ResolveVaultIdentities(nil, nil) = %#v, want an empty slice", got)
	}
}

// TestRepoRefSourceGet pins the repository reference the claim path reads:
// url, branch, subpath, the git credential id and the labelled vault
// identities (converted, FromTemplate false), all from the organization's
// own registry; an unknown id or another organization's repo is
// repos.ErrNotFound.
func TestRepoRefSourceGet(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org := seedOrg(t, d, "app-repo-ref")
	other := seedOrg(t, d, "app-repo-ref-other")
	creds := credentials.NewService(credentials.NewSQLStore(d), crypto.NewBox([32]byte{4, 2}), time.Now, rand.Reader)
	vault, err := creds.Create(ctx, org, credentials.CreateInput{Name: "vault-main", Kind: credentials.KindVaultPassword, Material: credentials.Material{Password: "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	git, err := creds.Create(ctx, org, credentials.CreateInput{Name: "git-token", Kind: credentials.KindGitToken, Material: credentials.Material{Token: "tok"}})
	if err != nil {
		t.Fatal(err)
	}
	svc := repos.NewService(repos.NewSQLStore(d), creds, nil)
	repo, err := svc.Create(ctx, org, repos.CreateInput{
		Name: "infra", URL: "https://git.example.com/infra.git", Branch: "release", Subpath: "ansible",
		CredentialID:    &git.ID,
		VaultIdentities: []repos.VaultIdentity{{CredentialID: vault.ID, Label: "prod"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	src := repoRefSource{s: svc}
	var _ jobs.RepoSource = src
	ref, err := src.Get(ctx, org, repo.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ref.ID != repo.ID || ref.URL != "https://git.example.com/infra.git" || ref.Branch != "release" || ref.Subpath != "ansible" {
		t.Fatalf("ref = %+v", ref)
	}
	if ref.CredentialID == nil || *ref.CredentialID != git.ID {
		t.Fatalf("ref.CredentialID = %v, want %v", ref.CredentialID, git.ID)
	}
	wantIDs := []jobs.VaultIdentity{{CredentialID: vault.ID, Label: "prod"}}
	if !slices.Equal(ref.VaultIdentities, wantIDs) {
		t.Fatalf("ref.VaultIdentities = %+v, want %+v", ref.VaultIdentities, wantIDs)
	}

	if _, err := src.Get(ctx, other, repo.ID); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("Get across organizations = %v, want repos.ErrNotFound", err)
	}
	if _, err := src.Get(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, repos.ErrNotFound) {
		t.Fatalf("Get of an unknown id = %v, want repos.ErrNotFound", err)
	}
}

// TestRepoIDListerRepoIDs: the gRPC repository listing (backlog 7.95)
// names every repository of the organization and none of another's; an
// organization without repositories is an empty list.
func TestRepoIDListerRepoIDs(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org := seedOrg(t, d, "app-repo-ids")
	other := seedOrg(t, d, "app-repo-ids-other")
	empty := seedOrg(t, d, "app-repo-ids-empty")
	svc := repos.NewService(repos.NewSQLStore(d), nil, nil)
	var want []uuid.UUID
	for _, name := range []string{"infra", "apps"} {
		r, err := svc.Create(ctx, org, repos.CreateInput{Name: name, URL: "https://git.example.com/" + name + ".git", Branch: "main"})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, r.ID)
	}
	if _, err := svc.Create(ctx, other, repos.CreateInput{Name: "infra", URL: "https://git.example.com/x.git", Branch: "main"}); err != nil {
		t.Fatal(err)
	}

	lister := repoIDLister{s: svc}
	var _ grpcsrv.RepoLister = lister
	got, err := lister.RepoIDs(ctx, org)
	if err != nil {
		t.Fatalf("RepoIDs: %v", err)
	}
	slices.SortFunc(got, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	slices.SortFunc(want, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	if !slices.Equal(got, want) {
		t.Fatalf("RepoIDs = %v, want %v", got, want)
	}
	got, err = lister.RepoIDs(ctx, empty)
	if err != nil || len(got) != 0 {
		t.Fatalf("RepoIDs of an organization without repositories = %v, %v; want none", got, err)
	}
}

// TestTemplateLookupMapping pins what TemplateLookup copies into the
// queue's row: ids, the inline playbook text (and empty repo/path), the
// scope and limit, plain extra vars, the template's labelled vault
// credentials (FromTemplate true) and the schema; plus the not-found
// mapping — an unknown template and another organization's template are
// both jobs.ErrNotFound, never the stores' own sentinel.
func TestTemplateLookupMapping(t *testing.T) {
	ctx := context.Background()
	d := testdb.Open(t)
	org := seedOrg(t, d, "app-tmpl-map")
	other := seedOrg(t, d, "app-tmpl-map-other")
	scoped := db.WithOrgScope(ctx, org)
	box := crypto.NewBox([32]byte{9, 9})
	creds := credentials.NewService(credentials.NewSQLStore(d), box, time.Now, rand.Reader)
	vault, err := creds.Create(ctx, org, credentials.CreateInput{Name: "tmpl-vault", Kind: credentials.KindVaultPassword, Material: credentials.Material{Password: "pw"}})
	if err != nil {
		t.Fatal(err)
	}

	pbStore, tStore := playbooks.NewSQLStore(d), templates.NewSQLStore(d)
	inline := "- hosts: all\n  tasks: []"
	pb, err := pbStore.Create(scoped, playbooks.Playbook{ID: uuid.Must(uuid.NewV7()), OrgID: org, Inline: &inline, Source: playbooks.SourceEditor})
	if err != nil {
		t.Fatal(err)
	}
	limit := "web*"
	tmpl, err := tStore.Create(scoped, templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: pb.ID, Name: "deploy", Limit: limit,
		ExtraVars:       map[string]any{"region": "eu"},
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{Name: "region", Type: schema.TypeString}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tStore.ReplaceVaultCredentials(scoped, org, tmpl.ID, []templates.VaultCredential{{CredentialID: vault.ID, Label: "prod"}}); err != nil {
		t.Fatal(err)
	}

	lookup := NewTemplateLookup(tStore, pbStore, box)
	row, err := lookup.TemplateLookup(scoped, org, tmpl.ID)
	if err != nil {
		t.Fatalf("TemplateLookup: %v", err)
	}
	if row.ID != tmpl.ID || row.OrgID != org || row.PlaybookID != pb.ID {
		t.Fatalf("row ids = %v/%v/%v", row.ID, row.OrgID, row.PlaybookID)
	}
	if row.PlaybookInline != inline || row.PlaybookPath != "" || row.PlaybookRepo != nil {
		t.Fatalf("playbook = repo %v path %q inline %q, want the inline text only", row.PlaybookRepo, row.PlaybookPath, row.PlaybookInline)
	}
	if row.Limit != limit {
		t.Fatalf("row.Limit = %q, want %q", row.Limit, limit)
	}
	if string(row.ExtraVars) != `{"region":"eu"}` {
		t.Fatalf("row.ExtraVars = %s", row.ExtraVars)
	}
	if len(row.ExtraVarsSchema) == 0 {
		t.Fatal("row.ExtraVarsSchema is empty, want the template's schema")
	}
	wantIDs := []jobs.VaultIdentity{{CredentialID: vault.ID, Label: "prod", FromTemplate: true}}
	if !slices.Equal(row.VaultIdentities, wantIDs) {
		t.Fatalf("row.VaultIdentities = %+v, want %+v", row.VaultIdentities, wantIDs)
	}

	if _, err := lookup.TemplateLookup(scoped, org, uuid.Must(uuid.NewV7())); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("unknown template = %v, want jobs.ErrNotFound", err)
	}
	if _, err := lookup.TemplateLookup(db.WithOrgScope(ctx, other), other, tmpl.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("another organization's template = %v, want jobs.ErrNotFound", err)
	}
}

// TestOpenExtraVarsOpensNestedSecrets: a sealed value is opened wherever it
// sits — inside a nested map or a list — and everything else passes through
// untouched. A value sealed for a different template fails the whole open.
func TestOpenExtraVarsOpensNestedSecrets(t *testing.T) {
	box := crypto.NewBox([32]byte{3, 1, 4})
	orgID, tmplID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	aad := templates.ExtraVarsAAD(orgID, tmplID)
	seal := func(s string, aad []byte) any {
		t.Helper()
		ct, err := box.SealAAD([]byte(s), aad)
		if err != nil {
			t.Fatal(err)
		}
		return schema.Sealed(ct)
	}
	lookup := templateLookup{box: box}

	got, secrets, err := lookup.openExtraVars(templates.Template{ID: tmplID, OrgID: orgID, ExtraVars: map[string]any{
		"plain": "eu",
		"db":    map[string]any{"password": seal("nested-pw", aad), "port": float64(5432)},
		"keys":  []any{seal("list-secret", aad), "literal"},
	}})
	if err != nil {
		t.Fatalf("openExtraVars: %v", err)
	}
	db, _ := got["db"].(map[string]any)
	keys, _ := got["keys"].([]any)
	if got["plain"] != "eu" || db["password"] != "nested-pw" || db["port"] != float64(5432) ||
		len(keys) != 2 || keys[0] != "list-secret" || keys[1] != "literal" {
		t.Fatalf("opened vars = %#v", got)
	}
	if !slices.Equal(secrets, []string{"list-secret", "nested-pw"}) {
		t.Fatalf("opened secrets = %v, want every opened value, sorted", secrets)
	}

	for name, vars := range map[string]map[string]any{
		"nested map": {"db": map[string]any{"password": seal("x", []byte("other-aad"))}},
		"list":       {"keys": []any{seal("x", []byte("other-aad"))}},
	} {
		if _, _, err := lookup.openExtraVars(templates.Template{ID: tmplID, OrgID: orgID, ExtraVars: vars}); err == nil {
			t.Fatalf("%s: a ciphertext sealed for another template must not open", name)
		}
	}
}

// TestNotFoundMapsOnlyTheStoreSentinels: both stores' not-found errors
// become jobs.ErrNotFound (message kept), nil stays nil, and any other error
// passes through unchanged.
func TestNotFoundMapsOnlyTheStoreSentinels(t *testing.T) {
	for name, in := range map[string]error{"templates": templates.ErrNotFound, "playbooks": playbooks.ErrNotFound} {
		if err := notFound(in); !errors.Is(err, jobs.ErrNotFound) {
			t.Fatalf("%s: notFound = %v, want jobs.ErrNotFound", name, err)
		}
	}
	if err := notFound(nil); err != nil {
		t.Fatalf("notFound(nil) = %v", err)
	}
	boom := errors.New("db down")
	if err := notFound(boom); err != boom || errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("notFound(other) = %v, want it unchanged", err)
	}
}

// TestBuildOptionsApply: WithScheduleClock and WithAuthReaperInterval set
// exactly their own field on buildOptions and nothing else.
func TestBuildOptionsApply(t *testing.T) {
	fixed := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	var o buildOptions
	for _, opt := range []Option{WithScheduleClock(func() time.Time { return fixed }), WithAuthReaperInterval(time.Second)} {
		opt(&o)
	}
	if o.scheduleNow == nil || !o.scheduleNow().Equal(fixed) {
		t.Fatal("scheduleNow is not the injected clock")
	}
	if o.authReaperInterval != time.Second {
		t.Fatalf("authReaperInterval = %v, want 1s", o.authReaperInterval)
	}
}
