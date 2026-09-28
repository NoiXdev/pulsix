package checks_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

// uuidPtrEqual compares two optional ids the way the unique
// (org_id, binding_kind, group_id, host_id, key) constraint does: NULL
// (nil) matches NULL, never "distinct from everything else" — the same
// NULLS NOT DISTINCT semantics migration 00043 relies on.
func uuidPtrEqual(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// defKeyTaken mirrors the real store's unique constraint: same org, same
// binding_kind, same group/host (NULLs included), same key.
func (f *fakeStore) defKeyTaken(orgID uuid.UUID, bindingKind checks.BindingKind, groupID, hostID *uuid.UUID, key string, exceptID uuid.UUID) bool {
	for _, d := range f.defsByID {
		if d.ID == exceptID {
			continue
		}
		if d.OrgID == orgID && d.BindingKind == bindingKind && d.Key == key &&
			uuidPtrEqual(d.GroupID, groupID) && uuidPtrEqual(d.HostID, hostID) {
			return true
		}
	}
	return false
}

func (f *fakeStore) CreateDefinition(_ context.Context, d checks.Definition) (checks.Definition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return checks.Definition{}, f.err
	}
	if f.defKeyTaken(d.OrgID, d.BindingKind, d.GroupID, d.HostID, d.Key, d.ID) {
		return checks.Definition{}, checks.ErrKeyTaken
	}
	now := f.now()
	d.CreatedAt, d.UpdatedAt = now, now
	f.defsByID[d.ID] = d
	return d, nil
}

func (f *fakeStore) GetDefinition(_ context.Context, orgID, id uuid.UUID) (checks.Definition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return checks.Definition{}, f.err
	}
	d, ok := f.defsByID[id]
	if !ok || d.OrgID != orgID {
		return checks.Definition{}, checks.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) ListDefinitions(_ context.Context, orgID uuid.UUID) ([]checks.Definition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []checks.Definition
	for _, d := range f.defsByID {
		if d.OrgID == orgID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateDefinition(_ context.Context, d checks.Definition) (checks.Definition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return checks.Definition{}, f.err
	}
	cur, ok := f.defsByID[d.ID]
	if !ok || cur.OrgID != d.OrgID {
		return checks.Definition{}, checks.ErrNotFound
	}
	if f.defKeyTaken(d.OrgID, d.BindingKind, d.GroupID, d.HostID, d.Key, d.ID) {
		return checks.Definition{}, checks.ErrKeyTaken
	}
	d.CreatedAt, d.UpdatedAt = cur.CreatedAt, f.now()
	f.defsByID[d.ID] = d
	return d, nil
}

func (f *fakeStore) DeleteDefinition(_ context.Context, orgID, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.defsByID[id]
	if !ok || d.OrgID != orgID {
		return checks.ErrNotFound
	}
	delete(f.defsByID, id)
	return nil
}

// TestCreateDefinitionValidation is the validation table the brief asks
// for: every rule normalizeDefinition enforces, both the rejections and
// the defaults (key defaulting to the plugin name).
func TestCreateDefinitionValidation(t *testing.T) {
	someGroup, someHost := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	cases := []struct {
		name    string
		in      checks.DefinitionInput
		wantErr bool
	}{
		{
			name: "valid cpu, key defaults to plugin",
			in:   checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg},
		},
		{
			name: "valid custom key",
			in:   checks.DefinitionInput{Key: "disk-var", Plugin: checks.PluginDisk, Interval: time.Minute, BindingKind: checks.BindingOrg},
		},
		{
			name:    "unknown plugin refused",
			in:      checks.DefinitionInput{Plugin: "ping", Interval: time.Minute, BindingKind: checks.BindingOrg},
			wantErr: true,
		},
		{
			name:    "empty plugin refused",
			in:      checks.DefinitionInput{Interval: time.Minute, BindingKind: checks.BindingOrg},
			wantErr: true,
		},
		{
			name:    "key with uppercase refused",
			in:      checks.DefinitionInput{Key: "CPU", Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg},
			wantErr: true,
		},
		{
			name:    "key starting with a hyphen refused",
			in:      checks.DefinitionInput{Key: "-cpu", Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg},
			wantErr: true,
		},
		{
			name:    "key over 63 characters refused",
			in:      checks.DefinitionInput{Key: "a234567890123456789012345678901234567890123456789012345678901234", Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg},
			wantErr: true,
		},
		{
			name:    "interval below 10s refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: 9 * time.Second, BindingKind: checks.BindingOrg},
			wantErr: true,
		},
		{
			name: "interval at the 10s floor accepted",
			in:   checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: 10 * time.Second, BindingKind: checks.BindingOrg},
		},
		{
			name:    "interval above 24h refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: 24*time.Hour + time.Second, BindingKind: checks.BindingOrg},
			wantErr: true,
		},
		{
			name: "interval at the 24h ceiling accepted",
			in:   checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: 24 * time.Hour, BindingKind: checks.BindingOrg},
		},
		{
			name:    "cpu with parameters refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg, Params: map[string]any{"anything": true}},
			wantErr: true,
		},
		{
			name:    "memory with parameters refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginMemory, Interval: time.Minute, BindingKind: checks.BindingOrg, Params: map[string]any{"anything": true}},
			wantErr: true,
		},
		{
			name: "disk with no parameters accepted (mounts is optional)",
			in:   checks.DefinitionInput{Plugin: checks.PluginDisk, Interval: time.Minute, BindingKind: checks.BindingOrg},
		},
		{
			name: "disk with a mounts allow-list accepted",
			in:   checks.DefinitionInput{Plugin: checks.PluginDisk, Interval: time.Minute, BindingKind: checks.BindingOrg, Params: map[string]any{"mounts": []string{"/", "/var"}}},
		},
		{
			name:    "disk with an unknown parameter refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginDisk, Interval: time.Minute, BindingKind: checks.BindingOrg, Params: map[string]any{"typo": []string{"/"}}},
			wantErr: true,
		},
		{
			name:    "disk mounts with an empty entry refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginDisk, Interval: time.Minute, BindingKind: checks.BindingOrg, Params: map[string]any{"mounts": []string{""}}},
			wantErr: true,
		},
		{
			name:    "service without units refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginService, Interval: time.Minute, BindingKind: checks.BindingOrg},
			wantErr: true,
		},
		{
			name:    "service with an empty units list refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginService, Interval: time.Minute, BindingKind: checks.BindingOrg, Params: map[string]any{"units": []string{}}},
			wantErr: true,
		},
		{
			name: "service with units, as []any the way JSON decodes it, accepted",
			in:   checks.DefinitionInput{Plugin: checks.PluginService, Interval: time.Minute, BindingKind: checks.BindingOrg, Params: map[string]any{"units": []any{"sshd", "cron"}}},
		},
		{
			name:    "service units with a non-string entry refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginService, Interval: time.Minute, BindingKind: checks.BindingOrg, Params: map[string]any{"units": []any{"sshd", 5}}},
			wantErr: true,
		},
		{
			name:    "org binding with a group set refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg, GroupID: &someGroup},
			wantErr: true,
		},
		{
			name: "group binding with a group set accepted",
			in:   checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingGroup, GroupID: &someGroup},
		},
		{
			name:    "group binding without a group refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingGroup},
			wantErr: true,
		},
		{
			name:    "group binding with both group and host set refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingGroup, GroupID: &someGroup, HostID: &someHost},
			wantErr: true,
		},
		{
			name: "host binding with a host set accepted",
			in:   checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingHost, HostID: &someHost},
		},
		{
			name:    "host binding without a host refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingHost},
			wantErr: true,
		},
		{
			name:    "unknown binding_kind refused",
			in:      checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingKind("cluster")},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := checks.NewService(newFakeStore())
			d, err := svc.CreateDefinition(ctx, uuid.Must(uuid.NewV7()), tc.in)
			if tc.wantErr {
				if !errors.Is(err, checks.ErrInvalidInput) {
					t.Fatalf("err = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			wantKey := tc.in.Key
			if wantKey == "" {
				wantKey = tc.in.Plugin
			}
			if d.Key != wantKey {
				t.Fatalf("key = %q, want %q", d.Key, wantKey)
			}
		})
	}
}

func TestCreateDefinitionStampsCustomOrigin(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	org := uuid.Must(uuid.NewV7())
	d, err := svc.CreateDefinition(ctx, org, checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg})
	if err != nil {
		t.Fatal(err)
	}
	if d.Origin != "custom" {
		t.Fatalf("origin = %q, want custom", d.Origin)
	}
	if d.ID == uuid.Nil || d.OrgID != org {
		t.Fatalf("d = %+v", d)
	}
}

func TestCreateDefinitionKeyTakenAtSameScope(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	org := uuid.Must(uuid.NewV7())
	in := checks.DefinitionInput{Key: "cpu", Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg}
	if _, err := svc.CreateDefinition(ctx, org, in); err != nil {
		t.Fatal(err)
	}
	_, err := svc.CreateDefinition(ctx, org, in)
	if !errors.Is(err, checks.ErrKeyTaken) {
		t.Fatalf("err = %v, want ErrKeyTaken", err)
	}

	// A different organization may reuse the same key.
	other := uuid.Must(uuid.NewV7())
	if _, err := svc.CreateDefinition(ctx, other, in); err != nil {
		t.Fatalf("a key taken in another org must not collide: %v", err)
	}
}

func TestUpdateDefinitionReplacesMutableFields(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	org := uuid.Must(uuid.NewV7())
	d, err := svc.CreateDefinition(ctx, org, checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.UpdateDefinition(ctx, org, d.ID, checks.DefinitionInput{
		Key: "cpu", Plugin: checks.PluginCPU, Interval: 5 * time.Minute, Enabled: false, BindingKind: checks.BindingOrg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Interval != 5*time.Minute || updated.Enabled {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.Origin != "custom" {
		t.Fatalf("origin must survive unchanged, got %q", updated.Origin)
	}
}

func TestUpdateDefinitionUnknownID(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	org := uuid.Must(uuid.NewV7())
	_, err := svc.UpdateDefinition(ctx, org, uuid.Must(uuid.NewV7()), checks.DefinitionInput{
		Key: "cpu", Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg,
	})
	if !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestUpdateDefinitionRefusesABlankKey pins the difference between a create
// and a full-replace update: a create may omit the key and take the plugin
// name, an update may not, because there the default is a RENAME of an
// existing definition — and the key is the identifier the resolver's
// override precedence keys on (task-10 review round 2, finding 1).
//
// Whitespace counts as blank: an HTML `required` attribute is satisfied by a
// single space, so the browser is no guard at all and this is the only place
// the rule can actually live.
func TestUpdateDefinitionRefusesABlankKey(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())
	d, err := svc.CreateDefinition(ctx, org, checks.DefinitionInput{
		Key: "disk-data", Plugin: checks.PluginDisk, Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{"omitted": "", "whitespace": "   ", "tab": "\t"} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.UpdateDefinition(ctx, org, d.ID, checks.DefinitionInput{
				Key: key, Plugin: checks.PluginDisk, Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg,
			})
			if !errors.Is(err, checks.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			got, err := svc.GetDefinition(ctx, org, d.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Key != "disk-data" {
				t.Fatalf("key = %q, want it untouched at %q", got.Key, "disk-data")
			}
		})
	}
	// A create in the same organization still defaults to the plugin name.
	created, err := svc.CreateDefinition(ctx, org, checks.DefinitionInput{
		Plugin: checks.PluginMemory, Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg,
	})
	if err != nil || created.Key != checks.PluginMemory {
		t.Fatalf("create with no key = %+v, %v; want key %q", created, err, checks.PluginMemory)
	}
}

// TestUpdateDefinitionRefusesChangingASeededRowsKeyOrPlugin pins the one
// rule origin = "seed" enforces: the operator's own row (created by
// CreateDefinition, origin "custom") has no such restriction.
func TestUpdateDefinitionRefusesChangingASeededRowsKeyOrPlugin(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())
	if err := svc.SeedForOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	defs, err := svc.ListDefinitions(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	var cpu checks.Definition
	for _, d := range defs {
		if d.Key == "cpu" {
			cpu = d
		}
	}
	if cpu.ID == uuid.Nil {
		t.Fatal("seed set must contain a cpu definition")
	}

	// Changing the key is refused.
	_, err = svc.UpdateDefinition(ctx, org, cpu.ID, checks.DefinitionInput{
		Key: "cpu-2", Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg,
	})
	if !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("changing a seeded row's key: err = %v, want ErrInvalidInput", err)
	}
	// Changing the plugin is refused.
	_, err = svc.UpdateDefinition(ctx, org, cpu.ID, checks.DefinitionInput{
		Key: "cpu", Plugin: checks.PluginMemory, Interval: time.Minute, BindingKind: checks.BindingOrg,
	})
	if !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("changing a seeded row's plugin: err = %v, want ErrInvalidInput", err)
	}
	// Disabling it and changing its interval/parameters is allowed.
	updated, err := svc.UpdateDefinition(ctx, org, cpu.ID, checks.DefinitionInput{
		Key: "cpu", Plugin: checks.PluginCPU, Interval: 2 * time.Minute, Enabled: false, BindingKind: checks.BindingOrg,
	})
	if err != nil {
		t.Fatalf("disabling/retuning a seeded row must be allowed: %v", err)
	}
	if updated.Enabled || updated.Interval != 2*time.Minute || updated.Origin != "seed" {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestDeleteDefinition(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	org := uuid.Must(uuid.NewV7())
	d, err := svc.CreateDefinition(ctx, org, checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteDefinition(ctx, org, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteDefinition(ctx, org, d.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("deleting twice = %v, want ErrNotFound", err)
	}
}

func TestListDefinitionsIsOrgScoped(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	orgA, orgB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := svc.CreateDefinition(ctx, orgA, checks.DefinitionInput{Plugin: checks.PluginCPU, Interval: time.Minute, BindingKind: checks.BindingOrg}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateDefinition(ctx, orgB, checks.DefinitionInput{Plugin: checks.PluginMemory, Interval: time.Minute, BindingKind: checks.BindingOrg}); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListDefinitions(ctx, orgA)
	if err != nil || len(list) != 1 || list[0].Plugin != checks.PluginCPU {
		t.Fatalf("list = %+v, %v, want only orgA's definition", list, err)
	}
}

// TestSeedForOrgWritesTheLinuxBaseSet pins SeedDefinitions' own contract:
// four rows, one per plugin, every one origin = "seed" and bound to the
// organization.
func TestSeedForOrgWritesTheLinuxBaseSet(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	org := uuid.Must(uuid.NewV7())
	if err := svc.SeedForOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	defs, err := svc.ListDefinitions(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 4 {
		t.Fatalf("len(defs) = %d, want 4", len(defs))
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if d.Origin != "seed" {
			t.Fatalf("definition %q origin = %q, want seed", d.Key, d.Origin)
		}
		if d.BindingKind != checks.BindingOrg || d.OrgID != org {
			t.Fatalf("definition %q = %+v, want org-bound to %v", d.Key, d, org)
		}
		seen[d.Key] = true
	}
	for _, want := range []string{"cpu", "memory", "disk", "service"} {
		if !seen[want] {
			t.Fatalf("seed set missing %q, got %v", want, defs)
		}
	}
}

func TestSeedDefinitionsMatchesLinuxBase(t *testing.T) {
	got := checks.SeedDefinitions()
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	byPlugin := map[string]checks.DefinitionInput{}
	for _, in := range got {
		byPlugin[in.Plugin] = in
	}
	for _, plugin := range []string{checks.PluginCPU, checks.PluginMemory, checks.PluginDisk, checks.PluginService} {
		in, ok := byPlugin[plugin]
		if !ok {
			t.Fatalf("seed set missing plugin %q", plugin)
		}
		if in.BindingKind != checks.BindingOrg || !in.Enabled {
			t.Fatalf("plugin %q = %+v, want org-bound and enabled", plugin, in)
		}
		if _, err := checks.NewService(newFakeStore()).CreateDefinition(ctx, uuid.Must(uuid.NewV7()), in); err != nil {
			t.Fatalf("SeedDefinitions()' own %q entry must itself pass validation: %v", plugin, err)
		}
	}
}

// TestSeedForOrgPropagatesAFailure: SeedForOrg run twice for the same
// organization collides with its own first pass's rows (a key already
// taken at the same scope) — the pathological "hook ran twice" case its
// own doc comment describes — and the failure is reported, wrapped with
// the offending key, rather than silently discarded.
func TestSeedForOrgPropagatesAFailure(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	org := uuid.Must(uuid.NewV7())
	if err := svc.SeedForOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	err := svc.SeedForOrg(ctx, org)
	if !errors.Is(err, checks.ErrKeyTaken) {
		t.Fatalf("err = %v, want ErrKeyTaken", err)
	}
	if !strings.Contains(err.Error(), "cpu") {
		t.Fatalf("err = %v, want it to name the offending key", err)
	}
}

func TestGetDefinitionIsOrgScoped(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	orgA, orgB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	d, err := svc.CreateDefinition(ctx, orgA, checks.DefinitionInput{
		Plugin: checks.PluginCPU, Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetDefinition(ctx, orgA, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != d.ID || got.Key != checks.PluginCPU {
		t.Fatalf("got = %+v, want the created definition", got)
	}
	if _, err := svc.GetDefinition(ctx, orgB, d.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("cross-org get = %v, want ErrNotFound", err)
	}
}
