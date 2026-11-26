package repos_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

func sampleVault() *varsource.Vault {
	return &varsource.Vault{
		Names: []varsource.VaultName{
			{Name: "db_password", File: "group_vars/all.yml"},
			{Name: "api_token", File: "roles/web/vars/main.yml"},
		},
		EncryptedFiles: []string{"group_vars/all/vault.yml"},
		Unread:         []string{},
	}
}

// TestVaultNamesThroughTheSQLStore: the vault names a current worker
// reports are stored with the repository's record and read back by every
// read of it; a report without them (an older worker's) stores none, read
// back as unknown (nil), and a Failed read keeps the previous ones.
func TestVaultNamesThroughTheSQLStore(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	svc := repos.NewService(repos.NewSQLStore(d), nil, nil)
	org := seedOrg(t, q, "var-sources-vault-org")
	scoped := db.WithOrgScope(ctx, org)
	repoID := seedSourcesRepo(t, d, org, "infra")
	t0 := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

	read := func(r varsource.Report, at time.Time) {
		t.Helper()
		if _, err := svc.RecordVarSources(scoped, org, repoID, at, encodeReport(t, r), nil, "", time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	vaultOf := func() (*varsource.Vault, *varsource.Vault) {
		t.Helper()
		rec, _, found, err := svc.VarSources(scoped, org, repoID)
		if err != nil || !found {
			t.Fatalf("VarSources = found %v, %v", found, err)
		}
		recp, _, err := svc.PlaybookVarSource(scoped, org, repoID, "site.yml")
		if err != nil || recp == nil {
			t.Fatalf("PlaybookVarSource = %+v, %v", recp, err)
		}
		return rec.Vault, recp.Vault
	}

	read(varsource.Report{Commit: commitA, Playbooks: []varsource.Playbook{playbookSource("site.yml")}, Notes: []varsource.Note{}, Vault: sampleVault()}, t0)
	if a, b := vaultOf(); !reflect.DeepEqual(a, sampleVault()) || !reflect.DeepEqual(b, sampleVault()) {
		t.Fatalf("stored vault = %+v / %+v, want %+v", a, b, sampleVault())
	}
	var stored string
	if err := d.Pool().QueryRow(ctx, `SELECT vault_names::text FROM repo_var_sources WHERE repo_id = $1`, repoID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "ANSIBLE_VAULT") || !strings.Contains(stored, "db_password") {
		t.Fatalf("stored column = %s", stored)
	}

	// A Failed read keeps the names of the read it keeps.
	read(varsource.Report{Commit: commitB, Failed: true, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{varsource.ReadFailed("x")}}, t0.Add(time.Hour))
	if a, _ := vaultOf(); !reflect.DeepEqual(a, sampleVault()) {
		t.Fatalf("vault after a failed read = %+v", a)
	}

	// An older worker's read: the names are unknown, never "none".
	read(varsource.Report{Commit: commitB, Playbooks: []varsource.Playbook{playbookSource("site.yml")}, Notes: []varsource.Note{}}, t0.Add(2*time.Hour))
	if a, b := vaultOf(); a != nil || b != nil {
		t.Fatalf("vault after an older worker's read = %+v / %+v, want nil", a, b)
	}

	// A current worker that found none: known, empty.
	none := &varsource.Vault{Names: []varsource.VaultName{}, EncryptedFiles: []string{}, Unread: []string{}}
	read(varsource.Report{Commit: commitA, Playbooks: []varsource.Playbook{playbookSource("site.yml")}, Notes: []varsource.Note{}, Vault: none}, t0.Add(3*time.Hour))
	if a, _ := vaultOf(); a == nil || len(a.Names) != 0 || !a.Complete() {
		t.Fatalf("vault after a read that found none = %+v", a)
	}

	// A worker that reads no sources at all keeps the record, names
	// included, like the rest of it.
	if _, err := svc.RecordVarSources(scoped, org, repoID, t0.Add(4*time.Hour), nil, nil, "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if a, _ := vaultOf(); a == nil || !a.Complete() {
		t.Fatalf("vault after a sync that read no sources = %+v", a)
	}
}

// TestDecodeReportChecksVaultNames: a reported name that is no variable
// name, or a file too long to store, is dropped and the names count as cut
// (not all known); past the caps likewise.
func TestDecodeReportChecksVaultNames(t *testing.T) {
	store := previousRead()
	v := &varsource.Vault{
		Names: []varsource.VaultName{
			{Name: "ok_name", File: "group_vars/all.yml"},
			{Name: "not a name", File: "group_vars/all.yml"},
			{Name: "long_file", File: strings.Repeat("d/", repos.MaxStoredPathBytes)},
		},
		EncryptedFiles: []string{strings.Repeat("e/", repos.MaxStoredPathBytes), "group_vars/all/vault.yml"},
		Unread:         []string{},
	}
	raw, err := json.Marshal(varsource.Report{Commit: commitA, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{}, Vault: v})
	if err != nil {
		t.Fatal(err)
	}
	if _, log := record(t, store, time.Now(), raw); log == "" {
		t.Fatal("dropped vault names are not logged")
	}
	got := store.record.Vault
	want := &varsource.Vault{
		Names:          []varsource.VaultName{{Name: "ok_name", File: "group_vars/all.yml"}},
		EncryptedFiles: []string{"group_vars/all/vault.yml"}, Unread: []string{}, Cut: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("vault = %+v, want %+v", got, want)
	}
}
