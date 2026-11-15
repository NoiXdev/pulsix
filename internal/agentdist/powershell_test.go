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
		"[IO.Directory]::Delete($DataDir, $true)",
		// A service an earlier uninstall left marked for deletion.
		`HKLM:\SYSTEM\CurrentControlSet\Services\$ServiceName`,
		"($entry.DeleteFlag -eq 1)",
		// The wait holds the process itself, not just its id.
		"$process.WaitForExit(30000)",
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

	// An install refuses a service marked for deletion before it
	// downloads or stops anything.
	assertInOrder(t, "install.ps1", main,
		"if ($Uninstall) {",
		"if (Test-AgentServiceMarkedForDeletion) {\n\tStop-Install \"the $ServiceName service is marked for deletion",
		"restart Windows, then run the installer again",
		"$restoreRecovery = $false\n$restartAgent = $false\ntry {",
	)

	// The upgrade: the data directory is checked before the running agent
	// is stopped, its recovery actions are restored in a finally, and the
	// copy follows the wait for the process.
	assertInOrder(t, "install.ps1", main,
		"$restoreRecovery = $false\n$restartAgent = $false\ntry {",
		"Get-FileHash",
		"if (Test-Path -LiteralPath $DataDir) {\n\t\t\tAssert-DataDirReusable\n",
		"$restoreRecovery = $true",
		"Stop-AgentService 'the binary was not replaced'",
		"$restartAgent = $true",
		"Copy-Item -LiteralPath $downloaded -Destination $BinPath -Force",
		"Assert-DataDirReusable",
		"/inheritance:r",
		`Invoke-Sc "$verb $ServiceName $scArgs"`,
		"Set-AgentRecovery",
		"$restoreRecovery = $false",
		"} finally {",
		"if ($restoreRecovery) {",
		"Set-AgentRecovery",
		// A failure after the stop starts the agent again, best effort,
		// and says whether it runs; neither may mask the failure.
		"if ($restartAgent) {",
		"try {\n\t\t\t\tStart-Service -Name $ServiceName\n",
		"the $ServiceName service was started again, check $LogFile in case the agent stopped at once",
		"} catch {\n\t\t\t\tWrite-Warning \"pulsix-agent: the install failed and the $ServiceName service is stopped",
		"# 13. Start.",
		"Start-Service -Name $ServiceName",
	)
	if !strings.Contains(main, "$restoreRecovery = $false\n$restartAgent = $false\ntry {") {
		t.Error("install.ps1 must start with $restartAgent false next to $restoreRecovery")
	}

	// The shared stop: the process id read and the recovery actions
	// cleared before the stop, then the status, then the process. A stop
	// that fails restores the recovery actions before it ends the script.
	assertInOrder(t, "Stop-AgentService", psFunction(t, script, "Stop-AgentService"),
		"Get-CimInstance -ClassName Win32_Service",
		".ProcessId",
		// The process object and a handle on it, taken before the stop:
		// a reused id cannot fake or prolong the wait.
		"$process = Get-Process -Id $processId -ErrorAction SilentlyContinue",
		"[void]$process.Handle",
		`Invoke-Sc ("failure $ServiceName " + 'reset= 0 actions= ""')`,
		"$service.Stop()",
		"$service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))",
		"$exited = $process.WaitForExit(30000)",
		// Only when WaitForExit itself fails.
		"} catch {\n\t\t\t\t$exited = Wait-ProcessExit $processId 30",
		"if (-not $exited) {",
		// The handle is let go once the wait is over.
		"$process.Dispose()",
		"Set-AgentRecovery",
		"Stop-Install",
		// Its ServiceController too, whatever happened.
		"} finally {\n\t\t$service.Dispose()\n\t}",
	)
	// Every ServiceController the installer creates is disposed: a handle
	// still open in the administrator's session would keep a deleted
	// service marked for deletion. Only these two functions create one.
	assertInOrder(t, "Get-AgentServiceStatus", psFunction(t, script, "Get-AgentServiceStatus"),
		"$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue",
		"return $null",
		"try {\n\t\treturn [string]$service.Status\n\t} finally {\n\t\t$service.Dispose()\n\t}",
	)
	if n := strings.Count(script, "Get-Service -Name"); n != 2 {
		t.Errorf("Get-Service must be called only in Get-AgentServiceStatus and Stop-AgentService, found %d calls", n)
	}
	assertInOrder(t, "Test-AgentServiceMarkedForDeletion", psFunction(t, script, "Test-AgentServiceMarkedForDeletion"),
		`Get-ItemProperty -LiteralPath "HKLM:\SYSTEM\CurrentControlSet\Services\$ServiceName" -Name DeleteFlag -ErrorAction SilentlyContinue`,
		"return ($null -ne $entry) -and ($entry.DeleteFlag -eq 1)",
	)
	// The installer's own access checks share the full check.
	assertInOrder(t, "Assert-OnlyAdmins", psFunction(t, script, "Assert-OnlyAdmins"),
		"Get-AdminsOnlyProblem $Path -ExplicitOnly:$ExplicitOnly",
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
		"Assert-RemovableBinDir",
		// Whether the data directory is removed is decided, and checked,
		// before anything changes: one that appears later is never removed.
		"$removeData = (-not $KeepData) -and (Test-Path -LiteralPath $DataDir)",
		"if ($removeData) {\n\t\tAssert-RemovableDataDir $DataDir\n\t}",
		// A service marked for deletion: refused while it runs, else left
		// to go on its own, without sc.exe (error 1072).
		"$status = Get-AgentServiceStatus",
		"if (($null -ne $status) -and (Test-AgentServiceMarkedForDeletion)) {",
		"if ($status -ne 'Stopped') {",
		`Stop-Install "the $ServiceName service is marked for deletion but still running; nothing was removed.`,
		"is already marked for deletion",
		"} elseif ($null -ne $status) {",
		"Stop-AgentService 'nothing was removed'",
		// A failed delete leaves the service stopped: its recovery
		// actions come back, and the message says it is stopped.
		"try {\n\t\t\tInvoke-Sc \"delete $ServiceName\"\n\t\t} catch {",
		"Set-AgentRecovery",
		"the $ServiceName service is stopped and nothing was removed",
		"Remove-Item -LiteralPath $BinPath -Force",
		"Remove-Item -LiteralPath $BinDir -Force",
		"if ($KeepData) {",
		"} elseif ($removeData -and (Test-Path -LiteralPath $DataDir)) {\n\t\t[IO.Directory]::Delete($DataDir, $true)",
		"} elseif (Test-Path -LiteralPath $DataDir) {\n\t\tWrite-Warning",
		"pulsix-agent: nothing to remove",
		"pulsix-agent: the agent is still registered in Pulsix; revoke or delete it on the Agents page",
	)
	if strings.Count(uninstall, "Assert-RemovableDataDir") != 1 || strings.Count(uninstall, "Directory]::Delete(") != 1 {
		t.Error("Uninstall-Agent must check the data directory once and remove it once, both under $removeData")
	}
	if strings.Contains(uninstall, "finally") || strings.Count(uninstall, "Set-AgentRecovery") != 1 {
		t.Error("Uninstall-Agent may set recovery actions only when the delete failed, never in a finally")
	}
	// The data directory is removed only when nobody but SYSTEM and
	// Administrators can create anything in it: every folder in the tree
	// gets the full check (inherited entries included), every file the
	// link check. Otherwise a folder could be swapped for a junction
	// between the check and the removal.
	removable := psFunction(t, script, "Assert-RemovableDataDir")
	assertInOrder(t, "Assert-RemovableDataDir", removable,
		"$problem = Get-AdminsOnlyProblem $Path\n",
		"Stop-Install",
		"nothing was removed. Check the folder and remove it by hand",
		"foreach ($child in @(Get-ChildItem -LiteralPath $Path -Force)) {",
		"if ($child.PSIsContainer) {\n\t\t\tAssert-RemovableDataDir $child.FullName",
		"} elseif (Test-ReparsePoint $child.FullName) {",
		"Stop-Install",
	)
	if strings.Contains(removable, "ExplicitOnly") {
		t.Error("Assert-RemovableDataDir must weigh inherited entries too")
	}
	assertInOrder(t, "Get-AdminsOnlyProblem", psFunction(t, script, "Get-AdminsOnlyProblem"),
		"if (Test-ReparsePoint $Path) {",
		"GetOwner",
		"$AllowedSids -notcontains $owner",
		"$acl.GetAccessRules($true, (-not $ExplicitOnly), [Security.Principal.SecurityIdentifier])",
		"$AllowedSids -notcontains $rule.IdentityReference.Value",
		"return $null",
	)
	// Program Files\Pulsix gets an access list of its own, on every
	// install and upgrade, before the binary is copied in so it inherits
	// it: nothing inherited from Program Files (its CREATOR OWNER entry
	// would hand the creating account full control), SYSTEM and
	// Administrators full control, Users read and execute, owner
	// Administrators. Each icacls call is checked.
	assertInOrder(t, "install.ps1", main,
		"Stop-AgentService 'the binary was not replaced'",
		"New-Item -ItemType Directory -Path $BinDir -Force | Out-Null\n",
		"\t\t& icacls.exe $BinDir /inheritance:r /grant:r \"*S-1-5-18:(OI)(CI)F\" \"*S-1-5-32-544:(OI)(CI)F\" \"*S-1-5-32-545:(OI)(CI)RX\" | Out-Null\n\t\tif ($LASTEXITCODE -ne 0) {\n",
		"Stop-Install \"icacls could not set the access list of $BinDir (exit code $LASTEXITCODE)\"",
		"\t\t& icacls.exe $BinDir /setowner \"*S-1-5-32-544\" | Out-Null\n\t\tif ($LASTEXITCODE -ne 0) {\n",
		"Stop-Install \"icacls could not hand $BinDir to Administrators (exit code $LASTEXITCODE)\"",
		"Copy-Item -LiteralPath $downloaded -Destination $BinPath -Force",
		"} finally {\n\t\tRemove-Item -LiteralPath $tmp",
	)
	if n := strings.Count(script, "icacls.exe $BinDir"); n != 2 {
		t.Errorf("install.ps1 must call icacls on $BinDir exactly twice (access list, owner), found %d", n)
	}
	// Program Files\Pulsix: the same rule as the data directory, where
	// every user may read; the install's own access list passes it. The
	// binary must be a plain file: Remove-Item on a directory junction
	// can recurse into its target once someone confirms.
	binDir := psFunction(t, script, "Assert-RemovableBinDir")
	assertInOrder(t, "Assert-RemovableBinDir", binDir,
		"if (Test-ReparsePoint $BinDir) {",
		"if ($BinDirWriters -notcontains $owner) {",
		"$acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])",
		"PropagationFlags]::InheritOnly",
		"$BinDirWriters -contains $rule.IdentityReference.Value",
		"([int]$rule.FileSystemRights -band $WriteRights) -ne 0",
		"Stop-Install",
		"if ((Test-Path -LiteralPath $BinPath) -and ((Get-Item -LiteralPath $BinPath -Force).PSIsContainer -or (Test-ReparsePoint $BinPath))) {",
		"is not a plain file",
	)
	if strings.Contains(binDir, "$runner") || strings.Contains(binDir, "GetCurrent()") {
		t.Error("Assert-RemovableBinDir must not make an exception for the account running it: the install sets the owner")
	}
	for _, want := range []string{
		"$TrustedInstallerSid = 'S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464'",
		"$BinDirWriters = @($SystemSid, $AdminsSid, $TrustedInstallerSid)",
		"$WriteRights = 0x500D0046",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("install.ps1 must contain %q", want)
		}
	}

	// Only the download directory goes through Remove-Item -Recurse; the
	// data directory goes through Directory.Delete, which deletes a link
	// as the link, once its tree has passed Assert-RemovableDataDir.
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "Remove-Item") && strings.Contains(line, "-Recurse") &&
			line != "Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue" {
			t.Errorf("unexpected recursive removal: %q", line)
		}
		if strings.Contains(line, "Directory]::Delete(") && line != "[IO.Directory]::Delete($DataDir, $true)" {
			t.Errorf("unexpected Directory.Delete: %q", line)
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

// The Windows smoke checks the program folder's access list after every
// install, so a regression fails there rather than at the uninstall.
func TestInstallSmokeWindowsChecksTheProgramFolder(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci", "install-smoke-windows.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	smoke := string(raw)
	for i, r := range smoke {
		if r > 0x7e || (r < 0x20 && r != '\n' && r != '\r' && r != '\t') {
			t.Fatalf("install-smoke-windows.ps1 has a non-ASCII or control character %U at byte %d", r, i)
		}
	}
	assertInOrder(t, "Assert-ProgramFolderAcl", psFunction(t, smoke, "Assert-ProgramFolderAcl"),
		"$acl = Get-Acl -LiteralPath $BinDir",
		"$owner -ne 'S-1-5-32-544'",
		"if (-not $acl.AreAccessRulesProtected) {",
		"'S-1-5-18' = 'FullControl'",
		"'S-1-5-32-544' = 'FullControl'",
		"'S-1-5-32-545' = 'ReadAndExecute, Synchronize'",
		"AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow",
		"$rules.Count -ne $want.Count",
	)
	assertInOrder(t, "Assert-Installed", psFunction(t, smoke, "Assert-Installed"),
		"Fail \"$BinPath is missing\"",
		"Assert-ProgramFolderAcl",
		"Assert-OnlyAdmins $DataDir -Protected",
	)
}

// The Windows smoke's token file: the installer accepts a -TokenFile only
// SYSTEM, Administrators and its owner may read. The smoke replaces the
// file's whole access list rather than granting with icacls /grant:r,
// which keeps every explicit entry it does not name (on the CI runner the
// one for the account that created the file, while Administrators own
// it), and checks the result before the first install. It also has the
// installer refuse a file Users may read, before anything is installed,
// so the rule is seen to stay strict.
func TestInstallSmokeWindowsPreparesTheTokenFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci", "install-smoke-windows.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	smoke := string(raw)
	if regexp.MustCompile(`&\s*icacls\.exe[^\n]*/grant:r`).MatchString(smoke) {
		t.Error("install-smoke-windows.ps1 must not protect a token file with icacls /grant:r: it keeps the entries it does not name")
	}
	assertInOrder(t, "Set-TokenFileAcl", psFunction(t, smoke, "Set-TokenFileAcl"),
		"$acl = New-Object Security.AccessControl.FileSecurity",
		"$acl.SetAccessRuleProtection($true, $false)",
		"$acl.SetOwner((New-Object Security.Principal.SecurityIdentifier 'S-1-5-32-544'))",
		"foreach ($sid in $AllowedSids) {",
		"FileSystemRights]::FullControl",
		"foreach ($sid in $ReaderSids) {",
		"FileSystemRights]::Read,",
		"Set-Acl -LiteralPath $Path -AclObject $acl",
	)
	start := strings.LastIndex(smoke, "\n$failed = $false\n")
	if start < 0 {
		t.Fatal("install-smoke-windows.ps1 must have its main part")
	}
	assertInOrder(t, "the smoke's main part", smoke[start:],
		"Set-TokenFileAcl $openTokenFile @('S-1-5-32-545')",
		"Assert-InstallerRefuses 'powershell.exe' @('-Manager', '127.0.0.1:1', '-Name', 'ci-smoke', '-TokenFile', $openTokenFile) @('is readable by', 'S-1-5-32-545')",
		"if ((Get-ServiceStatus) -ne 'missing') {",
		"Set-TokenFileAcl $tokenFile @()",
		"Assert-OnlyAdmins $tokenFile -Protected",
		"Install-Agent 'powershell.exe'",
	)
	assertInOrder(t, "Assert-InstallerRefuses", psFunction(t, smoke, "Assert-InstallerRefuses"),
		"if ($result.Code -eq 0) {",
		`$flat = $result.Output -replace '_x000[DA]_|\s', ''`,
		`$flat.Contains(($text -replace '\s', ''))`,
	)
}

// Read-TokenFile exempts SYSTEM, Administrators and the file's owner and
// nobody else: not the account running the installer either, which is an
// administrator but is named on the file as an account of its own.
func TestInstallPs1TokenFileReadersAreFixed(t *testing.T) {
	read := psFunction(t, string(PowerShellInstallScript()), "Read-TokenFile")
	assertInOrder(t, "Read-TokenFile", read,
		"$owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value",
		"$tokenReaders = @($AllowedSids) + $owner",
		"$acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])",
		"AccessControlType]::Allow",
		"$tokenReaders -contains $rule.IdentityReference.Value",
		`Stop-Install "token file $Path is readable by`,
	)
	if strings.Contains(read, "GetCurrent()") || strings.Contains(read, "IsInRole") {
		t.Error("Read-TokenFile must not exempt the account running it or judge readers by group membership")
	}
}
