package api

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/items"
)

// TestItemReadsResolveAPluginKeyDecision pins backlog 3.13g on the read side:
// the items listing and the fleet's all-ignored flag resolve an item of a
// copied check through an org-wide decision on its plugin's key, from the
// plugin the view carries.
func TestItemReadsResolveAPluginKeyDecision(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	decision := items.Decision{
		ID: uuid.Must(uuid.NewV7()), Scope: items.ScopeOrg, CheckKey: "disk",
		MatchKind: items.MatchGlob, Pattern: "*", Verdict: items.Ignore,
		CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0),
	}
	view := func(plugin string) items.View {
		return items.View{Item: items.Item{HostID: host, CheckKey: "disk-data", Name: "/data"}, Plugin: plugin}
	}

	dto := toItemDTO(view("disk"), []items.Decision{decision})
	if dto.Verdict != "ignore" || dto.DecidedBy == nil || dto.DecidedBy.CheckKey != "disk" {
		t.Fatalf("item = %+v, want ignored through the decision on the plugin key", dto)
	}
	if !allIgnoredHosts([]items.View{view("disk")}, []items.Decision{decision})[host] {
		t.Fatal("host with only an item of a copied disk check is not all-ignored, want it to be")
	}
	if dto := toItemDTO(view(""), []items.Decision{decision}); dto.Verdict != "watch" {
		t.Fatalf("item of unknown plugin = %+v, want watch", dto)
	}
}
