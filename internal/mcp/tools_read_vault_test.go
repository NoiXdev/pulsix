package mcp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

// TestListRepoFilesNamesVaultVariables (backlog 2.134): list_repo_files
// shows the repository's vault-encrypted variables by name and file, so an
// assistant sees which names a run request's extra vars may not use.
func TestListRepoFilesNamesVaultVariables(t *testing.T) {
	f := newOrgFixture(t)
	if err := repos.NewSQLStore(f.s.DB).ReplaceVarSources(f.scoped, f.org, f.repo, repos.SourcesRecord{
		Commit: "abc123", SyncedAt: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC), WorkerReads: true,
		Vault: &varsource.Vault{Names: []varsource.VaultName{{Name: "db_password", File: "group_vars/all.yml"}}, EncryptedFiles: []string{}, Unread: []string{}},
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	text := callOK(t, mustConnect(t, f.s, f.token), "list_repo_files", map[string]any{"repo_id": f.repo.String()}, nil)
	for _, want := range []string{`"vault"`, `"db_password"`, `"group_vars/all.yml"`} {
		if !strings.Contains(text, want) {
			t.Errorf("list_repo_files lacks %s:\n%s", want, text)
		}
	}
}
