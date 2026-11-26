package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The sync reports the names of the repository's vault-encrypted variables
// with the files they come from, in every location, in both forms, and
// never a value (backlog 2.134).
func TestSyncExecutorReportsVaultNames(t *testing.T) {
	const tagged = "!vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  3132333435363738\n"
	const inline = "\"$ANSIBLE_VAULT;1.1;AES256\\n3132333435363738\"\n"
	clone := t.TempDir()
	writeTree(t, clone, map[string]string{
		"site.yml": "- hosts: all\n  vars:\n    play_inline: " + inline + "    play_tagged: !vault |\n      $ANSIBLE_VAULT;1.1;AES256\n      3132\n" +
			"  roles:\n    - web\n",
		"group_vars/all.yml":                "gv_tagged: " + tagged + "gv_inline: " + inline + "gv_plain: 1\n",
		"host_vars/web1.yml":                "hv_tagged: " + tagged + "hv_inline: " + inline,
		"roles/web/defaults/main.yml":       "rd_tagged: " + tagged + "rd_inline: " + inline,
		"roles/web/vars/main.yml":           "rv_tagged: " + tagged + "rv_inline: " + inline,
		"roles/web/meta/argument_specs.yml": webSpec,
	})
	outside := filepath.Join(t.TempDir(), "outside.yml")
	if err := os.WriteFile(outside, []byte("escaped: "+tagged), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(clone, "group_vars", "linked.yml")); err != nil {
		t.Fatal(err)
	}
	r, _, stats := syncSources(t, &SyncExecutor{Fetcher: &fakeFetcher{dir: clone, commit: testCommit}}, "")
	if r.Vault == nil {
		t.Fatal("the report names no vault variables")
	}
	var got []string
	for _, n := range r.Vault.Names {
		got = append(got, n.Name+"@"+n.File)
	}
	want := []string{
		"gv_inline@group_vars/all.yml", "gv_tagged@group_vars/all.yml",
		"hv_inline@host_vars/web1.yml", "hv_tagged@host_vars/web1.yml",
		"play_inline@site.yml", "play_tagged@site.yml",
		"rd_inline@roles/web/defaults/main.yml", "rd_tagged@roles/web/defaults/main.yml",
		"rv_inline@roles/web/vars/main.yml", "rv_tagged@roles/web/vars/main.yml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("vault names =\n%q\nwant\n%q", got, want)
	}
	// A link out of the clone is not followed: its file counts as unread.
	if !reflect.DeepEqual(r.Vault.Unread, []string{"group_vars/linked.yml"}) || r.Vault.Complete() {
		t.Fatalf("unread = %q, complete = %v", r.Vault.Unread, r.Vault.Complete())
	}
	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "3132") || strings.Contains(string(data), "escaped") {
		t.Fatalf("the sync result carries a vault value or a name from outside the clone: %s", data)
	}
}
