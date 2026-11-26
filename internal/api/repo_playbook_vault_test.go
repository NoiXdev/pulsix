package api_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/tenancy"
	"github.com/noixdev/pulsix/internal/varsource"
)

// TestRepoPlaybooksListVaultNames (backlog 2.134): the sources view names
// the repository's vault-encrypted variables, by name and file only, and
// leaves vault out when the last read did not report them (an older
// worker's), so "unknown" never reads as "none".
func TestRepoPlaybooksListVaultNames(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("rpv-owner@example.test", "correct horse battery")
	org := s.SeedOrg("rpv-org", owner)
	viewer := s.SeedUser("rpv-viewer@example.test", "correct horse battery")
	s.AddMember(org, viewer, tenancy.RoleViewer)
	repoID := seedRepoFixture(t, s, org, "infra")
	base := s.HTTP.URL + "/api/v1/orgs/rpv-org/repos/" + repoID + "/playbooks"
	vc := s.LoginAs(viewer)
	synced := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

	type vaultBody struct {
		Sources struct {
			Vault *struct {
				Names []struct {
					Name string `json:"name"`
					File string `json:"file"`
				} `json:"names"`
				EncryptedFiles []string `json:"encrypted_files"`
				Unread         []string `json:"unread"`
				Cut            bool     `json:"cut"`
			} `json:"vault"`
		} `json:"sources"`
	}

	seedVarSources(t, s, org, repoID, repos.SourcesRecord{
		Commit: "abc123", SyncedAt: synced, WorkerReads: true,
		Vault: &varsource.Vault{
			Names:          []varsource.VaultName{{Name: "db_password", File: "group_vars/all.yml"}},
			EncryptedFiles: []string{"group_vars/all/vault.yml"}, Unread: []string{"vars/broken.yml"}, Cut: true,
		},
	}, nil)
	resp := requestJSON(t, vc, http.MethodGet, base, nil)
	raw := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, raw)
	}
	var body vaultBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	v := body.Sources.Vault
	if v == nil || len(v.Names) != 1 || v.Names[0].Name != "db_password" || v.Names[0].File != "group_vars/all.yml" ||
		!reflect.DeepEqual(v.EncryptedFiles, []string{"group_vars/all/vault.yml"}) || !reflect.DeepEqual(v.Unread, []string{"vars/broken.yml"}) || !v.Cut {
		t.Fatalf("vault = %s", raw)
	}

	seedVarSources(t, s, org, repoID, repos.SourcesRecord{Commit: "abc124", SyncedAt: synced, WorkerReads: true}, nil)
	resp = requestJSON(t, vc, http.MethodGet, base, nil)
	raw = mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK || strings.Contains(raw, `"vault"`) {
		t.Fatalf("an older worker's read: %d %s, want no vault", resp.StatusCode, raw)
	}
}
