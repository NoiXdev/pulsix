package agentdist

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
	"unicode"
)

// powerShellPlaceholder is the one template field install.ps1 carries.
const powerShellPlaceholder = "{{.BaseURL}}"

// RenderPowerShellInstaller fills the embedded Windows installer template
// with the manager's public URL.
func RenderPowerShellInstaller(publicURL string) ([]byte, error) {
	return renderPowerShellInstaller(PowerShellInstallScript(), publicURL)
}

// renderPowerShellInstaller is RenderPowerShellInstaller over any template.
//
// The URL lands in a single-quoted PowerShell string. PowerShell ends such
// a string at an ASCII apostrophe and at each of the typographic single
// quotes U+2018 to U+201B, so a URL carrying one would hand the rest of it
// to an administrator's shell as code; one is refused rather than escaped,
// because no sane public URL has one. Control characters are refused too.
func renderPowerShellInstaller(script []byte, publicURL string) ([]byte, error) {
	if i := strings.IndexFunc(publicURL, endsPowerShellString); i >= 0 {
		return nil, fmt.Errorf("the public URL %q has a character a single-quoted PowerShell string cannot carry (%U)", publicURL, []rune(publicURL[i:])[0])
	}
	if !bytes.Contains(script, []byte(powerShellPlaceholder)) {
		return nil, fmt.Errorf("the template does not carry %s", powerShellPlaceholder)
	}
	tmpl, err := template.New("install.ps1").Parse(string(script))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var buf bytes.Buffer
	// Trailing slashes are trimmed like install.sh's, so the URLs the
	// script builds never carry a double slash.
	if err := tmpl.Execute(&buf, struct{ BaseURL string }{BaseURL: strings.TrimRight(publicURL, "/")}); err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}
	if bytes.Contains(buf.Bytes(), []byte("{{")) || bytes.Contains(buf.Bytes(), []byte("}}")) {
		return nil, fmt.Errorf("the rendered script still carries template syntax")
	}
	return buf.Bytes(), nil
}

// endsPowerShellString reports whether r ends a single-quoted PowerShell
// string or has no place in a URL at all.
func endsPowerShellString(r rune) bool {
	switch r {
	case '\'', '‘', '’', '‚', '‛':
		return true
	}
	return unicode.IsControl(r)
}
