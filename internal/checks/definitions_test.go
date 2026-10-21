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

// TestUpdateDefinitionRefusesChangingASharedRowsKeyPluginParamsOrInterval is
// TestUpdateDefinitionRefusesChangingASeededRowsKeyOrPlugin's own test for a
// PROJECTED definition (origin = "shared", spec v0.2f decision 2,
// migration 00062): key, plugin, params and interval belong to the
// installation-owned definition and are refused — a WIDER refusal than a
// seeded row's (which only fixes key and plugin, not interval/params) —
// while binding_kind, group_id, host_id and enabled stay ordinary, mutable
// columns, the OPPOSITE of what decision 2 gives a repository or job
// template's own projection.
func TestUpdateDefinitionRefusesChangingASharedRowsKeyPluginParamsOrInterval(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())
	sharedDefID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())
	store.defsByID[id] = checks.Definition{
		ID: id, OrgID: org, Key: "disk-data", Plugin: checks.PluginDisk, Interval: 5 * time.Minute,
		Enabled: true, Origin: "shared", BindingKind: checks.BindingOrg, SharedDefinitionID: &sharedDefID,
	}

	cases := []struct {
		name string
		in   checks.DefinitionInput
	}{
		{"key", checks.DefinitionInput{Key: "disk-data-2", Plugin: checks.PluginDisk, Interval: 5 * time.Minute, BindingKind: checks.BindingOrg}},
		{"plugin", checks.DefinitionInput{Key: "disk-data", Plugin: checks.PluginMemory, Interval: 5 * time.Minute, BindingKind: checks.BindingOrg}},
		{"interval", checks.DefinitionInput{Key: "disk-data", Plugin: checks.PluginDisk, Interval: 10 * time.Minute, BindingKind: checks.BindingOrg}},
		{"params", checks.DefinitionInput{
			Key: "disk-data", Plugin: checks.PluginDisk, Interval: 5 * time.Minute, BindingKind: checks.BindingOrg,
			Params: map[string]any{"mounts": []string{"/data"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.UpdateDefinition(ctx, org, id, tc.in); !errors.Is(err, checks.ErrInvalidInput) {
				t.Fatalf("changing a shared row's %s: err = %v, want ErrInvalidInput", tc.name, err)
			}
			got, err := svc.GetDefinition(ctx, org, id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Key != "disk-data" || got.Plugin != checks.PluginDisk || got.Interval != 5*time.Minute {
				t.Fatalf("definition after a refused edit = %+v, want it untouched", got)
			}
		})
	}

	// Rebinding to a group and disabling it IS allowed — decision 2 gives
	// the binding to the projection, not the definition.
	group := uuid.Must(uuid.NewV7())
	updated, err := svc.UpdateDefinition(ctx, org, id, checks.DefinitionInput{
		Key: "disk-data", Plugin: checks.PluginDisk, Interval: 5 * time.Minute, Enabled: false,
		BindingKind: checks.BindingGroup, GroupID: &group,
	})
	if err != nil {
		t.Fatalf("rebinding/disabling a shared row must be allowed: %v", err)
	}
	if updated.Enabled || updated.BindingKind != checks.BindingGroup || updated.GroupID == nil || *updated.GroupID != group {
		t.Fatalf("updated = %+v, want the new binding and enabled=false applied", updated)
	}
	if updated.Origin != "shared" || updated.SharedDefinitionID == nil || *updated.SharedDefinitionID != sharedDefID {
		t.Fatalf("updated origin/SharedDefinitionID = %q/%v, want them to survive the edit unchanged", updated.Origin, updated.SharedDefinitionID)
	}
}

// TestUpdateDefinitionAcceptsParamsEchoedBackAsTheSameValue proves
// paramsEqual's own reasoning: a client that merely echoes a GET response's
// params back on a PUT (common for a form that round-trips the full row)
// must not be refused as "changing" the installation-owned params, even
// when one side is a nil map and the other an explicit empty one.
func TestUpdateDefinitionAcceptsParamsEchoedBackAsTheSameValue(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())
	sharedDefID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())
	store.defsByID[id] = checks.Definition{
		ID: id, OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: time.Minute,
		Enabled: true, Origin: "shared", Params: nil, BindingKind: checks.BindingOrg, SharedDefinitionID: &sharedDefID,
	}

	if _, err := svc.UpdateDefinition(ctx, org, id, checks.DefinitionInput{
		Key: "cpu", Plugin: checks.PluginCPU, Interval: time.Minute, Enabled: true,
		BindingKind: checks.BindingOrg, Params: map[string]any{},
	}); err != nil {
		t.Fatalf("UpdateDefinition() with an empty-map echo of a nil params = %v, want nil", err)
	}
}

// TestDeleteDefinitionRefusesAProjectedRow is
// repos.Service.Delete's own test (spec v0.2f decision 6), ported here: a
// projected check definition cannot be deleted from inside an organization
// — the next propagation from the definition would simply recreate it — and
// the refusal is ErrForbidden, not ErrInvalidInput, since no change to the
// DELETE request (which carries none) could ever make it succeed.
func TestDeleteDefinitionRefusesAProjectedRow(t *testing.T) {
	store := newFakeStore()
	svc := checks.NewService(store)
	org := uuid.Must(uuid.NewV7())
	sharedDefID := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())
	store.defsByID[id] = checks.Definition{
		ID: id, OrgID: org, Key: "cpu", Plugin: checks.PluginCPU, Interval: time.Minute,
		Enabled: true, Origin: "shared", BindingKind: checks.BindingOrg, SharedDefinitionID: &sharedDefID,
	}

	if err := svc.DeleteDefinition(ctx, org, id); !errors.Is(err, checks.ErrForbidden) {
		t.Fatalf("DeleteDefinition() on a projected row = %v, want ErrForbidden", err)
	}
	if _, err := svc.GetDefinition(ctx, org, id); err != nil {
		t.Fatalf("GetDefinition() after a refused delete = %v, want the row still to exist", err)
	}
}

// TestDeleteDefinitionStillAllowsASeededRow proves the shared-origin refusal
// above is scoped to origin == "shared" specifically, not to every
// non-custom row: DeleteDefinition's own doc comment is explicit that a
// seeded row deletes like any other.
func TestDeleteDefinitionStillAllowsASeededRow(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	org := uuid.Must(uuid.NewV7())
	if err := svc.SeedForOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	defs, err := svc.ListDefinitions(ctx, org)
	if err != nil || len(defs) == 0 {
		t.Fatalf("ListDefinitions() = %v, %v, want the seed set", defs, err)
	}
	if err := svc.DeleteDefinition(ctx, org, defs[0].ID); err != nil {
		t.Fatalf("DeleteDefinition() on a seeded row = %v, want nil", err)
	}
}

// TestValidateDefinitionShapeIsWhatInternalSharedCallsDirectly exercises
// checks.ValidateDefinitionShape on its own, independent of
// normalizeDefinition/a binding — this is the exact entry point
// internal/shared.Service calls for its own, installation-owned check
// definitions (shared.validateCheckDefinitionInput), so its own contract
// (default an empty key to the plugin name, refuse an unsupported plugin,
// refuse an out-of-range interval, apply the plugin's own parameter schema)
// is pinned here directly rather than only indirectly through
// normalizeDefinition's own tests.
func TestValidateDefinitionShapeIsWhatInternalSharedCallsDirectly(t *testing.T) {
	key, err := checks.ValidateDefinitionShape(checks.PluginCPU, "", nil, time.Minute)
	if err != nil || key != "cpu" {
		t.Fatalf("ValidateDefinitionShape(cpu, \"\", ...) = (%q, %v), want (\"cpu\", nil)", key, err)
	}
	if _, err := checks.ValidateDefinitionShape("nonsense", "k", nil, time.Minute); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("ValidateDefinitionShape with an unsupported plugin error = %v, want ErrInvalidInput", err)
	}
	if _, err := checks.ValidateDefinitionShape(checks.PluginCPU, "k", nil, 5*time.Second); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("ValidateDefinitionShape with a too-short interval error = %v, want ErrInvalidInput", err)
	}
	if _, err := checks.ValidateDefinitionShape(checks.PluginService, "k", nil, time.Minute); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("ValidateDefinitionShape(service, no units) error = %v, want ErrInvalidInput", err)
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

// TestCreateDefinitionRefusesAKeyASharedProjectionHolds is the service-side
// half of a hole a review found in migration 00063's own fix. That trigger
// rebinds a shared projection to ('org', nil, nil) when the group or host
// it was bound to is deleted, so the provider's check survives a customer
// deleting their own group. But check_definitions carries UNIQUE NULLS NOT
// DISTINCT (org_id, binding_kind, group_id, host_id, key), so the rebind
// only works while that organization-scope slot is still free — and
// nothing stopped a customer from taking it:
//
//  1. the provider creates shared key K; the projection lands at org scope
//  2. the customer rebinds it to a group, freeing ('org', nil, nil, K)
//  3. the customer creates their OWN check with key K at org scope
//  4. the customer deletes the group — and the trigger's UPDATE raises
//     23505, aborting the whole DELETE FROM monitoring_groups
//
// Step 4 is the damage: the customer can no longer delete their own group,
// with an opaque error naming a table they never touched. The fix is to
// make step 3 impossible rather than to handle step 4, because by step 4
// the trigger has only bad options left — fail the customer's delete, or
// silently drop the provider's check.
//
// The refusal is deliberately narrow: it bites only on a key some SHARED
// projection in this organization already holds, at any scope. The same key
// at two different scopes stays legal in general (UNIQUE NULLS NOT DISTINCT
// allows it on purpose, and running disk org-wide and disk on one host with
// different parameters is a real thing people do) — taking that away would
// change behaviour for ordinary checks that have nothing to do with shared
// resources.
func TestCreateDefinitionRefusesAKeyASharedProjectionHolds(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	group := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := checks.NewService(store)

	// The projection, already rebound by its customer to a group — so the
	// organization-scope slot its key needs back is free right now.
	projection := checks.Definition{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Key: "disk-data", Plugin: "disk",
		Interval: 60 * time.Second, Enabled: true, Origin: "shared",
		BindingKind: checks.BindingGroup, GroupID: &group,
	}
	store.defsByID[projection.ID] = projection

	_, err := svc.CreateDefinition(ctx, org, checks.DefinitionInput{
		Key: "disk-data", Plugin: "disk", Interval: 60 * time.Second, Enabled: true,
		BindingKind: checks.BindingOrg,
	})
	if !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("creating a check whose key a shared projection holds = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "disk-data") {
		t.Fatalf("the refusal must name the key so a person can act on it, got %q", err)
	}

	// Narrowness: a key NO shared projection holds is unaffected, including
	// at a scope another ordinary check already uses.
	if _, err := svc.CreateDefinition(ctx, org, checks.DefinitionInput{
		Key: "disk-logs", Plugin: "disk", Interval: 60 * time.Second, Enabled: true,
		BindingKind: checks.BindingOrg,
	}); err != nil {
		t.Fatalf("a key no shared projection holds must still be creatable, got %v", err)
	}
}
