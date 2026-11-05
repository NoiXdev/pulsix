package jobs_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
)

// onlyHostFixture is one template whose scope resolves to a single zone
// holding hosts A (web-01) and B (web-02); C (web-03) is managed but outside
// the scope, U is unmanaged.
type onlyHostFixture struct {
	org, templateID, zone uuid.UUID
	a, b, c, u            uuid.UUID
	store                 *fakeStore
	res                   *fakeResolver
	svc                   *jobs.Service
}

func newOnlyHostFixture(t *testing.T, tmplLimit string) onlyHostFixture {
	t.Helper()
	f := onlyHostFixture{
		org: uuid.Must(uuid.NewV7()), templateID: uuid.Must(uuid.NewV7()), zone: uuid.Must(uuid.NewV7()),
		a: uuid.Must(uuid.NewV7()), b: uuid.Must(uuid.NewV7()), c: uuid.Must(uuid.NewV7()), u: uuid.Must(uuid.NewV7()),
		store: newFakeStore(),
	}
	tmpl := &fakeTemplates{byID: map[uuid.UUID]jobs.TemplateRow{
		f.templateID: {ID: f.templateID, OrgID: f.org, Scope: json.RawMessage(`{"group_id":null,"host_ids":[]}`), Limit: tmplLimit},
	}}
	f.res = &fakeResolver{targets: []jobs.ZoneTargets{{ZoneID: f.zone, HostIDs: []uuid.UUID{f.a, f.b}}}}
	hs := &fakeHosts{byID: map[uuid.UUID]hosts.Host{
		f.a: {ID: f.a, Name: "web-01", ZoneID: f.zone, Managed: true},
		f.b: {ID: f.b, Name: "web-02", ZoneID: f.zone, Managed: true},
		f.c: {ID: f.c, Name: "web-03", ZoneID: f.zone, Managed: true},
		f.u: {ID: f.u, Name: "web-04", ZoneID: f.zone, Managed: false},
	}}
	f.svc = newService(f.store, &fakeQueue{}, tmpl, f.res, hs)
	return f
}

func (f onlyHostFixture) run(host uuid.UUID, ov *jobs.RunOverrides) ([]jobs.Job, error) {
	actor := uuid.Must(uuid.NewV7())
	return f.svc.EnqueueRunWith(ctx, f.org, f.templateID, jobs.RunRequest{
		Trigger: jobs.TriggerManual, Actor: &actor, Overrides: ov, OnlyHost: &host,
	})
}

// sealedOverrides opens the job's sealed payload with the fake sealer.
func (f onlyHostFixture) sealedOverrides(t *testing.T, j jobs.Job) jobs.RunOverrides {
	t.Helper()
	var b64 string
	if err := json.Unmarshal(f.store.jobs[j.ID].payload, &b64); err != nil {
		t.Fatalf("payload of %s is not a JSON string: %v", j.ID, err)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := fakeSealer{}.OpenAAD(raw, jobs.JobAAD(f.org, j.ID))
	if err != nil {
		t.Fatalf("open payload of %s: %v", j.ID, err)
	}
	var ov jobs.RunOverrides
	if err := json.Unmarshal(plain, &ov); err != nil {
		t.Fatal(err)
	}
	return ov
}

// TestEnqueueRunWithOnlyHost pins a template run narrowed to one host
// (backlog 7.76): exactly one job targeting exactly that host, a sealed
// limit that names the host, and a refusal for a host the template would
// not have run on.
func TestEnqueueRunWithOnlyHost(t *testing.T) {
	assertOnlyA := func(t *testing.T, f onlyHostFixture, created []jobs.Job, wantLimit string) jobs.RunOverrides {
		t.Helper()
		if len(created) != 1 {
			t.Fatalf("jobs = %d, want 1", len(created))
		}
		j := created[0]
		if j.ZoneID != f.zone || len(j.HostIDs) != 1 || j.HostIDs[0] != f.a {
			t.Fatalf("job zone %s hosts %v, want zone %s hosts [%s]", j.ZoneID, j.HostIDs, f.zone, f.a)
		}
		ov := f.sealedOverrides(t, j)
		if ov.Limit == nil || *ov.Limit != wantLimit {
			t.Fatalf("sealed limit = %v, want %q", ov.Limit, wantLimit)
		}
		return ov
	}

	t.Run("no limit narrows to the host name", func(t *testing.T) {
		f := newOnlyHostFixture(t, "")
		created, err := f.run(f.a, nil)
		if err != nil {
			t.Fatalf("EnqueueRunWith: %v", err)
		}
		assertOnlyA(t, f, created, "web-01")
		if len(f.res.limits) != 1 || f.res.limits[0] != "" {
			t.Fatalf("resolver gated on %v, want the effective limit [\"\"]", f.res.limits)
		}
	})

	t.Run("template limit is intersected", func(t *testing.T) {
		f := newOnlyHostFixture(t, "web*")
		created, err := f.run(f.a, nil)
		if err != nil {
			t.Fatalf("EnqueueRunWith: %v", err)
		}
		assertOnlyA(t, f, created, "web*:&web-01")
		// The scope resolution gates on the effective limit, never on the
		// narrowed one (which the matcher cannot decide).
		if len(f.res.limits) != 1 || f.res.limits[0] != "web*" {
			t.Fatalf("resolver gated on %v, want [\"web*\"]", f.res.limits)
		}
	})

	t.Run("caller overrides survive", func(t *testing.T) {
		f := newOnlyHostFixture(t, "db*")
		limit := "web-0*"
		ov := &jobs.RunOverrides{Limit: &limit, ExtraVars: map[string]any{"release": "v2"}}
		created, err := f.run(f.a, ov)
		if err != nil {
			t.Fatalf("EnqueueRunWith: %v", err)
		}
		sealed := assertOnlyA(t, f, created, "web-0*:&web-01")
		if sealed.ExtraVars["release"] != "v2" {
			t.Fatalf("sealed extra vars = %v, want release=v2", sealed.ExtraVars)
		}
		if *ov.Limit != "web-0*" {
			t.Fatalf("caller's overrides were mutated: limit %q", *ov.Limit)
		}
	})

	t.Run("host outside the scope", func(t *testing.T) {
		f := newOnlyHostFixture(t, "")
		created, err := f.run(f.c, nil)
		if !errors.Is(err, jobs.ErrHostNotInScope) {
			t.Fatalf("err = %v, want ErrHostNotInScope", err)
		}
		if len(created) != 0 || len(f.store.jobs) != 0 {
			t.Fatalf("created %d jobs (stored %d), want none", len(created), len(f.store.jobs))
		}
	})

	t.Run("host excluded by a decidable limit", func(t *testing.T) {
		f := newOnlyHostFixture(t, "db*")
		if _, err := f.run(f.a, nil); !errors.Is(err, jobs.ErrHostNotInScope) {
			t.Fatalf("err = %v, want ErrHostNotInScope", err)
		}
		if len(f.store.jobs) != 0 {
			t.Fatalf("stored %d jobs, want none", len(f.store.jobs))
		}
	})

	t.Run("host in the excluded hosts", func(t *testing.T) {
		f := newOnlyHostFixture(t, "")
		actor := uuid.Must(uuid.NewV7())
		host := f.a
		_, err := f.svc.EnqueueRunWith(ctx, f.org, f.templateID, jobs.RunRequest{
			Trigger: jobs.TriggerManual, Actor: &actor, OnlyHost: &host, ExcludeHosts: []uuid.UUID{f.a},
		})
		if !errors.Is(err, jobs.ErrAllHostsExcluded) {
			t.Fatalf("err = %v, want ErrAllHostsExcluded", err)
		}
		if len(f.store.jobs) != 0 {
			t.Fatalf("stored %d jobs, want none", len(f.store.jobs))
		}
	})

	// The matcher decides on host names, so a group the host is in is
	// Ansible's to decide at run time (limit.go's fail-open contract): an
	// exclusion-only group limit never removes the narrowed host here.
	t.Run("host in the excluded hosts under an exclusion-only group limit", func(t *testing.T) {
		f := newOnlyHostFixture(t, "!dbservers")
		actor := uuid.Must(uuid.NewV7())
		host := f.a
		_, err := f.svc.EnqueueRunWith(ctx, f.org, f.templateID, jobs.RunRequest{
			Trigger: jobs.TriggerManual, Actor: &actor, OnlyHost: &host, ExcludeHosts: []uuid.UUID{f.a},
		})
		if !errors.Is(err, jobs.ErrAllHostsExcluded) {
			t.Fatalf("err = %v, want ErrAllHostsExcluded", err)
		}
		if len(f.store.jobs) != 0 {
			t.Fatalf("stored %d jobs, want none", len(f.store.jobs))
		}
	})

	t.Run("exclusion-only group limit the host is not in", func(t *testing.T) {
		f := newOnlyHostFixture(t, "!dbservers")
		created, err := f.run(f.a, nil)
		if err != nil {
			t.Fatalf("EnqueueRunWith: %v", err)
		}
		assertOnlyA(t, f, created, "!dbservers:&web-01")
	})

	t.Run("excluding another host still runs the narrowed host", func(t *testing.T) {
		f := newOnlyHostFixture(t, "")
		actor := uuid.Must(uuid.NewV7())
		host := f.a
		created, err := f.svc.EnqueueRunWith(ctx, f.org, f.templateID, jobs.RunRequest{
			Trigger: jobs.TriggerManual, Actor: &actor, OnlyHost: &host, ExcludeHosts: []uuid.UUID{f.b},
		})
		if err != nil {
			t.Fatalf("EnqueueRunWith: %v", err)
		}
		assertOnlyA(t, f, created, "web-01")
	})

	t.Run("scope that resolves to nothing", func(t *testing.T) {
		f := newOnlyHostFixture(t, "")
		f.res.err = jobs.ErrEmptyScope
		if _, err := f.run(f.a, nil); !errors.Is(err, jobs.ErrHostNotInScope) {
			t.Fatalf("err = %v, want ErrHostNotInScope", err)
		}
	})

	t.Run("unmanaged or unknown host", func(t *testing.T) {
		f := newOnlyHostFixture(t, "")
		for name, id := range map[string]uuid.UUID{"unmanaged": f.u, "unknown": uuid.Must(uuid.NewV7())} {
			if _, err := f.run(id, nil); !errors.Is(err, jobs.ErrNotFound) {
				t.Errorf("%s: err = %v, want ErrNotFound", name, err)
			}
		}
		if len(f.store.jobs) != 0 {
			t.Fatalf("stored %d jobs, want none", len(f.store.jobs))
		}
	})
}
