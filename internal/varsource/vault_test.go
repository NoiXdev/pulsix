package varsource

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// vaultTag and vaultString are a vault-encrypted value in the two forms a
// repository holds one: the !vault tag, and the ciphertext as a plain
// string (what a !vault value becomes in a JSON file, or pasted by hand).
const (
	vaultTag    = "!vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  6162636465666768\n"
	vaultString = "\"$ANSIBLE_VAULT;1.1;AES256\\n6162636465666768\"\n"
	wholeVault  = "$ANSIBLE_VAULT;1.1;AES256\n6162636465666768\n"
)

// vaultTagAt is vaultTag with its ciphertext indented by n spaces, for a
// key nested n-2 deep.
func vaultTagAt(n int) string {
	return strings.ReplaceAll(vaultTag, "\n  ", "\n"+strings.Repeat(" ", n))
}

func vaultRepo() map[string]string {
	return map[string]string{
		"site.yml": "- hosts: all\n  vars:\n    play_tagged: " + vaultTagAt(6) + "    play_plain: 1\n    play_string: " + vaultString +
			"  vars_files: [secrets/extra.yml, \"{{ env }}.yml\"]\n  roles:\n    - role: db\n      vars:\n        role_entry: " + vaultTagAt(10) +
			"      role_param: " + vaultString,
		"secrets/extra.yml":                   "extra_tagged: " + vaultTag + "extra_plain: x\n",
		"group_vars/all.yml":                  "gv_tagged: " + vaultTag + "gv_plain: hello\ngv_string: " + vaultString,
		"group_vars/web/vault.yaml":           "gv_nested:\n  inner:\n    - " + vaultTagAt(8) + "gv_list: [a, b]\n",
		"group_vars/web/all.vault":            "gv_wrong_extension: " + vaultTag,
		"group_vars/db":                       "gv_no_extension: " + vaultString,
		"group_vars/all/encrypted.yml":        wholeVault,
		"inventories/prod/host_vars/h1.json":  "{\"hv_json\": \"$ANSIBLE_VAULT;1.1;AES256\\n6162\", \"hv_plain\": 1}\n",
		"inventories/prod/host_vars/h1/a.yml": "hv_tagged: " + vaultTag,
		"roles/db/defaults/main.yml":          "rd_tagged: " + vaultTag + "rd_plain: 1\n",
		"roles/db/vars/main.yml":              "rv_string: " + vaultString,
		"roles/db/tasks/main.yml":             "- debug: {msg: x}\n  vars:\n    task_var: " + vaultTagAt(6),
		"defaults/main.yml":                   "not_a_role_default: " + vaultTag,
		"vars/broken.yml":                     "a: [unclosed\n",
	}
}

func TestVaultNamesInEveryLocation(t *testing.T) {
	roles := mapFS(map[string]string{"ext/defaults/main.yml": "installed_role: " + vaultTag, "ext/tasks/main.yml": "- debug: {}\n"})
	colls := mapFS(map[string]string{
		"ns/coll/roles/r/vars/main.yml": "coll_role: " + vaultString,
		"ns/coll/plugins/vars/x.yml":    "coll_plugin: " + vaultTag,
	})
	r := ParseRepoInstalled(context.Background(), mapFS(vaultRepo()), Installed{Roles: roles, Collections: colls}, "c")
	if r.Vault == nil {
		t.Fatal("no vault names reported")
	}
	want := []VaultName{
		{Name: "coll_role", File: InstalledCollectionsPrefix + "ns/coll/roles/r/vars/main.yml"},
		{Name: "extra_tagged", File: "secrets/extra.yml"},
		{Name: "gv_nested", File: "group_vars/web/vault.yaml"},
		{Name: "gv_no_extension", File: "group_vars/db"},
		{Name: "gv_string", File: "group_vars/all.yml"},
		{Name: "gv_tagged", File: "group_vars/all.yml"},
		{Name: "hv_json", File: "inventories/prod/host_vars/h1.json"},
		{Name: "hv_tagged", File: "inventories/prod/host_vars/h1/a.yml"},
		{Name: "installed_role", File: InstalledRolesPrefix + "ext/defaults/main.yml"},
		{Name: "play_string", File: "site.yml"},
		{Name: "play_tagged", File: "site.yml"},
		{Name: "rd_tagged", File: "roles/db/defaults/main.yml"},
		{Name: "role_entry", File: "site.yml"},
		{Name: "role_param", File: "site.yml"},
		{Name: "rv_string", File: "roles/db/vars/main.yml"},
	}
	if !reflect.DeepEqual(r.Vault.Names, want) {
		t.Errorf("names =\n%+v\nwant\n%+v", r.Vault.Names, want)
	}
	if want := []string{"group_vars/all/encrypted.yml"}; !reflect.DeepEqual(r.Vault.EncryptedFiles, want) {
		t.Errorf("encrypted files = %q, want %q", r.Vault.EncryptedFiles, want)
	}
	if want := []string{"vars/broken.yml"}; !reflect.DeepEqual(r.Vault.Unread, want) {
		t.Errorf("unread = %q, want %q", r.Vault.Unread, want)
	}
	if r.Vault.Cut || r.Vault.Complete() {
		t.Errorf("cut = %v, complete = %v; want an incomplete read that was not cut", r.Vault.Cut, r.Vault.Complete())
	}
	// Never a value: nothing of the ciphertext travels.
	data, err := json.Marshal(r.Vault)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ANSIBLE_VAULT") || strings.Contains(string(data), "6162") {
		t.Fatalf("the vault names carry a value: %s", data)
	}
}

func TestVaultNamesComplete(t *testing.T) {
	r := ParseRepo(context.Background(), mapFS(map[string]string{
		"site.yml":           "- hosts: all\n  vars:\n    a: 1\n",
		"group_vars/all.yml": "plain: 1\n",
	}), "c")
	if r.Vault == nil || !r.Vault.Complete() || len(r.Vault.Names) != 0 {
		t.Fatalf("vault = %+v, want a complete read with no names", r.Vault)
	}
	data, err := json.Marshal(r.Vault)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"names":[],"encrypted_files":[],"unread":[],"cut":false}` {
		t.Fatalf("json = %s", data)
	}
}

func TestVaultNamesSkipInvalidNames(t *testing.T) {
	r := ParseRepo(context.Background(), mapFS(map[string]string{
		"group_vars/all.yml": "\"not a name\": " + vaultTag + "\"9lives\": " + vaultTag + "Upper_Case: " + vaultTag,
	}), "c")
	if want := []VaultName{{Name: "Upper_Case", File: "group_vars/all.yml"}}; !reflect.DeepEqual(r.Vault.Names, want) {
		t.Fatalf("names = %+v, want %+v", r.Vault.Names, want)
	}
}

func TestVaultNamesTimeLimitCuts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := ParseRepo(ctx, mapFS(map[string]string{"group_vars/all.yml": "a: " + vaultTag}), "c")
	if r.Vault == nil || !r.Vault.Cut || r.Vault.Complete() {
		t.Fatalf("vault = %+v, want a cut read", r.Vault)
	}
}

func TestVaultNamesCapCuts(t *testing.T) {
	var b strings.Builder
	for i := range maxVaultNames + 1 {
		fmt.Fprintf(&b, "v%d: %s", i, vaultString)
	}
	r := ParseRepo(context.Background(), mapFS(map[string]string{"group_vars/all.yml": b.String()}), "c")
	if len(r.Vault.Names) != maxVaultNames || !r.Vault.Cut {
		t.Fatalf("%d names, cut %v; want %d and cut", len(r.Vault.Names), r.Vault.Cut, maxVaultNames)
	}
}
