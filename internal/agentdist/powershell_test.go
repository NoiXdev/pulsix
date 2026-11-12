package agentdist

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func renderPowerShellReal(t *testing.T) string {
	t.Helper()
	out, err := RenderPowerShellInstaller("https://manager.example/")
	if err != nil {
		t.Fatalf("RenderPowerShellInstaller: %v", err)
	}
	return string(out)
}

func TestRenderPowerShellInstaller(t *testing.T) {
	out := renderPowerShellReal(t)
	for _, want := range []string{
		// Trimmed, like install.sh: the URLs the script builds never
		// carry a double slash.
		"$BaseUrl = 'https://manager.example'",
		"PulsixAgent",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered installer must contain %q", want)
		}
	}
	if strings.Contains(out, "{{") || strings.Contains(out, "}}") {
		t.Error("the rendered installer still carries template syntax")
	}
}

// The base URL lands in a single-quoted PowerShell string. PowerShell ends
// such a string at an ASCII apostrophe and at each of the four typographic
// single quotes, so a URL carrying one would end the string early and hand
// the rest of it to an administrator's shell as code.
func TestRenderPowerShellInstallerRefusesAURLThatEndsTheString(t *testing.T) {
	for _, url := range []string{
		"https://m.example/'; Remove-Item C:\\ -Recurse; '",
		"https://m.example/\u2018x",
		"https://m.example/\u2019x",
		"https://m.example/\u201ax",
		"https://m.example/\u201bx",
		"https://m.example/\nx",
		"https://m.example/\rx",
		"https://m.example/\x00x",
	} {
		if _, err := RenderPowerShellInstaller(url); err == nil {
			t.Errorf("RenderPowerShellInstaller(%q) succeeded, want an error", url)
		}
	}
}

// The placeholder guard: a template that lost its placeholder would serve
// a script with no manager to download from, and a URL that smuggles in
// template syntax would leave it in the output.
func TestRenderPowerShellInstallerGuardsThePlaceholder(t *testing.T) {
	if _, err := renderPowerShellInstaller([]byte("$BaseUrl = 'https://hard-coded'\n"), "https://m.example"); err == nil {
		t.Error("render accepted a template without the BaseURL placeholder")
	}
	if _, err := renderPowerShellInstaller(PowerShellInstallScript(), "https://m.example/{{x}}"); err == nil {
		t.Error("render accepted a URL that leaves template syntax in the output")
	}
}

// Windows PowerShell 5.1 reads a script without a byte order mark in the
// system's ANSI code page, so the script stays ASCII to read the same
// everywhere. A carriage return is tolerated: a Windows checkout may
// convert the line endings.
func TestInstallPs1IsASCII(t *testing.T) {
	for i, r := range string(PowerShellInstallScript()) {
		if r > 0x7e || (r < 0x20 && r != '\n' && r != '\r' && r != '\t') {
			t.Fatalf("install.ps1 has a non-ASCII or control character %U at byte %d", r, i)
		}
	}
}

func TestInstallPs1Shape(t *testing.T) {
	script := string(PowerShellInstallScript())
	for _, want := range []string{
		"[Parameter(Mandatory)][string]$Manager",
		"[string]$ServerName",
		"[string]$Name",
		"[string]$Token",
		"[string]$TokenFile",
		"Set-StrictMode -Version 3",
		"$ErrorActionPreference = 'Stop'",
		"WindowsBuiltInRole]::Administrator",
		"-bor 3072",
		"-AsSecureString",
		"SecureStringToBSTR",
		"PtrToStringBSTR",
		"ZeroFreeBSTR",
		// The token file's readers: SYSTEM, Administrators and its owner,
		// nobody else.
		"$tokenReaders = @($AllowedSids) + $owner",
		"$tokenReaders -contains $rule.IdentityReference.Value",
		"PROCESSOR_ARCHITECTURE",
		"ARM64",
		"AMD64",
		"/agents/bin/windows/",
		"-UseBasicParsing",
		"Get-FileHash",
		"-Algorithm SHA256",
		"WaitForStatus",
		"pulsix-agent.exe",
		"UTF8Encoding $false",
		"WriteAllText",
		"/inheritance:r",
		`"*S-1-5-18:(OI)(CI)F"`,
		`"*S-1-5-32-544:(OI)(CI)F"`,
		`/setowner "*S-1-5-32-544"`,
		"$LASTEXITCODE",
		"PULSIX_MANAGER_ADDR=",
		"PULSIX_ENROLL_TOKEN=",
		"PULSIX_AGENT_NAME=",
		"PULSIX_MANAGER_SERVER_NAME=",
		`binPath= "\"`,
		`\" --env-file \"`,
		`\" --log-file \"`,
		`start= delayed-auto DisplayName= "Pulsix agent"`,
		"'create'",
		"'config'",
		"reset= 86400 actions= restart/5000/restart/5000/restart/5000",
		"failureflag",
		"Start-Service",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("install.ps1 must contain %q", want)
		}
	}
	for _, bad := range []struct{ re, why string }{
		// Taking ownership recursively would launder files a squatter
		// planted in a directory it created first.
		{`/setowner[^\n]*/T\b`, "a recursive /setowner"},
		// Windows PowerShell 5.1 has neither.
		{`\?\?`, "the ?? operator"},
		{`-AsPlainText`, "-AsPlainText"},
	} {
		if regexp.MustCompile(bad.re).MatchString(script) {
			t.Errorf("install.ps1 must not use %s", bad.why)
		}
	}
}

// The script cannot run here, but where PowerShell is installed it can at
// least be parsed. CI's Windows job runs it for real.
func TestInstallPs1Parses(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("no pwsh on this system")
	}
	path := filepath.Join(t.TempDir(), "install.ps1")
	if err := os.WriteFile(path, []byte(renderPowerShellReal(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	// The path is a test temp directory: no single quote in it.
	check := `$e = $null; [void][System.Management.Automation.Language.Parser]::ParseFile('` + path +
		`', [ref]$null, [ref]$e); if ($e.Count) { $e | ForEach-Object { $_.ToString() }; exit 1 }`
	if out, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", check).CombinedOutput(); err != nil {
		t.Fatalf("parse install.ps1: %v\n%s", err, out)
	}
}
