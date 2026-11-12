package agentdist

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// Heredoc delimiters install.sh writes the unit and the plist with.
const (
	unitHeredocDelimiter  = "PULSIX_UNIT_EOF"
	plistHeredocDelimiter = "PULSIX_PLIST_EOF"
)

// RenderShellInstaller fills the embedded installer template with the
// manager's public URL, the systemd unit and the launchd plist.
func RenderShellInstaller(publicURL string) ([]byte, error) {
	return renderShellInstaller(InstallScript(), Unit(), Plist(), publicURL)
}

// renderShellInstaller is RenderShellInstaller over any inputs.
//
// The unit and the plist are inlined into quoted heredocs, so a body whose
// own text carried its heredoc delimiter on a line of its own would close
// the heredoc early and hand everything after it to the installing host's
// shell as commands. All inputs are ours (embedded from deploy/, the URL is
// configuration), so this cannot happen today; the check is here so it
// cannot start happening silently either.
func renderShellInstaller(script, unit, plist []byte, publicURL string) ([]byte, error) {
	// The trailing newline is the template line's own: install.sh has the
	// field on a line between the heredoc delimiters.
	unitText, err := heredocBody("systemd unit", unit, unitHeredocDelimiter)
	if err != nil {
		return nil, err
	}
	plistText, err := heredocBody("launchd plist", plist, plistHeredocDelimiter)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("install.sh").Parse(string(script))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct {
		BaseURL string
		Unit    string
		Plist   string
	}{
		// Trailing slashes are trimmed so the URLs the script builds
		// ("$BASE_URL/agents/bin/...") never carry a double slash: some
		// proxies answer those differently from the canonical path.
		BaseURL: strings.TrimRight(publicURL, "/"),
		Unit:    unitText,
		Plist:   plistText,
	}); err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}
	return buf.Bytes(), nil
}

// heredocBody returns body without its trailing newlines, or an error when
// a line of it is the heredoc delimiter.
func heredocBody(what string, body []byte, delimiter string) (string, error) {
	text := strings.TrimRight(string(body), "\n")
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == delimiter {
			return "", fmt.Errorf("the %s contains the heredoc delimiter %q on its own line", what, delimiter)
		}
	}
	return text, nil
}
