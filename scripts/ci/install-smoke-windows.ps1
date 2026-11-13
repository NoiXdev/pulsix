# install-smoke-windows.ps1 - CI's install smoke for the Windows agent.
#
# Runs ONLY on a disposable CI runner (the agent-windows job in
# .github/workflows/ci.yml), from an elevated PowerShell 7: it installs the
# PulsixAgent service, writes to Program Files and ProgramData, and removes
# it all again at the end. Never run it on a machine you care about.
#
# The installer is the real one, fetched from installstub
# (internal/agentdist/cmd/installstub), which must already be listening at
# -BaseUrl with the runner's own binary in its dist. The install points the
# agent at manager 127.0.0.1:1, where nothing listens: the agent keeps
# retrying its enrollment, which is enough to show that it runs as a
# service. The sequence, alternating Windows PowerShell 5.1 and
# PowerShell 7 so both dialects run every path of the script:
#
#   1. a fresh install (5.1);
#   2. an upgrade over the running agent (7);
#   3. an uninstall with -KeepData (5.1), which removes the service and the
#      binary but keeps ProgramData\Pulsix with agent.env and the log;
#   4. a reinstall over the kept data directory (7), whose log goes on
#      counting the agent's starts;
#   5. a full uninstall (7), after which service, binary, Program
#      Files\Pulsix and ProgramData\Pulsix are gone;
#   6. the uninstall again (5.1), which finds nothing and still succeeds.
#
# Every install is checked for the service, the start count, the access
# lists and the recovery actions. Remove-Agent removes the agent by hand as
# a fallback, whatever step failed.
#
# Usage: pwsh -File scripts/ci/install-smoke-windows.ps1 [-BaseUrl http://127.0.0.1:18080]
# Exit code 0 on success, 1 on failure.

param(
	[string]$BaseUrl = 'http://127.0.0.1:18080'
)

Set-StrictMode -Version 3
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$ServiceName = 'PulsixAgent'
$AllowedSids = @('S-1-5-18', 'S-1-5-32-544') # SYSTEM, Administrators
$BinDir = Join-Path $env:ProgramFiles 'Pulsix'
$BinPath = Join-Path $BinDir 'pulsix-agent.exe'
$DataDir = Join-Path $env:ProgramData 'Pulsix'
$EnvFile = Join-Path $DataDir 'agent.env'
$LogFile = Join-Path (Join-Path $DataDir 'logs') 'agent.log'
# Well-formed (enrolltoken.ParseAgent accepts it) but unknown to any
# manager: a malformed one would make the agent exit at once.
$Token = 'pxa1.ASNFZ4mrze8BI0VniavN7w.ERERERERERERERERERERERERERERERERERERERERERE.IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI'
# Longer than the service's 5 s restart delay, so an agent that exits and
# is restarted shows up as a second start within it.
$SettleSeconds = 6
$Hint = 'pulsix-agent: the agent is still registered in Pulsix; revoke or delete it on the Agents page'
$NothingToRemove = 'pulsix-agent: nothing to remove'

$work = Join-Path ([IO.Path]::GetTempPath()) ('pulsix-smoke-' + [guid]::NewGuid().ToString('N'))
$tokenFile = Join-Path $work 'token.txt'
$installer = Join-Path $work 'install.ps1'

function Fail([string]$Message) {
	throw "install-smoke: FAIL: $Message"
}

# Read-AgentLog returns the agent's log, or '' while there is none. It
# shares the file with the running agent, which holds it open.
function Read-AgentLog {
	if (-not (Test-Path -LiteralPath $LogFile)) {
		return ''
	}
	$stream = [IO.File]::Open($LogFile, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]'ReadWrite, Delete')
	try {
		$reader = New-Object IO.StreamReader $stream
		return $reader.ReadToEnd()
	} finally {
		$stream.Dispose()
	}
}

function Get-StartCount {
	return [regex]::Matches((Read-AgentLog), 'pulsix-agent starting').Count
}

# Get-ServiceStatus returns the service's status, or 'missing'. It
# disposes what Get-Service returns rather than leave that to the garbage
# collector: a handle on the service still open in this process when the
# uninstall deletes it would keep it marked for deletion instead of gone.
function Get-ServiceStatus {
	$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
	if ($null -eq $service) {
		return 'missing'
	}
	try {
		return [string]$service.Status
	} finally {
		$service.Dispose()
	}
}

# Assert-OnlyAdmins fails unless SYSTEM or Administrators own the path and
# every access rule on it names one of the two. With -Protected it also
# wants inheritance cut off and both of them granted.
function Assert-OnlyAdmins([string]$Path, [switch]$Protected) {
	$acl = Get-Acl -LiteralPath $Path
	$owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
	if ($AllowedSids -notcontains $owner) {
		Fail "$Path is owned by $owner, want SYSTEM or Administrators"
	}
	$sids = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]) |
			ForEach-Object { $_.IdentityReference.Value } | Sort-Object -Unique)
	foreach ($sid in $sids) {
		if ($AllowedSids -notcontains $sid) {
			Fail "$Path has an access rule for $sid; want only SYSTEM and Administrators (rules: $($sids -join ', '))"
		}
	}
	if ($Protected) {
		if (-not $acl.AreAccessRulesProtected) {
			Fail "$Path still inherits access rules from its parent"
		}
		foreach ($sid in $AllowedSids) {
			if ($sids -notcontains $sid) {
				Fail "$Path grants nothing to $sid (rules: $($sids -join ', '))"
			}
		}
	}
}

# Assert-Installed waits for the service to run and for the agent to have
# logged exactly $WantStarts starts, checks that it stays up, and checks
# every file the installer wrote. Any start beyond $WantStarts means the
# agent exited and the service's recovery started it again, which a
# running service alone would hide.
function Assert-Installed([int]$WantStarts) {
	$deadline = (Get-Date).AddSeconds(30)
	while ($true) {
		$status = Get-ServiceStatus
		$starts = Get-StartCount
		if ($starts -gt $WantStarts) {
			Fail "'pulsix-agent starting' was logged $starts times, want exactly ${WantStarts}: the agent exited and was started again"
		}
		if (($status -eq 'Running') -and ($starts -eq $WantStarts)) {
			break
		}
		if ((Get-Date) -gt $deadline) {
			Fail "after 30 s the service is $status and 'pulsix-agent starting' was logged $starts times (want exactly $WantStarts)"
		}
		Start-Sleep -Seconds 1
	}
	Start-Sleep -Seconds $SettleSeconds
	$status = Get-ServiceStatus
	$after = Get-StartCount
	if (($status -ne 'Running') -or ($after -ne $starts)) {
		Fail "the agent did not stay up: $SettleSeconds s later the service is $status and the start count went from $starts to $after"
	}

	if (-not (Test-Path -LiteralPath $BinPath -PathType Leaf)) {
		Fail "$BinPath is missing"
	}
	$config = (& sc.exe qc $ServiceName | Out-String)
	if ($LASTEXITCODE -ne 0) {
		Fail "sc.exe qc $ServiceName exited with $LASTEXITCODE"
	}
	if ($config -notmatch 'AUTO_START') {
		Fail "the service does not start automatically:`n$config"
	}
	if (-not $config.Contains($BinPath)) {
		Fail "the service does not run $BinPath`:`n$config"
	}
	Assert-OnlyAdmins $DataDir -Protected
	Assert-OnlyAdmins $EnvFile
	$envText = [IO.File]::ReadAllText($EnvFile)
	if ($envText -notmatch '(?m)^PULSIX_MANAGER_ADDR=127\.0\.0\.1:1$') {
		Fail "$EnvFile does not name the manager"
	}
	Write-Host "install-smoke: install $WantStarts OK (running, protected, $starts start(s) logged)"
}

# Assert-Recovery fails unless the service restarts the agent 5 s after
# each of its first three failures, resets that count after a day, and
# applies this to the failures the agent reports through its exit code:
# install.ps1's Set-AgentRecovery, which the upgrade must set again after
# clearing it for the stop.
function Assert-Recovery {
	$failure = (& sc.exe qfailure $ServiceName | Out-String)
	if ($LASTEXITCODE -ne 0) {
		Fail "sc.exe qfailure $ServiceName exited with $LASTEXITCODE"
	}
	$restarts = [regex]::Matches($failure, 'RESTART -- Delay = 5000 milliseconds').Count
	$actions = [regex]::Matches($failure, ' -- Delay = ').Count
	if (($restarts -ne 3) -or ($actions -ne 3) -or ($failure -notmatch 'RESET_PERIOD \(in seconds\)\s*:\s*86400\b')) {
		Fail "the recovery actions are not 'reset= 86400 actions= restart/5000/restart/5000/restart/5000':`n$failure"
	}
	$flag = (& sc.exe qfailureflag $ServiceName | Out-String)
	if ($LASTEXITCODE -ne 0) {
		Fail "sc.exe qfailureflag $ServiceName exited with $LASTEXITCODE"
	}
	if ($flag -notmatch 'FAILURE_ACTIONS_ON_NONCRASH_FAILURES\s*:\s*TRUE') {
		Fail "the recovery actions do not apply to the agent's failure exits (failureflag is not 1):`n$flag"
	}
	Write-Host 'install-smoke: recovery actions OK (3 x restart after 5000 ms, reset after 86400 s, failureflag 1)'
}

# Assert-Uninstalled waits for the service to go (sc.exe delete only marks
# it while a program holds it open) and fails unless the binary and its
# folder are gone too, and the data directory is gone or, with -KeepData,
# still there with agent.env, private to SYSTEM and Administrators.
function Assert-Uninstalled([switch]$KeepData) {
	$deadline = (Get-Date).AddSeconds(30)
	while ((Get-ServiceStatus) -ne 'missing') {
		if ((Get-Date) -gt $deadline) {
			Fail "30 s after the uninstall the $ServiceName service still exists ($(Get-ServiceStatus))"
		}
		Start-Sleep -Seconds 1
	}
	foreach ($path in @($BinPath, $BinDir)) {
		if (Test-Path -LiteralPath $path) {
			Fail "$path is still there after the uninstall"
		}
	}
	if ($KeepData) {
		if (-not (Test-Path -LiteralPath $EnvFile -PathType Leaf)) {
			Fail "-KeepData did not keep $EnvFile"
		}
		Assert-OnlyAdmins $DataDir -Protected
	} elseif (Test-Path -LiteralPath $DataDir) {
		Fail "$DataDir is still there after the uninstall"
	}
	Write-Host 'install-smoke: uninstall OK'
}

# Assert-Output fails unless the installer's output holds the line $Line.
function Assert-Output([string]$Output, [string]$Line) {
	if ($Output -notmatch [regex]::Escape($Line)) {
		Fail "install.ps1 did not print '$Line'"
	}
}

# Invoke-Installer downloads install.ps1 from the stub and runs it under
# $Shell with $Arguments, elevated like the runner itself. It echoes the
# installer's output and returns it.
function Invoke-Installer([string]$Shell, [string[]]$Arguments) {
	Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/install.ps1" -OutFile $installer
	$savedModulePath = $env:PSModulePath
	if ($Shell -eq 'powershell.exe') {
		# PowerShell 7's module path would make Windows PowerShell load
		# PowerShell 7's modules; give it the one it starts with on its own.
		$env:PSModulePath = [Environment]::GetEnvironmentVariable('PSModulePath', 'Machine')
	}
	try {
		$output = & $Shell -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $installer @Arguments | Out-String
		$code = $LASTEXITCODE
	} finally {
		$env:PSModulePath = $savedModulePath
	}
	Write-Host $output
	if ($code -ne 0) {
		Fail "install.ps1 $($Arguments -join ' ') under $Shell exited with $code"
	}
	return $output
}

# Install-Agent installs or upgrades the agent under $Shell.
function Install-Agent([string]$Shell) {
	[void](Invoke-Installer $Shell @('-Manager', '127.0.0.1:1', '-Name', 'ci-smoke', '-TokenFile', $tokenFile))
}

function Show-Diagnostics {
	Write-Host "--- sc.exe query $ServiceName"
	& sc.exe query $ServiceName | Out-Host
	Write-Host "--- sc.exe qc $ServiceName"
	& sc.exe qc $ServiceName | Out-Host
	Write-Host "--- sc.exe qfailure $ServiceName"
	& sc.exe qfailure $ServiceName | Out-Host
	Write-Host "--- $LogFile"
	Write-Host (Read-AgentLog)
}

function Remove-Agent {
	Write-Host 'install-smoke: removing the agent'
	$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
	if ($null -ne $service) {
		if ($service.Status -ne 'Stopped') {
			& sc.exe stop $ServiceName | Out-Host
			try {
				$service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
			} catch {
				Write-Warning "the $ServiceName service did not stop within 30 seconds"
			}
		}
		& sc.exe delete $ServiceName | Out-Host
	}
	foreach ($path in @($BinDir, $DataDir, $work)) {
		if (Test-Path -LiteralPath $path) {
			try {
				Remove-Item -LiteralPath $path -Recurse -Force
			} catch {
				Write-Warning "could not remove ${path}: $($_.Exception.Message)"
			}
		}
	}
}

$failed = $false
try {
	# The token file the installer accepts: nobody but SYSTEM and
	# Administrators may read it.
	New-Item -ItemType Directory -Path $work | Out-Null
	[IO.File]::WriteAllText($tokenFile, "$Token`n")
	& icacls.exe $tokenFile /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' | Out-Host
	if ($LASTEXITCODE -ne 0) {
		Fail "icacls could not protect $tokenFile (exit code $LASTEXITCODE)"
	}

	Write-Host 'install-smoke: fresh install under Windows PowerShell 5.1'
	Install-Agent 'powershell.exe'
	Assert-Installed 1
	Assert-Recovery

	Write-Host 'install-smoke: upgrade over the running agent under PowerShell 7'
	$previousStarts = Get-StartCount
	Install-Agent 'pwsh'
	Assert-Installed ($previousStarts + 1)
	Assert-Recovery

	Write-Host 'install-smoke: uninstall with -KeepData under Windows PowerShell 5.1'
	$previousStarts = Get-StartCount
	$output = Invoke-Installer 'powershell.exe' @('-Uninstall', '-KeepData')
	Assert-Output $output $Hint
	Assert-Uninstalled -KeepData

	# The kept data directory keeps the log too, so the count goes on from
	# where it was: a reinstall that started over in a new directory would
	# log a single start.
	Write-Host 'install-smoke: reinstall over the kept data directory under PowerShell 7'
	Install-Agent 'pwsh'
	Assert-Installed ($previousStarts + 1)
	Assert-Recovery

	Write-Host 'install-smoke: uninstall under PowerShell 7'
	$output = Invoke-Installer 'pwsh' @('-Uninstall')
	Assert-Output $output $Hint
	Assert-Uninstalled

	Write-Host 'install-smoke: uninstall again, with nothing installed, under Windows PowerShell 5.1'
	$output = Invoke-Installer 'powershell.exe' @('-Uninstall')
	Assert-Output $output $NothingToRemove

	Write-Host 'install-smoke: PASS'
} catch {
	$failed = $true
	Write-Host "$_"
	Write-Host $_.ScriptStackTrace
	try {
		Show-Diagnostics
	} catch {
		Write-Warning "diagnostics failed: $_"
	}
} finally {
	Remove-Agent
}
if ($failed) {
	exit 1
}
exit 0
