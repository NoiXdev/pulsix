package runner

import (
	"fmt"
	"slices"
	"strings"
)

// proxyPart is one hop of a rendered ProxyJump chain: user@addr:port, as
// internal/sshconf.ProxyJump spells it. User and Port may be empty (ssh then
// applies its own defaults), Addr keeps IPv6 brackets.
type proxyPart struct {
	User string
	Addr string
	Port string
}

// proxyHost is the ssh_config entry one bastion hop resolves to. Aliases
// derive from the bastion's uuid, so the same bastion used by several hosts
// collapses into one block, keyed by the alias for deterministic output.
type proxyHost struct {
	id      string
	part    proxyPart
	keyPath string // absolute path of the hop's private key; "" without one
}

// bastionAlias is the ssh_config Host alias for a bastion hop. The alias is
// namespaced so it can never collide with an inventory host name.
func bastionAlias(bastionID string) string {
	return "pulsix-jump-" + bastionID
}

// parseProxyJump extracts the hop chain from an ansible_ssh_common_args
// value. It returns the chain exactly as spelled (for the exact rewrite) and
// one part per hop, in order. ok is false when the value carries no
// "-o ProxyJump=" option.
func parseProxyJump(args string) ([]proxyPart, string, bool) {
	const opt = "-o ProxyJump="
	i := strings.Index(args, opt)
	if i < 0 {
		return nil, "", false
	}
	// The chain runs to the value's end, or to the next "-o " option when
	// the hostvar value carries further ssh options (an operator's own
	// value may append them; the renderer writes the chain last).
	chain := strings.TrimSpace(args[i+len(opt):])
	if j := strings.Index(chain, " -o "); j >= 0 {
		chain = chain[:j]
	}
	var parts []proxyPart
	for _, hop := range strings.Split(chain, ",") {
		hop = strings.TrimSpace(hop)
		if hop == "" {
			continue
		}
		parts = append(parts, parseHop(hop))
	}
	if len(parts) == 0 {
		return nil, "", false
	}
	return parts, chain, true
}

// parseHop splits user@addr[:port] with IPv6 bracket support. The user is
// everything before the first "@" (ssh forbids "@" in user names and in
// bracketed addresses alike), the port the segment after the last ":"
// outside brackets.
func parseHop(hop string) proxyPart {
	var part proxyPart
	if user, rest, ok := strings.Cut(hop, "@"); ok {
		part.User, hop = user, rest
	}
	// An address with a port: split on the last ":" that is not inside
	// brackets.
	depth := 0
	last := -1
	for i, r := range hop {
		switch r {
		case '[':
			depth++
		case ']':
			depth--
		case ':':
			if depth == 0 {
				last = i
			}
		}
	}
	if last >= 0 {
		part.Addr, part.Port = hop[:last], hop[last+1:]
		return part
	}
	part.Addr = hop
	return part
}

// hopHost builds the ssh_config entry for one bastion hop: alias from the
// bastion's uuid, address unbracketed, port defaulting to 22, and the
// identity file when the hop's credential is a key (keyPath is the absolute
// path the runner wrote it to; empty when the hop carries no key).
func hopHost(bastionID string, part proxyPart, keyPath string) proxyHost {
	return proxyHost{
		id:      bastionID,
		part:    part,
		keyPath: keyPath,
	}
}

// config renders one Host block of the private ssh_config. Bastions carry
// no pin of their own, so a hop's host key is trusted on first use —
// recorded under the hop's alias in the run's own known_hosts (wiped with
// the work dir) and never the system's. A pinned target is still verified
// end to end: ProxyJump tunnels the target's handshake through the hop,
// so a hop cannot impersonate a pinned host.
func (h proxyHost) config(knownHostsPath string) string {
	port := h.part.Port
	if port == "" {
		port = "22"
	}
	addr := strings.Trim(h.part.Addr, "[]")
	var b strings.Builder
	fmt.Fprintf(&b, "Host %s\n", bastionAlias(h.id))
	fmt.Fprintf(&b, "    HostName %s\n", addr)
	fmt.Fprintf(&b, "    Port %s\n", port)
	if h.part.User != "" {
		fmt.Fprintf(&b, "    User %s\n", h.part.User)
	}
	if h.keyPath != "" {
		fmt.Fprintf(&b, "    IdentityFile %s\n", h.keyPath)
	}
	b.WriteString("    IdentitiesOnly yes\n")
	fmt.Fprintf(&b, "    HostKeyAlias %s\n", bastionAlias(h.id))
	b.WriteString(knownHostsDefaults(knownHostsPath))
	return b.String()
}

// knownHostsDefaults is the host-key posture of every block the run's
// ssh_config writes: trust on first use, into the run's known_hosts only.
// A pinned host overrides StrictHostKeyChecking on the command line
// (command-line options win over ssh_config).
func knownHostsDefaults(knownHostsPath string) string {
	return "    StrictHostKeyChecking accept-new\n" +
		"    UserKnownHostsFile " + knownHostsPath + "\n" +
		"    GlobalKnownHostsFile /dev/null\n"
}

// sshConfigText renders the private ssh_config file: one Host block per
// bastion, ordered by alias so the output is deterministic, then the
// catch-all defaults for every other host and for jump chains the claim
// could not alias (ssh_config applies the first value it obtains, so the
// catch-all must come last). ProxyJump hops read this same file.
func sshConfigText(hosts []proxyHost, knownHostsPath string) string {
	sorted := append([]proxyHost(nil), hosts...)
	slices.SortFunc(sorted, func(a, b proxyHost) int {
		return strings.Compare(bastionAlias(a.id), bastionAlias(b.id))
	})
	var b strings.Builder
	for _, h := range sorted {
		b.WriteString(h.config(knownHostsPath))
	}
	b.WriteString("Host *\n")
	b.WriteString(knownHostsDefaults(knownHostsPath))
	return b.String()
}

// rewriteProxyJump replaces the original ProxyJump chain in the ssh args
// with the alias chain. ok is false (args returned untouched) when the
// parsed chain is not present verbatim — the caller keeps the rendered
// hostvars as they are instead of mangling a value it does not recognize.
func rewriteProxyJump(args, chain, aliasChain string) (string, bool) {
	if chain == "" || aliasChain == "" {
		return args, false
	}
	if !strings.Contains(args, chain) {
		return args, false
	}
	return strings.Replace(args, chain, aliasChain, 1), true
}
