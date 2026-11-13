package rules_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/rules"
)

// itemForm is hostForm narrowed to one disk mount, the baseline for the
// item_check_key tests (backlog 3.13e).
func itemForm(hostID uuid.UUID) rules.RuleInput {
	return rules.RuleInput{
		Name: "data disk", Enabled: true, Scope: rules.ScopeItem, HostID: &hostID, Item: "/data",
		Metric: "disk.used_pct", Severity: rules.SeverityWarn, Mode: "form",
		Form: &rules.Form{Metric: "disk.used_pct", Op: ">", Value: 90},
	}
}

func TestItemCheckKeyIsRefusedOutsideItemScope(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	group := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())

	for _, tt := range []struct {
		name string
		in   func() rules.RuleInput
	}{
		{"org", func() rules.RuleInput {
			in := itemForm(host)
			in.Scope, in.HostID, in.Item = rules.ScopeOrg, nil, ""
			return in
		}},
		{"group", func() rules.RuleInput {
			in := itemForm(host)
			in.Scope, in.HostID, in.GroupID, in.Item = rules.ScopeGroup, nil, &group, ""
			return in
		}},
		{"host", func() rules.RuleInput {
			in := itemForm(host)
			in.Scope, in.Item = rules.ScopeHost, ""
			return in
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in()
			in.ItemCheckKey = "disk-data"
			svc := rules.NewService(newFakeStore())
			_, err := svc.Create(ctx, org, in)
			if !errors.Is(err, rules.ErrInvalidInput) || !strings.Contains(err.Error(), "item_check_key") {
				t.Fatalf("Create = %v, want ErrInvalidInput naming item_check_key", err)
			}
			if _, err := svc.Check(ctx, org, in); !errors.Is(err, rules.ErrInvalidInput) {
				t.Fatalf("Check = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestItemCheckKeyMustBeAValidCheckKey(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	for _, key := range []string{"Disk", "disk_data", "-disk", "disk data", "disk/data", "a" + strings.Repeat("b", 63)} {
		t.Run(key, func(t *testing.T) {
			in := itemForm(host)
			in.ItemCheckKey = key
			_, err := rules.NewService(newFakeStore()).Create(ctx, org, in)
			if !errors.Is(err, rules.ErrInvalidInput) || !strings.Contains(err.Error(), "item_check_key") {
				t.Fatalf("Create(item_check_key=%q) = %v, want ErrInvalidInput naming item_check_key", key, err)
			}
		})
	}
}

func TestItemCheckKeyIsTrimmedStoredAndReadBack(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	store := newFakeStore()
	svc := rules.NewService(store)

	in := itemForm(host)
	in.ItemCheckKey = "  disk-data\t"
	created, err := svc.Create(ctx, org, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.ItemCheckKey != "disk-data" {
		t.Fatalf("ItemCheckKey = %q, want the trimmed key", created.ItemCheckKey)
	}
	if created.CheckKey != "disk" {
		t.Fatalf("CheckKey = %q, want the plugin's key, still derived from the metric", created.CheckKey)
	}
	got, err := svc.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ItemCheckKey != "disk-data" {
		t.Fatalf("read back ItemCheckKey = %q", got.ItemCheckKey)
	}

	t.Run("a blank key is the plugin's key", func(t *testing.T) {
		in := itemForm(host)
		in.ItemCheckKey = "   "
		r, err := svc.Create(ctx, org, in)
		if err != nil {
			t.Fatal(err)
		}
		if r.ItemCheckKey != "" {
			t.Fatalf("ItemCheckKey = %q, want empty", r.ItemCheckKey)
		}
	})

	t.Run("a blank key is accepted on another scope", func(t *testing.T) {
		in := hostForm(host)
		in.ItemCheckKey = " "
		r, err := svc.Create(ctx, org, in)
		if err != nil {
			t.Fatalf("Create = %v, want a blank key to count as absent", err)
		}
		if r.ItemCheckKey != "" {
			t.Fatalf("ItemCheckKey = %q, want empty", r.ItemCheckKey)
		}
	})
}

// TestUpdateWithoutItemCheckKeyFallsBackToThePluginsKey pins the update
// semantics the API documents: Update is a full replace, so an update that
// leaves item_check_key out stores the empty key — the plugin's.
func TestUpdateWithoutItemCheckKeyFallsBackToThePluginsKey(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	org := uuid.Must(uuid.NewV7())
	svc := rules.NewService(newFakeStore())

	in := itemForm(host)
	in.ItemCheckKey = "disk-data"
	created, err := svc.Create(ctx, org, in)
	if err != nil {
		t.Fatal(err)
	}

	changed := itemForm(host)
	changed.ItemCheckKey = "disk-backup"
	updated, err := svc.Update(ctx, org, created.ID, changed)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ItemCheckKey != "disk-backup" {
		t.Fatalf("ItemCheckKey = %q, want the new key", updated.ItemCheckKey)
	}

	cleared, err := svc.Update(ctx, org, created.ID, itemForm(host))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.ItemCheckKey != "" {
		t.Fatalf("ItemCheckKey = %q, want empty after an update without it", cleared.ItemCheckKey)
	}
	got, err := svc.Get(ctx, org, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ItemCheckKey != "" {
		t.Fatalf("stored ItemCheckKey = %q, want empty", got.ItemCheckKey)
	}
}
