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
		// Two parameter sets: install (the default, -Manager mandatory)
		// and uninstall; mixing them is PowerShell's own error.
		"[CmdletBinding(DefaultParameterSetName = 'Install')]",
		"[Parameter(Mandatory, ParameterSetName = 'Install')][string]$Manager",
		"[Parameter(Mandatory, ParameterSetName = 'Uninstall')][switch]$Uninstall",
		"[Parameter(ParameterSetName = 'Uninstall')][switch]$KeepData",
		"-Uninstall [-KeepData]",
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
		// The upgrade and the uninstall wait for the process, not only for
		// the service status (backlog 3.51).
		"Win32_Service",
		"ProcessId",
		"Get-Process -Id $Id",
		// The copy is retried 5 x 1 s while a scanner holds the file.
		"Copy-Item -LiteralPath $downloaded -Destination $BinPath -Force",
		"if ($attempt -ge 5) {",
		"Start-Sleep -Seconds 1",
		// The recovery actions are cleared while the agent is stopped.
		`reset= 0 actions= ""`,
		// The uninstall: the service, the binary, the data directory.
		`Invoke-Sc "delete $ServiceName"`,
		"Remove-Item -LiteralPath $DataDir -Recurse -Force",
		"pulsix-agent: kept $DataDir (-KeepData)",
		"pulsix-agent: nothing to remove",
		"pulsix-agent: the agent is still registered in Pulsix; revoke or delete it on the Agents page",
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
		// The secrets file is the agent's to check (on every read): an
		// operator's agent.yaml must not stop an upgrade.
		"Where-Object { $_.Name -ne 'agent.yaml' } | ForEach-Object { Assert-OnlyAdmins $_.FullName }",
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

// The order of the upgrade (backlog 3.51) and of the uninstall (backlog
// 3.49), asserted on the script text.
func TestInstallPs1UpgradeAndUninstallOrder(t *testing.T) {
	script := string(PowerShellInstallScript())
	start := strings.Index(script, "\n# 1. Administrator only.\n")
	if start < 0 {
		t.Fatal("install.ps1 must have its numbered main part")
	}
	main := script[start:]

	// The uninstall runs right after the administrator check, before the
	// token, TLS and download, and outside the upgrade's finally.
	assertInOrder(t, "install.ps1", main,
		"WindowsBuiltInRole]::Administrator",
		"if ($Uninstall) {",
		"\tUninstall-Agent\n\treturn\n",
		"-bor 3072",
		"Read-TokenFile $TokenFile",
		"$restoreRecovery = $false",
		"Invoke-WebRequest",
	)

	// The upgrade: the data directory is checked before the running agent
	// is stopped, its recovery actions are restored in a finally, and the
	// copy follows the wait for the process.
	assertInOrder(t, "install.ps1", main,
		"$restoreRecovery = $false\ntry {",
		"Get-FileHash",
		"if (Test-Path -LiteralPath $DataDir) {\n\t\t\tAssert-DataDirReusable\n",
		"$restoreRecovery = $true",
		"Stop-AgentService 'the binary was not replaced'",
		"Copy-Item -LiteralPath $downloaded -Destination $BinPath -Force",
		"Assert-DataDirReusable",
		"/inheritance:r",
		`Invoke-Sc "$verb $ServiceName $scArgs"`,
		"Set-AgentRecovery",
		"$restoreRecovery = $false",
		"} finally {",
		"if ($restoreRecovery) {",
		"Set-AgentRecovery",
		"Start-Service",
	)

	// The shared stop: the process id read and the recovery actions
	// cleared before the stop, then the status, then the process. A stop
	// that fails restores the recovery actions before it ends the script.
	assertInOrder(t, "Stop-AgentService", psFunction(t, script, "Stop-AgentService"),
		"Get-CimInstance -ClassName Win32_Service",
		".ProcessId",
		`Invoke-Sc ("failure $ServiceName " + 'reset= 0 actions= ""')`,
		"$service.Stop()",
		"$service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))",
		"Wait-ProcessExit $processId 30",
		"Set-AgentRecovery",
		"Stop-Install",
	)
	assertInOrder(t, "Set-AgentRecovery", psFunction(t, script, "Set-AgentRecovery"),
		`Invoke-Sc "failure $ServiceName reset= 86400 actions= restart/5000/restart/5000/restart/5000"`,
		`Invoke-Sc "failureflag $ServiceName 1"`,
	)

	// The checks of an existing data directory, run before the stop: a
	// plain folder owned by SYSTEM or Administrators, nothing inside that
	// the check after tightening would refuse.
	assertInOrder(t, "Assert-DataDirReusable", psFunction(t, script, "Assert-DataDirReusable"),
		"PSIsContainer",
		"Test-ReparsePoint $DataDir",
		"GetOwner",
		"$AllowedSids -notcontains $owner",
		"Assert-OnlyAdmins $DataDir -ExplicitOnly",
		"($_.Name -ne 'agent.yaml') -and ($_.Name -ne 'agent.env')",
		"Assert-OnlyAdmins $_.FullName -ExplicitOnly",
	)

	// The uninstall refuses links before it changes anything, stops the
	// agent with the shared helper, deletes the service, then removes the
	// files. It never sets recovery actions on the service it deletes.
	uninstall := psFunction(t, script, "Uninstall-Agent")
	assertInOrder(t, "Uninstall-Agent", uninstall,
		"Assert-NoLink $BinDir",
		"Assert-NoLink $DataDir -Below",
		"Stop-AgentService 'nothing was removed'",
		`Invoke-Sc "delete $ServiceName"`,
		"Remove-Item -LiteralPath $BinPath -Force",
		"Remove-Item -LiteralPath $BinDir -Force",
		"if ($KeepData) {",
		"Remove-Item -LiteralPath $DataDir -Recurse -Force",
		"pulsix-agent: nothing to remove",
		"pulsix-agent: the agent is still registered in Pulsix; revoke or delete it on the Agents page",
	)
	for _, bad := range []string{"finally", "Set-AgentRecovery"} {
		if strings.Contains(uninstall, bad) {
			t.Errorf("Uninstall-Agent must not contain %q: it would set recovery actions on a deleted service", bad)
		}
	}
	assertInOrder(t, "Assert-NoLink", psFunction(t, script, "Assert-NoLink"),
		"if (Test-ReparsePoint $Path) {",
		"Stop-Install",
		"Assert-NoLink $child.FullName -Below",
	)

	// Only the data directory (after Assert-NoLink) and the download
	// directory are removed recursively.
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "Remove-Item") || !strings.Contains(line, "-Recurse") {
			continue
		}
		if line != "Remove-Item -LiteralPath $DataDir -Recurse -Force" &&
			line != "Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue" {
			t.Errorf("unexpected recursive removal: %q", line)
		}
	}
}

// psFunction returns the text of the PowerShell function name: from its
// "function name" line up to the next line that is a lone "}".
func psFunction(t *testing.T, script, name string) string {
	t.Helper()
	start := -1
	for _, head := range []string{"\nfunction " + name + " {\n", "\nfunction " + name + "("} {
		if i := strings.Index(script, head); i >= 0 {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("install.ps1 must define the function %s", name)
	}
	body := script[start+1:]
	end := strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatalf("the function %s has no closing brace", name)
	}
	return body[:end]
}
