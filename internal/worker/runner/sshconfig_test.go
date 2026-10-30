// The bastion hop model and the private ssh_config the run generates:
// pure functions, table-driven tested. The rendered hostvars already carry
// the ProxyJump chain (user@addr:port, from internal/sshconf); the claim
// adds the bastion credentials' ids (bastion_credential_ids, additive field
// of internal/jobs' claim payload). The worker turns each hop into a
// ssh_config Host block with its own IdentityFile and rewrites the chain to
// the aliases — the standard pattern for per-hop key files.
package runner

import (
	"strings"
	"testing"
)

func TestParseProxyJump(t *testing.T) {
	cases := []struct {
		name  string
		args  string
		ok    bool
		chain string
		parts []proxyPart
	}{
		{"single hop", "-o ProxyJump=deploy@10.0.0.9:2222", true,
			"deploy@10.0.0.9:2222",
			[]proxyPart{{User: "deploy", Addr: "10.0.0.9", Port: "2222"}}},
		{"two hops", "-o ProxyJump=u1@10.0.0.9:2222,u2@[fd00::9]:2200", true,
			"u1@10.0.0.9:2222,u2@[fd00::9]:2200",
			[]proxyPart{{User: "u1", Addr: "10.0.0.9", Port: "2222"}, {User: "u2", Addr: "[fd00::9]", Port: "2200"}}},
		{"no user", "-o ProxyJump=bastion.example.org", true,
			"bastion.example.org",
			[]proxyPart{{User: "", Addr: "bastion.example.org", Port: ""}}},
		{"no port", "-o ProxyJump=u@h", true, "u@h", []proxyPart{{User: "u", Addr: "h", Port: ""}}},
		{"plain host", "-o ProxyJump=h", true, "h", []proxyPart{{User: "", Addr: "h", Port: ""}}},
		{"not a proxy jump", "-o ControlMaster=auto", false, "", nil},
		{"empty args", "", false, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parts, chain, ok := parseProxyJump(c.args)
			if ok != c.ok {
				t.Fatalf("ok = %t, want %t", ok, c.ok)
			}
			if chain != c.chain {
				t.Errorf("chain = %q, want %q", chain, c.chain)
			}
			if len(parts) != len(c.parts) {
				t.Fatalf("parts = %+v, want %+v", parts, c.parts)
			}
			for i, p := range parts {
				if p != c.parts[i] {
					t.Errorf("part %d = %+v, want %+v", i, p, c.parts[i])
				}
			}
		})
	}
}

func TestHopHostConfig(t *testing.T) {
	// The ssh_config entry a hop resolves to: HostName unbracketed, port
	// defaulting to 22, the identity file of the hop's credential, and the
	// hop's host key trusted on first use inside the run's own known_hosts
	// under the hop's alias (bastions carry no pin).
	cases := []struct {
		name    string
		part    proxyPart
		keyPath string
		want    string
	}{
		{"full", proxyPart{User: "u", Addr: "b.example.org", Port: "2222"}, "/w/keys/cred-1",
			"Host pulsix-jump-b1\n    HostName b.example.org\n    Port 2222\n    User u\n    IdentityFile /w/keys/cred-1\n    IdentitiesOnly yes\n    HostKeyAlias pulsix-jump-b1\n    StrictHostKeyChecking accept-new\n    UserKnownHostsFile /w/known_hosts\n    GlobalKnownHostsFile /dev/null\n"},
		{"no credential", proxyPart{User: "u", Addr: "b.example.org", Port: "2222"}, "",
			"Host pulsix-jump-b1\n    HostName b.example.org\n    Port 2222\n    User u\n    IdentitiesOnly yes\n    HostKeyAlias pulsix-jump-b1\n    StrictHostKeyChecking accept-new\n    UserKnownHostsFile /w/known_hosts\n    GlobalKnownHostsFile /dev/null\n"},
		{"ipv6 unbracketed", proxyPart{User: "u", Addr: "[fd00::9]", Port: "2222"}, "/w/keys/c",
			"Host pulsix-jump-b1\n    HostName fd00::9\n    Port 2222\n    User u\n    IdentityFile /w/keys/c\n    IdentitiesOnly yes\n    HostKeyAlias pulsix-jump-b1\n    StrictHostKeyChecking accept-new\n    UserKnownHostsFile /w/known_hosts\n    GlobalKnownHostsFile /dev/null\n"},
		{"default port", proxyPart{User: "", Addr: "b.example.org", Port: ""}, "/w/keys/c",
			"Host pulsix-jump-b1\n    HostName b.example.org\n    Port 22\n    IdentityFile /w/keys/c\n    IdentitiesOnly yes\n    HostKeyAlias pulsix-jump-b1\n    StrictHostKeyChecking accept-new\n    UserKnownHostsFile /w/known_hosts\n    GlobalKnownHostsFile /dev/null\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := hopHost("b1", c.part, c.keyPath, "")
			if got := h.config("/w/known_hosts"); got != c.want {
				t.Errorf("config =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

func TestSshConfigTextOrdersBlocksAndEndsWithDefaults(t *testing.T) {
	hosts := []proxyHost{
		{id: "b2", part: proxyPart{User: "u", Addr: "10.0.0.9", Port: "2222"}, keyPath: "/w/keys/c2"},
		{id: "b1", part: proxyPart{User: "v", Addr: "10.0.0.8"}, keyPath: "/w/keys/c1"},
	}
	hop := func(id string) string {
		return "    HostKeyAlias pulsix-jump-" + id + "\n    StrictHostKeyChecking accept-new\n    UserKnownHostsFile /w/known_hosts\n    GlobalKnownHostsFile /dev/null\n"
	}
	want := "Host pulsix-jump-b1\n    HostName 10.0.0.8\n    Port 22\n    User v\n    IdentityFile /w/keys/c1\n    IdentitiesOnly yes\n" + hop("b1") +
		"Host pulsix-jump-b2\n    HostName 10.0.0.9\n    Port 2222\n    User u\n    IdentityFile /w/keys/c2\n    IdentitiesOnly yes\n" + hop("b2") +
		// The defaults block comes last (ssh_config: first obtained value
		// wins), for hosts and hops the claim could not alias.
		"Host *\n    StrictHostKeyChecking accept-new\n    UserKnownHostsFile /w/known_hosts\n    GlobalKnownHostsFile /dev/null\n"
	if got := sshConfigText(hosts, "/w/known_hosts"); got != want {
		t.Errorf("sshConfigText =\n%q\nwant\n%q", got, want)
	}
}

func TestRewriteProxyJump(t *testing.T) {
	args := "-o ProxyJump=u1@10.0.0.9:2222,u2@[fd00::9]:2200"
	chain := "u1@10.0.0.9:2222,u2@[fd00::9]:2200"
	got, ok := rewriteProxyJump(args, chain, "pulsix-jump-b1,pulsix-jump-b2")
	if !ok || got != "-o ProxyJump=pulsix-jump-b1,pulsix-jump-b2" {
		t.Errorf("rewrite = %q, %t", got, ok)
	}
	// A chain that is not the one parsed must stay untouched (defensive:
	// never mangle hostvars that do not name the parsed chain).
	if got, ok := rewriteProxyJump("-o ProxyJump=other", "different", "aliases"); ok {
		t.Errorf("a chain that is not present must not rewrite, got %q", got)
	}
}

// --- Pinned bastion hops (backlog 2.40): the ssh_config demands the pinned
// key per hop alias the way pinHosts does for the target.

func TestPinnedHopConfig(t *testing.T) {
	h := hopHost("b1", proxyPart{User: "u", Addr: "b.example.org", Port: "2222"}, "/w/keys/cred-1", "ssh-ed25519 AAAA")
	want := "Host pulsix-jump-b1\n    HostName b.example.org\n    Port 2222\n    User u\n" +
		"    IdentityFile /w/keys/cred-1\n    IdentitiesOnly yes\n    HostKeyAlias pulsix-jump-b1\n" +
		"    StrictHostKeyChecking yes\n    UserKnownHostsFile /w/known_hosts\n    GlobalKnownHostsFile /dev/null\n"
	if got := h.config("/w/known_hosts"); got != want {
		t.Errorf("config =\n%q\nwant\n%q", got, want)
	}
}

func TestPinBastions(t *testing.T) {
	conns := map[string]string{
		"web1": `{"host_id":"h1","bastions":["b1","b2"],"bastion_source":"zone"}`,
	}
	keys := map[string]string{"b1": "ssh-ed25519 AAAA", "b2": "ssh-ed25519 BBBB"}
	pub, err := pinBastions(conns, map[string]string{"b1": "SHA256:AAAA", "b2": "SHA256:BBBB"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	want := "pulsix-jump-b1 ssh-ed25519 AAAA\npulsix-jump-b2 ssh-ed25519 BBBB\n"
	if pub != want {
		t.Errorf("lines = %q, want %q", pub, want)
	}

	// Fail closed: a pinned bastion without its full key cannot be
	// enforced — the claim refuses.
	_, err = pinBastions(conns, map[string]string{"b1": "SHA256:AAAA"}, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "b1") {
		t.Fatalf("missing full key: %v, want an error naming the bastion", err)
	}
	// A key that is not one authorized-key line fails closed too.
	_, err = pinBastions(conns, map[string]string{"b1": "SHA256:AAAA"}, map[string]string{"b1": "two lines\nsecond"})
	if err == nil {
		t.Fatal("a multi-line pinned key must fail closed")
	}
}

// TestMergeInventoryPinnedHopStrictArgs checks that the pinned hop's block
// is built with the pin and that a PINNED password-only bastion fails the
// run closed (the chain could not be aliased, so nothing would enforce the
// pin).
func TestMergeInventoryPinnedHop(t *testing.T) {
	doc := map[string]any{"all": map[string]any{}, "_meta": map[string]any{"hostvars": map[string]any{
		"web1": map[string]any{
			"ansible_host":            "10.0.0.10",
			"ansible_ssh_common_args": "-o ProxyJump=jump@10.0.0.9:2222",
		},
	}}}
	conns := map[string]string{
		"web1": `{"host_id":"u1","credential_id":"c1","bastions":["b1"],"bastion_credential_ids":["c2"],"bastion_source":"zone"}`,
	}
	secrets := map[string]string{"c1": `{"private_key":"host"}`, "c2": `{"password":"hop"}`}
	_, _, _, err := mergeInventory(doc, conns, secrets, "", "/w/keys", map[string]string{"b1": "SHA256:AAAA"})
	if err == nil || !strings.Contains(err.Error(), "b1") {
		t.Fatalf("a pinned password-only bastion must fail the merge closed: %v", err)
	}
}

// TestMergeInventoryFailsClosedWhenAPinnedRouteCannotBeAliased is backlog
// 7.14. A pinned hop's key is only enforced through its alias block, and the
// whole chain falls back to the raw ProxyJump value as soon as one hop cannot
// be scripted, the chain does not match the claim, or the rewrite fails.
// That fallback dials every hop by address under "Host *" with accept-new,
// so a pinned hop elsewhere in the chain silently lost its pin (security
// audit run 1: an impostor of the pinned bastion was accepted). Any route
// that dials a pinned bastion and cannot be fully aliased now fails closed.
func TestMergeInventoryFailsClosedWhenAPinnedRouteCannotBeAliased(t *testing.T) {
	doc := func(args string) map[string]any {
		return map[string]any{"all": map[string]any{}, "_meta": map[string]any{"hostvars": map[string]any{
			"web1": map[string]any{"ansible_host": "10.0.0.10", "ansible_ssh_common_args": args},
		}}}
	}
	secrets := map[string]string{"c1": `{"private_key":"host"}`, "ck": `{"private_key":"hop"}`, "cp": `{"password":"hop"}`}
	pins := map[string]string{"bpin": "SHA256:AAAA"}
	for _, tc := range []struct {
		name, args, conn string
	}{
		{"pinned key hop, then unpinned password hop", "-o ProxyJump=jump@10.0.0.9:22,ops@10.0.0.8:22",
			`{"credential_id":"c1","bastions":["bpin","bpw"],"bastion_credential_ids":["ck","cp"]}`},
		{"unpinned password hop, then pinned key hop", "-o ProxyJump=ops@10.0.0.8:22,jump@10.0.0.9:22",
			`{"credential_id":"c1","bastions":["bpw","bpin"],"bastion_credential_ids":["cp","ck"]}`},
		{"pinned key hop, then credential-less hop", "-o ProxyJump=jump@10.0.0.9:22,ops@10.0.0.8:22",
			`{"credential_id":"c1","bastions":["bpin","bnone"],"bastion_credential_ids":["ck",""]}`},
		{"chain that does not match the claim", "-o ProxyJump=jump@10.0.0.9:22",
			`{"credential_id":"c1","bastions":["bpin","bpw"],"bastion_credential_ids":["ck","cp"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := mergeInventory(doc(tc.args), map[string]string{"web1": tc.conn}, secrets, "", "/w/keys", pins)
			if err == nil || !strings.Contains(err.Error(), "bpin") {
				t.Fatalf("a route dialing pinned bastion bpin that cannot be aliased must fail closed, naming it: %v", err)
			}
		})
	}

	// Without any pin on the route the raw chain stays as rendered: there
	// is nothing to enforce, and an operator-facing value is never mangled.
	const args = "-o ProxyJump=jump@10.0.0.9:22,ops@10.0.0.8:22"
	d := doc(args)
	out, _, _, err := mergeInventory(d, map[string]string{"web1": `{"credential_id":"c1","bastions":["bk","bpw"],"bastion_credential_ids":["ck","cp"]}`}, secrets, "", "/w/keys", pins)
	if err != nil {
		t.Fatalf("an unpinned route that cannot be aliased must pass as rendered: %v", err)
	}
	if got := out["_meta"].(map[string]any)["hostvars"].(map[string]any)["web1"].(map[string]any)["ansible_ssh_common_args"]; got != args {
		t.Fatalf("ansible_ssh_common_args = %v, want the rendered chain unchanged", got)
	}
}
