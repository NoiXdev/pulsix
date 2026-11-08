package shared_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/shared"
)

// TestCreateAndUpdateRepoStoreTheCanonicalSubpath proves a shared
// definition's subpath is stored in the same canonical form as an
// organization's own repository's (backlog 7.94), so its projections — the
// rows every organization runs from — never carry "/ansible".
func TestCreateAndUpdateRepoStoreTheCanonicalSubpath(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)

	def, err := svc.CreateRepo(ctx, shared.RepoDefinitionInput{Name: "infra", URL: "https://git.example.com/infra.git", Subpath: "/ansible/"})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if got := store.createCalls[0].Subpath; got != "ansible" {
		t.Fatalf("CreateWithProjections got subpath %q, want %q", got, "ansible")
	}
	if def.Subpath != "ansible" {
		t.Fatalf("CreateRepo returned subpath %q, want %q", def.Subpath, "ansible")
	}

	if _, err := svc.UpdateRepo(ctx, def.ID, shared.RepoDefinitionInput{Name: "infra", URL: def.URL, Subpath: "./deploy//ansible/"}); err != nil {
		t.Fatalf("UpdateRepo: %v", err)
	}
	if got := store.updateCalls[0].Subpath; got != "deploy/ansible" {
		t.Fatalf("UpdateAndPropagate got subpath %q, want %q", got, "deploy/ansible")
	}
}

// TestCreateAndUpdateRepoRefuseAnInvalidSubpath proves a shared definition
// gets the subpath rules an organization's repository always had, before
// anything is written into any organization.
func TestCreateAndUpdateRepoRefuseAnInvalidSubpath(t *testing.T) {
	store := newFakeStore()
	svc := shared.NewService(store)
	def, err := svc.CreateRepo(ctx, shared.RepoDefinitionInput{Name: "infra", URL: "https://git.example.com/infra.git"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"../x", "a/../b", "pl*ys"} {
		if _, err := svc.CreateRepo(ctx, shared.RepoDefinitionInput{Name: "other", URL: def.URL, Subpath: sub}); !errors.Is(err, shared.ErrInvalidInput) || !strings.Contains(err.Error(), "invalid subpath") {
			t.Errorf("CreateRepo(subpath %q) = %v, want ErrInvalidInput naming \"invalid subpath\"", sub, err)
		}
		if _, err := svc.UpdateRepo(ctx, def.ID, shared.RepoDefinitionInput{Name: "infra", URL: def.URL, Subpath: sub}); !errors.Is(err, shared.ErrInvalidInput) || !strings.Contains(err.Error(), "invalid subpath") {
			t.Errorf("UpdateRepo(subpath %q) = %v, want ErrInvalidInput naming \"invalid subpath\"", sub, err)
		}
	}
	if len(store.createCalls) != 1 || len(store.updateCalls) != 0 {
		t.Fatalf("store saw %d creates and %d updates, want only the first create", len(store.createCalls), len(store.updateCalls))
	}
}
