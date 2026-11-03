package hosts_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
)

// TestCreateHostRejectsJumpModeMismatch: a jump host is named exactly when
// the mode is explicit, and the mode is one of the three the schema knows.
// Anything else is the caller's mistake (ErrInvalidInput), caught before
// the store is asked to write anything.
func TestCreateHostRejectsJumpModeMismatch(t *testing.T) {
	bastion := uuid.Must(uuid.NewV7())
	cases := map[string]hosts.Jump{
		"explicit without id": {Mode: hosts.JumpModeExplicit},
		"none with id":        {Mode: hosts.JumpModeNone, BastionID: &bastion},
		"inherit with id":     {Mode: hosts.JumpModeInherit, BastionID: &bastion},
		"unknown mode":        {Mode: "sideways"},
		"empty mode":          {},
	}
	for name, jump := range cases {
		t.Run(name, func(t *testing.T) {
			st := newFakeHostStore()
			svc := hosts.NewService(st, time.Now)
			org := uuid.Must(uuid.NewV7())
			zone, err := svc.CreateZone(ctx, org, "fra", "F")
			if err != nil {
				t.Fatal(err)
			}
			_, err = svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID, Jump: &jump})
			if !errors.Is(err, hosts.ErrInvalidInput) {
				t.Fatalf("CreateHost = %v, want ErrInvalidInput", err)
			}
			if st.calls["CreateHostWith"] != 0 {
				t.Fatal("an invalid jump host must not reach the store")
			}
		})
	}
}

// TestUpdateHostRejectsJumpModeMismatch is the same rule on the update path.
func TestUpdateHostRejectsJumpModeMismatch(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	h, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.UpdateHost(ctx, org, h.ID, h.Revision, hosts.HostInput{
		Name: "web-01", ZoneID: zone.ID, Jump: &hosts.Jump{Mode: hosts.JumpModeExplicit},
	})
	if !errors.Is(err, hosts.ErrInvalidInput) {
		t.Fatalf("UpdateHost = %v, want ErrInvalidInput", err)
	}
	if st.calls["UpdateHostWith"] != 0 {
		t.Fatal("an invalid jump host must not reach the store")
	}
}

// TestHostWriteValidatesTags: tags in a host write go through the same
// validateTags SetTags uses, so a bad tag fails the whole write.
func TestHostWriteValidatesTags(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	bad := []hosts.Tag{{Key: "Not Valid", Value: "x"}}
	if _, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID, Tags: &bad}); !errors.Is(err, hosts.ErrInvalidInput) {
		t.Fatalf("CreateHost with a bad tag = %v, want ErrInvalidInput", err)
	}
	if len(st.list) != 0 {
		t.Fatal("a bad tag must not create the host")
	}
	h, err := svc.CreateHost(ctx, org, hosts.HostInput{Name: "web-01", ZoneID: zone.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateHost(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: "web-01", ZoneID: zone.ID, Tags: &bad}); !errors.Is(err, hosts.ErrInvalidInput) {
		t.Fatalf("UpdateHost with a bad tag = %v, want ErrInvalidInput", err)
	}
}

// TestHostWritePassesJumpAndManualTagsToTheStore: a valid write hands the
// store the jump host and the tags, every tag marked manual whatever source
// the caller set, and a nil Jump/Tags stays nil (leave unchanged).
func TestHostWritePassesJumpAndManualTagsToTheStore(t *testing.T) {
	st := newFakeHostStore()
	svc := hosts.NewService(st, time.Now)
	org := uuid.Must(uuid.NewV7())
	zone, _ := svc.CreateZone(ctx, org, "fra", "F")
	bastion := uuid.Must(uuid.NewV7())
	tags := []hosts.Tag{{Key: "role", Value: "web", Source: hosts.TagSourceFact}}
	h, err := svc.CreateHost(ctx, org, hosts.HostInput{
		Name: "web-01", ZoneID: zone.ID,
		Jump: &hosts.Jump{Mode: hosts.JumpModeExplicit, BastionID: &bastion}, Tags: &tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := st.extras[h.ID]
	if got.Jump == nil || got.Jump.Mode != hosts.JumpModeExplicit || got.Jump.BastionID == nil || *got.Jump.BastionID != bastion {
		t.Fatalf("store jump = %+v", got.Jump)
	}
	if got.Tags == nil || len(*got.Tags) != 1 || (*got.Tags)[0].Source != hosts.TagSourceManual {
		t.Fatalf("store tags = %+v, want one manual tag", got.Tags)
	}

	if _, err := svc.UpdateHost(ctx, org, h.ID, h.Revision, hosts.HostInput{Name: "web-01", ZoneID: zone.ID}); err != nil {
		t.Fatal(err)
	}
	if got := st.extras[h.ID]; got.Jump != nil || got.Tags != nil {
		t.Fatalf("an update without jump and tags must pass nil for both, got %+v", got)
	}
}
