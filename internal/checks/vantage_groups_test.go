package checks_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

func vantageDef(key string, groups ...uuid.UUID) checks.DefinitionInput {
	list := make([]any, len(groups))
	for i, g := range groups {
		list[i] = g.String()
	}
	return checks.DefinitionInput{
		Plugin: "icmp", Key: key, Interval: time.Minute, BindingKind: checks.BindingOrg, Enabled: true,
		Params: map[string]any{"vantage_groups": list},
	}
}

func TestDefinitionsRefuseUnknownVantageGroups(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	org, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	mine, err := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "probes", Selector: "role=probe"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := svc.CreateGroup(ctx, other, checks.GroupInput{Name: "theirs", Selector: "role=probe"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CreateDefinition(ctx, org, vantageDef("ok", mine.ID)); err != nil {
		t.Fatalf("own group: %v", err)
	}
	for name, groups := range map[string][]uuid.UUID{
		"random":  {uuid.Must(uuid.NewV7())},
		"foreign": {foreign.ID},
		"mixed":   {mine.ID, foreign.ID},
	} {
		_, err := svc.CreateDefinition(ctx, org, vantageDef("bad-"+name, groups...))
		if !errors.Is(err, checks.ErrInvalidInput) {
			t.Errorf("create with %s group = %v, want ErrInvalidInput", name, err)
		}
	}

	d, err := svc.CreateDefinition(ctx, org, checks.DefinitionInput{Plugin: "icmp", Key: "plain", Interval: time.Minute, BindingKind: checks.BindingOrg, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	upd := vantageDef("plain", foreign.ID)
	if _, err := svc.UpdateDefinition(ctx, org, d.ID, upd); !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("update with foreign group = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.UpdateDefinition(ctx, org, d.ID, vantageDef("plain", mine.ID)); err != nil {
		t.Fatalf("update with own group: %v", err)
	}
}

func TestDeleteGroupRefusedWhileAVantage(t *testing.T) {
	svc := checks.NewService(newFakeStore())
	org, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	used, _ := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "used", Selector: "role=probe"})
	free, _ := svc.CreateGroup(ctx, org, checks.GroupInput{Name: "free", Selector: "role=probe"})
	for _, key := range []string{"zeta", "alpha"} {
		if _, err := svc.CreateDefinition(ctx, org, vantageDef(key, used.ID)); err != nil {
			t.Fatal(err)
		}
	}
	// A second binding of the same key must not repeat it.
	dup := vantageDef("alpha", used.ID)
	dup.BindingKind, dup.GroupID = checks.BindingGroup, &free.ID
	if _, err := svc.CreateDefinition(ctx, org, dup); err != nil {
		t.Fatal(err)
	}

	err := svc.DeleteGroup(ctx, org, used.ID)
	var inUse *checks.GroupInUseError
	if !errors.As(err, &inUse) || !errors.Is(err, checks.ErrConflict) {
		t.Fatalf("DeleteGroup(used) = %v, want *GroupInUseError matching ErrConflict", err)
	}
	if want := []string{"alpha", "zeta"}; !reflect.DeepEqual(inUse.Keys, want) {
		t.Fatalf("keys = %v, want %v", inUse.Keys, want)
	}
	if _, err := svc.GetGroup(ctx, org, used.ID); err != nil {
		t.Fatalf("refused delete removed the group: %v", err)
	}
	if err := svc.DeleteGroup(ctx, other, used.ID); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("foreign org delete = %v, want ErrNotFound", err)
	}
	// Another organization's checks never count.
	if err := svc.DeleteGroup(ctx, org, free.ID); err != nil {
		t.Fatalf("DeleteGroup(unreferenced): %v", err)
	}
}
