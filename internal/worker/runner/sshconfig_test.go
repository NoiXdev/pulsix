// The bastion hop model and the private ssh_config the run generates:
// pure functions, table-driven tested. The rendered hostvars already carry
// the ProxyJump chain (user@addr:port, from internal/sshconf); the claim
// adds the bastion credentials' ids (bastion_credential_ids, additive field
// of internal/jobs' claim payload). The worker turns each hop into a
// ssh_config Host block with its own IdentityFile and rewrites the chain to
// the aliases — the standard pattern for per-hop key files.
package runner

import (
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
	// defaulting to 22, and the identity file of the hop's credential.
	cases := []struct {
		name    string
		part    proxyPart
		keyPath string
		want    string
	}{
		{"full", proxyPart{User: "u", Addr: "b.example.org", Port: "2222"}, "/w/keys/cred-1",
			"Host pulsix-jump-b1\n    HostName b.example.org\n    Port 2222\n    User u\n    IdentityFile /w/keys/cred-1\n    IdentitiesOnly yes\n    StrictHostKeyChecking accept-new\n"},
		{"no credential", proxyPart{User: "u", Addr: "b.example.org", Port: "2222"}, "",
			"Host pulsix-jump-b1\n    HostName b.example.org\n    Port 2222\n    User u\n    IdentitiesOnly yes\n    StrictHostKeyChecking accept-new\n"},
		{"ipv6 unbracketed", proxyPart{User: "u", Addr: "[fd00::9]", Port: "2222"}, "/w/keys/c",
			"Host pulsix-jump-b1\n    HostName fd00::9\n    Port 2222\n    User u\n    IdentityFile /w/keys/c\n    IdentitiesOnly yes\n    StrictHostKeyChecking accept-new\n"},
		{"default port", proxyPart{User: "", Addr: "b.example.org", Port: ""}, "/w/keys/c",
			"Host pulsix-jump-b1\n    HostName b.example.org\n    Port 22\n    IdentityFile /w/keys/c\n    IdentitiesOnly yes\n    StrictHostKeyChecking accept-new\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := hopHost("b1", c.part, c.keyPath)
			if got := h.String(); got != c.want {
				t.Errorf("config =\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

func TestSshConfigTextOrdersBlocks(t *testing.T) {
	hosts := []proxyHost{
		{id: "b2", part: proxyPart{User: "u", Addr: "10.0.0.9", Port: "2222"}, keyPath: "/w/keys/c2"},
		{id: "b1", part: proxyPart{User: "v", Addr: "10.0.0.8"}, keyPath: "/w/keys/c1"},
	}
	want := "Host pulsix-jump-b1\n    HostName 10.0.0.8\n    Port 22\n    User v\n    IdentityFile /w/keys/c1\n    IdentitiesOnly yes\n    StrictHostKeyChecking accept-new\n" +
		"Host pulsix-jump-b2\n    HostName 10.0.0.9\n    Port 2222\n    User u\n    IdentityFile /w/keys/c2\n    IdentitiesOnly yes\n    StrictHostKeyChecking accept-new\n"
	if got := sshConfigText(hosts); got != want {
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
