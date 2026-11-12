package api

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// The two install lines the Add-agent dialog shows next to a new token:
// the shell command for Linux and macOS and the PowerShell command for
// Windows. Both are built from the same parts.

// agentInstallCommand is the one line the Add-agent dialog shows next to
// the token: the manager's own install script and the gRPC endpoint the
// agent dials (the shared listener workers use). The token is not in it;
// the installer asks for it. The
// certificate's name is passed only when it differs from the address's
// host — that is the case the agent cannot infer, and spelling it out
// always would make the common command noisier than it needs to be.
func agentInstallCommand(deps Deps, nameHint string) string {
	base, addr := agentInstallParts(deps)
	// The token stays off the command line (backlog 7.30): an argument of
	// the root installer is in the process list, readable by every local
	// user of the host while the install runs. install.sh asks for it on
	// the terminal instead, and the dialog shows it to copy from.
	cmd := fmt.Sprintf("curl -fsSL %s/install.sh | sudo sh -s -- --manager %s", base, shellArg(addr))
	if name := agentInstallServerName(deps, addr); name != "" {
		cmd += " --server-name " + shellArg(name)
	}
	if nameHint != "" {
		cmd += " --name " + shellArg(nameHint)
	}
	return cmd
}

// agentInstallCommandWindows is agentInstallCommand's Windows counterpart:
// the line to paste into a PowerShell window opened as administrator. The
// leading statement adds TLS 1.2 to what Windows PowerShell 5.1 offers
// before the first download (older builds offer only 1.0 and 1.1, which
// current proxies refuse), and the script block form passes parameters to
// the downloaded script without saving it. The token stays off the line
// here too: install.ps1 asks for it.
func agentInstallCommandWindows(deps Deps, nameHint string) string {
	base, addr := agentInstallParts(deps)
	cmd := "[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072; " +
		"& ([scriptblock]::Create((irm " + base + "/install.ps1))) -Manager " + powerShellArg(addr)
	if name := agentInstallServerName(deps, addr); name != "" {
		cmd += " -ServerName " + powerShellArg(name)
	}
	if nameHint != "" {
		cmd += " -Name " + powerShellArg(nameHint)
	}
	return cmd
}

// agentInstallParts are the manager URL and the gRPC address both install
// commands are built from, with placeholders where the manager does not
// know them.
func agentInstallParts(deps Deps) (base, addr string) {
	base = strings.TrimRight(deps.PublicURL, "/")
	if base == "" {
		base = "https://<manager>"
	}
	addr = deps.WorkerEndpoint.Addr
	if addr == "" {
		addr = "<manager>:8444"
	}
	return base, addr
}

// agentInstallServerName is the certificate name to pass, or "" when it is
// the dialed host anyway.
func agentInstallServerName(deps Deps, addr string) string {
	if name := deps.WorkerEndpoint.ServerName; name != "" && name != hostOf(addr) {
		return name
	}
	return ""
}

// plainShellArg is what a POSIX shell takes as one literal word without
// quotes. A leading dash is excluded separately and quoted only so the line reads
// unambiguously; quoting does not stop option parsing, and the installer takes
// the next word as the value anyway.
var plainShellArg = regexp.MustCompile(`^[A-Za-z0-9._:/@%+=,-]+$`)

// shellArg returns s as one literal POSIX shell word: bare when it is plain
// (the common case), single-quoted otherwise with each apostrophe
// written as quote, backslash, quote, quote.
// The line runs as root once an administrator pastes it, so nothing an
// operator typed may be read as shell syntax.
func shellArg(s string) string {
	if plainShellArg.MatchString(s) && !strings.HasPrefix(s, "-") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// plainPowerShellArg is what PowerShell takes as one literal string
// argument without quotes. It starts with a letter because PowerShell reads
// a bare argument that starts with a digit as a number when it can (007 is
// 7, 1kb is 1024) and one that starts with a dash as a parameter name.
var plainPowerShellArg = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:-]*$`)

// powerShellArg returns s as one literal PowerShell argument: bare when it
// is a plain word (the common case, and what the dialog reads best as),
// single-quoted otherwise. The name hint is free text an operator typed and
// an administrator pastes, so it must never be read as code. Inside single
// quotes PowerShell treats the ASCII apostrophe and the typographic single
// quotes U+2018 to U+201B alike, each escaped by doubling.
func powerShellArg(s string) string {
	if plainPowerShellArg.MatchString(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\'', '\u2018', '\u2019', '\u201a', '\u201b':
			b.WriteRune(r)
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

// hostOf is the host part of a host:port, or the input when it carries no
// port (then it is already a bare host).
func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
