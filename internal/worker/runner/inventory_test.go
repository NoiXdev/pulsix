// mergeInventory folds the claim's connection material into the rendered
// inventory document. Pure: every input is data, every output is data —
// the runner writes the files. Table-driven tests; the doc shapes mirror
// internal/inventory/render's dynamic inventory and the claim's connection
// JSON (with the additive fields internal/jobs' payload build adds).
package runner

import (
	"encoding/json"
	"testing"

	"github.com/noixdev/pulsix/internal/credentials"
)

// testHostID is the host uuid a connection JSON carries.
const testHostID = "01900000-0000-7000-8000-000000000001"

// testDoc is a minimal rendered inventory: one host with its own credential
// and one bastion hop, as internal/inventory/render produces it.
func testDoc() map[string]any {
	return map[string]any{
		"all": map[string]any{"children": []any{"web"}},
		"web": map[string]any{"hosts": []any{"web1"}},
		"_meta": map[string]any{"hostvars": map[string]any{
			"web1": map[string]any{
				"ansible_host":            "192.0.2.10",
				"ansible_user":            "ansible",
				"ansible_port":            2222,
				"ansible_ssh_common_args": "-o ProxyJump=deploy@192.0.2.9:2222",
			},
		}},
	}
}

func secretJSON(t *testing.T, s credentials.Secret) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func varsOf(t *testing.T, doc map[string]any, host string) map[string]any {
	t.Helper()
	meta, ok := doc["_meta"].(map[string]any)
	if !ok {
		t.Fatal("no _meta")
	}
	hv, ok := meta["hostvars"].(map[string]any)
	if !ok {
		t.Fatal("no hostvars")
	}
	return hv[host].(map[string]any)
}

func TestMergeInventoryInjectsKeyFileAndRewritesChain(t *testing.T) {
	conns := map[string]string{"web1": `{"host_id":"` + testHostID + `","credential_id":"cred-1","bastions":["b1"],"bastion_credential_ids":["cred-b"],"bastion_source":"host"}`}
	secrets := map[string]string{
		"cred-1": secretJSON(t, credentials.Secret{PrivateKey: "host-key"}),
		"cred-b": secretJSON(t, credentials.Secret{PrivateKey: "bastion-key"}),
	}
	merged, hosts, keys, err := mergeInventory(testDoc(), conns, secrets, "deploy", "/w/keys")
	if err != nil {
		t.Fatalf("mergeInventory: %v", err)
	}
	vars := varsOf(t, merged, "web1")
	if vars["ansible_ssh_private_key_file"] != "/w/keys/cred-1" {
		t.Errorf("identity file = %v", vars["ansible_ssh_private_key_file"])
	}
	// The bastion chain is rewritten to the ssh_config aliases.
	if vars["ansible_ssh_common_args"] != "-o ProxyJump=pulsix-jump-b1" {
		t.Errorf("common args = %v", vars["ansible_ssh_common_args"])
	}
	// The claim's per-host user stays; only absent users fall back.
	if vars["ansible_user"] != "ansible" {
		t.Errorf("ansible_user = %v", vars["ansible_user"])
	}
	if len(keys) != 2 || keys[0] != "cred-1" || keys[1] != "cred-b" {
		t.Errorf("keys = %v, want host and bastion key ids", keys)
	}
	if len(hosts) != 1 || hosts[0].id != "b1" || hosts[0].keyPath != "/w/keys/cred-b" {
		t.Errorf("hosts = %+v", hosts)
	}
}

func TestMergeInventoryFallsBackToTemplateUser(t *testing.T) {
	doc := testDoc()
	delete(varsOf(t, doc, "web1"), "ansible_user")
	conns := map[string]string{"web1": `{"host_id":"` + testHostID + `","credential_id":"cred-1","bastion_source":"none"}`}
	secrets := map[string]string{"cred-1": secretJSON(t, credentials.Secret{Password: "pw"})}
	merged, _, keys, err := mergeInventory(doc, conns, secrets, "deploy", "/w/keys")
	if err != nil {
		t.Fatalf("mergeInventory: %v", err)
	}
	vars := varsOf(t, merged, "web1")
	if vars["ansible_user"] != "deploy" {
		t.Errorf("ansible_user = %v, want the template default", vars["ansible_user"])
	}
	if vars["ansible_password"] != "pw" {
		t.Errorf("ansible_password = %v", vars["ansible_password"])
	}
	if len(keys) != 0 {
		t.Errorf("keys = %v, a password credential writes no key", keys)
	}
}

func TestMergeInventoryPreservesOperatorPassword(t *testing.T) {
	doc := testDoc()
	varsOf(t, doc, "web1")["ansible_password"] = "operator-pw"
	conns := map[string]string{"web1": `{"host_id":"` + testHostID + `","credential_id":"cred-1","bastion_source":"none"}`}
	secrets := map[string]string{"cred-1": secretJSON(t, credentials.Secret{Password: "cred-pw"})}
	merged, _, _, err := mergeInventory(doc, conns, secrets, "", "/w/keys")
	if err != nil {
		t.Fatalf("mergeInventory: %v", err)
	}
	if varsOf(t, merged, "web1")["ansible_password"] != "operator-pw" {
		t.Error("the operator's own ansible_password must win")
	}
}

func TestMergeInventorySkipsUnrewritableChains(t *testing.T) {
	// A connection whose bastion credential ids do not line up with the
	// rendered chain keeps the rendered args as they are: never mangle a
	// value the claim does not fully explain.
	conns := map[string]string{"web1": `{"host_id":"` + testHostID + `","credential_id":"cred-1","bastions":["b1"],"bastion_source":"host"}`}
	secrets := map[string]string{"cred-1": secretJSON(t, credentials.Secret{PrivateKey: "host-key"})}
	merged, hosts, keys, err := mergeInventory(testDoc(), conns, secrets, "", "/w/keys")
	if err != nil {
		t.Fatalf("mergeInventory: %v", err)
	}
	if varsOf(t, merged, "web1")["ansible_ssh_common_args"] != "-o ProxyJump=deploy@192.0.2.9:2222" {
		t.Error("the rendered chain must survive")
	}
	if len(hosts) != 0 {
		t.Errorf("hosts = %+v, no block without a resolvable credential", hosts)
	}
	if len(keys) != 1 || keys[0] != "cred-1" {
		t.Errorf("keys = %v, the host key is still written", keys)
	}
}

func TestMergeInventoryKeepsPasswordBastionOutOfTheChain(t *testing.T) {
	// A bastion whose credential is a password cannot hop automatically
	// (ssh has no scripted password entry for ProxyJump); the chain stays
	// as rendered and the run fails fast later, at ssh.
	conns := map[string]string{"web1": `{"host_id":"` + testHostID + `","credential_id":"cred-1","bastions":["b1"],"bastion_credential_ids":["cred-b"],"bastion_source":"host"}`}
	secrets := map[string]string{
		"cred-1": secretJSON(t, credentials.Secret{PrivateKey: "host-key"}),
		"cred-b": secretJSON(t, credentials.Secret{Password: "bastion-pw"}),
	}
	merged, hosts, keys, err := mergeInventory(testDoc(), conns, secrets, "", "/w/keys")
	if err != nil {
		t.Fatalf("mergeInventory: %v", err)
	}
	if varsOf(t, merged, "web1")["ansible_ssh_common_args"] != "-o ProxyJump=deploy@192.0.2.9:2222" {
		t.Error("a password bastion must not rewrite the chain")
	}
	if len(hosts) != 0 {
		t.Errorf("hosts = %+v", hosts)
	}
	if len(keys) != 1 || keys[0] != "cred-1" {
		t.Errorf("keys = %v, the bastion's password writes no key", keys)
	}
}

func TestMergeInventoryHostWithoutCredentialUsesTemplateDefault(t *testing.T) {
	// A host without its own credential logs in with the template's
	// default: the server substituted the effective credential id, the
	// worker maps it to the key file and the login user.
	doc := testDoc()
	delete(varsOf(t, doc, "web1"), "ansible_user")
	conns := map[string]string{"web1": `{"host_id":"` + testHostID + `","credential_id":"cred-1","bastion_source":"none"}`}
	secrets := map[string]string{"cred-1": secretJSON(t, credentials.Secret{PrivateKey: "host-key"})}
	merged, hosts, keys, err := mergeInventory(doc, conns, secrets, "deploy", "/w/keys")
	if err != nil {
		t.Fatalf("mergeInventory: %v", err)
	}
	vars := varsOf(t, merged, "web1")
	if vars["ansible_user"] != "deploy" {
		t.Errorf("ansible_user = %v", vars["ansible_user"])
	}
	if vars["ansible_ssh_private_key_file"] != "/w/keys/cred-1" {
		t.Errorf("identity file = %v", vars["ansible_ssh_private_key_file"])
	}
	if len(hosts) != 0 || len(keys) != 1 {
		t.Errorf("hosts = %v keys = %v", hosts, keys)
	}
}

func TestMergeInventoryUnknownHostIsUntouched(t *testing.T) {
	// A host the claim connects but the snapshot does not render (an
	// unmanaged leftover) is skipped, not invented.
	conns := map[string]string{"ghost": `{"host_id":"` + testHostID + `","credential_id":"cred-1","bastion_source":"none"}`}
	secrets := map[string]string{"cred-1": secretJSON(t, credentials.Secret{PrivateKey: "k"})}
	merged, hosts, keys, err := mergeInventory(testDoc(), conns, secrets, "deploy", "/w/keys")
	if err != nil {
		t.Fatalf("mergeInventory: %v", err)
	}
	if _, ok := merged["_meta"].(map[string]any)["hostvars"].(map[string]any)["ghost"]; ok {
		t.Fatal("hosts outside the snapshot must stay out")
	}
	if len(hosts) != 0 || len(keys) != 0 {
		t.Errorf("hosts = %v keys = %v", hosts, keys)
	}
}

func TestMergeInventoryFailsClosed(t *testing.T) {
	conns := map[string]string{"web1": `{"host_id":"broken"`}
	if _, _, _, err := mergeInventory(testDoc(), conns, nil, "", "/w/keys"); err == nil {
		t.Error("a malformed connection JSON must fail")
	}

	// A referenced credential with a broken secret JSON fails the merge.
	conns = map[string]string{"web1": `{"host_id":"` + testHostID + `","credential_id":"cred-1","bastion_source":"none"}`}
	if _, _, _, err := mergeInventory(testDoc(), conns, map[string]string{"cred-1": `{oops`}, "", "/w/keys"); err == nil {
		t.Error("a malformed secret JSON must fail")
	}

	// A referenced credential the claim does not carry fails the merge.
	if _, _, _, err := mergeInventory(testDoc(), conns, nil, "", "/w/keys"); err == nil {
		t.Error("a missing credential secret must fail")
	}
}

func TestMergeInventoryEmptyDocumentIsInert(t *testing.T) {
	// A sync-style claim ships no inventory at all; the merge must neither
	// crash nor invent files.
	merged, hosts, keys, err := mergeInventory(map[string]any{}, nil, nil, "deploy", "/w/keys")
	if err != nil {
		t.Fatalf("mergeInventory: %v", err)
	}
	if len(merged) != 0 || len(hosts) != 0 || len(keys) != 0 {
		t.Errorf("merged = %v hosts = %v keys = %v", merged, hosts, keys)
	}
}
