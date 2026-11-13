# The Pulsix agent installer for Windows: downloads the agent binary from
# the manager it is being installed against, writes the environment file
# carrying the enrollment token into the agent's data directory, and
# installs and starts the PulsixAgent service.
#
# The manager serves this script templated (GET /install.ps1): $BaseUrl is
# the manager's own public URL. This file is the canonical copy;
# internal/agentdist embeds an identical one and a test keeps the two equal.
# It runs in Windows PowerShell 5.1 and in PowerShell 7, and stays ASCII so
# both read it the same.
#
# Usage, in a PowerShell window opened as administrator:
#   [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072; & ([scriptblock]::Create((irm https://<manager>/install.ps1))) -Manager <manager>:8444 [-ServerName <manager certificate name>] [-Name <agent name>] [-TokenFile <path> | -Token pxa1.<id>.<secret>.<pin>]
#   [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072; & ([scriptblock]::Create((irm https://<manager>/install.ps1))) -Uninstall [-KeepData]
#
# The token and manager address are the two values the Agents page prints.
# Without -TokenFile or -Token the script asks for the token and hides the
# input. That is the default on purpose: a token typed on the command line
# stays in the PowerShell history and, where script block logging is on,
# in the event log. -TokenFile names a file only SYSTEM, Administrators and
# its owner can read, for unattended installs; -Token exists for automation
# and keeps those drawbacks.
# -ServerName is the manager gRPC certificate's DNS name for deployments
# where it is not the host being dialed.
# The token is written into %ProgramData%\Pulsix\agent.env, which only
# SYSTEM and Administrators can read (the agent refuses anything looser),
# and the agent forgets it as soon as a valid identity exists. Running the
# script again upgrades the agent and keeps its identity.
#
# -Uninstall removes the agent from the host (backlog 3.49): it stops and
# deletes the PulsixAgent service and removes the binary and the data
# directory (identity, certificate, agent.env, agent.yaml, logs).
# -KeepData keeps the data directory, so a later install reuses the
# identity. It needs no manager and no token, and removing something not
# installed succeeds quietly. The agent stays registered in Pulsix: revoke
# or delete it on the Agents page.

[CmdletBinding(DefaultParameterSetName = 'Install')]
param(
	[Parameter(Mandatory, ParameterSetName = 'Install')][string]$Manager,
	[Parameter(ParameterSetName = 'Install')][string]$ServerName,
	[Parameter(ParameterSetName = 'Install')][string]$Name,
	[Parameter(ParameterSetName = 'Install')][string]$Token,
	[Parameter(ParameterSetName = 'Install')][string]$TokenFile,
	[Parameter(Mandatory, ParameterSetName = 'Uninstall')][switch]$Uninstall,
	[Parameter(ParameterSetName = 'Uninstall')][switch]$KeepData
)

Set-StrictMode -Version 3
$ErrorActionPreference = 'Stop'
# Windows PowerShell 5.1 redraws a progress bar for every block it
# downloads, which turns a download of megabytes into minutes.
$ProgressPreference = 'SilentlyContinue'

$BaseUrl = '{{.BaseURL}}'
$ServiceName = 'PulsixAgent'
$SystemSid = 'S-1-5-18'
$AdminsSid = 'S-1-5-32-544'
$AllowedSids = @($SystemSid, $AdminsSid)

# Stop-Install ends the script with an error. It throws rather than exits:
# the script runs inside the administrator's own PowerShell window, which
# exit would close.
function Stop-Install([string]$Message) {
	throw "install.ps1: $Message"
}

# Format-Sid names a SID for a message, with its account name if it has one.
function Format-Sid([string]$Sid) {
	try {
		$account = (New-Object Security.Principal.SecurityIdentifier $Sid).Translate([Security.Principal.NTAccount]).Value
		return "$account ($Sid)"
	} catch {
		return $Sid
	}
}

function Test-ReparsePoint([string]$Path) {
	return (Get-Item -LiteralPath $Path -Force).Attributes.HasFlag([IO.FileAttributes]::ReparsePoint)
}

# Assert-OnlyAdmins stops unless SYSTEM or Administrators own the path and
# nobody else has an allow entry on it. The agent checks exactly this for
# its data directory and everything directly inside it, and refuses to
# start otherwise; checking it here shows the problem at install time.
# The one exception is agent.yaml, the secrets file the operator writes by
# hand: the agent checks it on every read and keeps running without
# secrets when it fails, so it does not stop an upgrade here either.
# -ExplicitOnly leaves inherited entries out, for the check before the
# data directory is tightened, which drops them.
function Assert-OnlyAdmins([string]$Path, [switch]$ExplicitOnly) {
	if (Test-ReparsePoint $Path) {
		Stop-Install "$Path is a link (symlink or junction); remove it and run the installer again"
	}
	$acl = Get-Acl -LiteralPath $Path
	$owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
	if ($AllowedSids -notcontains $owner) {
		Stop-Install "$Path is owned by $(Format-Sid $owner), not by SYSTEM or Administrators; remove it and run the installer again"
	}
	foreach ($rule in $acl.GetAccessRules($true, (-not $ExplicitOnly), [Security.Principal.SecurityIdentifier])) {
		if ($rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) {
			continue
		}
		if ($AllowedSids -notcontains $rule.IdentityReference.Value) {
			Stop-Install "$Path grants access to $(Format-Sid $rule.IdentityReference.Value); remove it and run the installer again"
		}
	}
}

# Assert-DataDirReusable stops unless the existing data directory can be
# taken over. Any user may create folders under ProgramData, so it is only
# taken over when it is a plain folder that SYSTEM or Administrators own:
# one created by someone else may hold files they planted, and taking
# ownership of those would hide where they came from. Nothing in it may
# fail the check that follows the tightening (Assert-OnlyAdmins), except
# agent.env, which is rewritten, and agent.yaml, which is the agent's to
# check. Inherited entries are left out: the tightening drops them, so a
# folder an administrator created by hand still passes. The upgrade runs
# this before it stops the running agent, so a problem leaves that agent
# running (backlog 3.51), and again right before the tightening.
function Assert-DataDirReusable {
	if ((-not (Get-Item -LiteralPath $DataDir -Force).PSIsContainer) -or (Test-ReparsePoint $DataDir)) {
		Stop-Install "$DataDir is not a plain folder; remove it and run the installer again"
	}
	try {
		$owner = (Get-Acl -LiteralPath $DataDir).GetOwner([Security.Principal.SecurityIdentifier]).Value
	} catch {
		Stop-Install "cannot read the owner of $DataDir ($($_.Exception.Message)); remove the folder and run the installer again"
	}
	if ($AllowedSids -notcontains $owner) {
		Stop-Install "$DataDir is owned by $(Format-Sid $owner), not by SYSTEM or Administrators, so someone else may have created it; remove the folder and run the installer again"
	}
	Assert-OnlyAdmins $DataDir -ExplicitOnly
	Get-ChildItem -LiteralPath $DataDir -Force | Where-Object { ($_.Name -ne 'agent.yaml') -and ($_.Name -ne 'agent.env') } | ForEach-Object { Assert-OnlyAdmins $_.FullName -ExplicitOnly }
}

# Read-TokenFile reads the token from the first line of a regular file that
# nobody but SYSTEM, Administrators and the file's owner can read: any
# other allow entry with a read right stops the install. Deny entries are
# not weighed; they cannot make a file readable.
function Read-TokenFile([string]$Path) {
	$item = Get-Item -LiteralPath $Path -Force
	if ($item.PSIsContainer -or (Test-ReparsePoint $Path)) {
		Stop-Install "token file $Path is not a regular file"
	}
	$acl = Get-Acl -LiteralPath $Path
	$owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
	$tokenReaders = @($AllowedSids) + $owner
	foreach ($rule in $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])) {
		if ($rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) {
			continue
		}
		if ($tokenReaders -contains $rule.IdentityReference.Value) {
			continue
		}
		# ReadData (0x1), GENERIC_ALL (0x10000000) or GENERIC_READ
		# (0x80000000, the sign bit of the 32-bit mask).
		$mask = [int]$rule.FileSystemRights
		if ((($mask -band 0x1) -ne 0) -or (($mask -band 0x10000000) -ne 0) -or ($mask -lt 0)) {
			Stop-Install "token file $Path is readable by $(Format-Sid $rule.IdentityReference.Value); let only SYSTEM, Administrators and its owner read it"
		}
	}
	$line = Get-Content -LiteralPath $Path -TotalCount 1
	if ($null -eq $line) {
		return ''
	}
	return [string]$line
}

# Read-TokenHidden asks for the token without echoing it.
function Read-TokenHidden {
	$secure = Read-Host -Prompt 'Enrollment token (input hidden)' -AsSecureString
	$bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
	try {
		return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
	} finally {
		[Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
	}
}

# Invoke-Sc runs sc.exe and stops on a non-zero exit code. The command line
# is handed over verbatim: the binary path needs quotes inside a quoted
# argument, and Windows PowerShell 5.1 and PowerShell 7 pass embedded quotes
# to native programs differently, so PowerShell's own argument passing
# cannot be relied on for it.
function Invoke-Sc([string]$Arguments) {
	$info = New-Object Diagnostics.ProcessStartInfo
	$info.FileName = Join-Path ([Environment]::SystemDirectory) 'sc.exe'
	$info.Arguments = $Arguments
	$info.UseShellExecute = $false
	$info.RedirectStandardOutput = $true
	$info.RedirectStandardError = $true
	$process = [Diagnostics.Process]::Start($info)
	$stdout = $process.StandardOutput.ReadToEnd()
	$stderr = $process.StandardError.ReadToEnd()
	$process.WaitForExit()
	if ($process.ExitCode -ne 0) {
		Stop-Install "sc.exe $Arguments failed with exit code $($process.ExitCode): $($stdout.Trim()) $($stderr.Trim())"
	}
}

# Set-AgentRecovery: restart after 5 seconds on every failure, like the
# Linux unit's Restart=on-failure. The agent reports its failures as a
# service exit code rather than by crashing, and the service manager only
# applies the recovery actions to those with the failure flag set.
function Set-AgentRecovery {
	Invoke-Sc "failure $ServiceName reset= 86400 actions= restart/5000/restart/5000/restart/5000"
	Invoke-Sc "failureflag $ServiceName 1"
}

# Wait-ProcessExit waits up to $Seconds for the process $Id to exit and
# says whether it did.
function Wait-ProcessExit([int]$Id, [int]$Seconds) {
	$deadline = (Get-Date).AddSeconds($Seconds)
	while ($null -ne (Get-Process -Id $Id -ErrorAction SilentlyContinue)) {
		if ((Get-Date) -ge $deadline) {
			return $false
		}
		Start-Sleep -Milliseconds 250
	}
	return $true
}

# Stop-AgentService stops the PulsixAgent service, if there is one, and
# waits until its process has exited, so its binary can be replaced or
# removed (backlog 3.51). The service manager can report Stopped while the
# process is still exiting, so the process id is read before the stop and
# waited for after it. The recovery actions are cleared first, or the
# service manager could restart an agent that exits with a failure while
# it stops; the caller sets them again (Set-AgentRecovery) or deletes the
# service. A stop that fails restores them itself and ends the script,
# with $Unchanged saying what was left as it was.
function Stop-AgentService([string]$Unchanged) {
	$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
	if ($null -eq $service) {
		return
	}
	try {
		$processId = 0
		$entry = Get-CimInstance -ClassName Win32_Service -Filter "Name='$ServiceName'"
		if ($null -ne $entry) {
			$processId = [int]$entry.ProcessId
		}
	} catch {
		Stop-Install "cannot read the process id of the $ServiceName service ($($_.Exception.Message)); $Unchanged"
	}
	Invoke-Sc ("failure $ServiceName " + 'reset= 0 actions= ""')
	$problem = $null
	if ($service.Status -ne 'Stopped') {
		Write-Host "pulsix-agent: stopping the running $ServiceName service"
		try {
			if ($service.Status -ne 'StopPending') {
				$service.Stop()
			}
			$service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
		} catch {
			$problem = "the running $ServiceName service did not stop within 30 seconds; $Unchanged. Stop it (Stop-Service $ServiceName) and try again"
		}
	}
	if (($null -eq $problem) -and ($processId -ne 0) -and (-not (Wait-ProcessExit $processId 30))) {
		$problem = "the $ServiceName process ($processId) did not exit within 30 seconds of the stop; $Unchanged. End it (Stop-Process -Id $processId) and try again"
	}
	if ($null -ne $problem) {
		try {
			Set-AgentRecovery
		} catch {
			Write-Warning "pulsix-agent: could not restore the recovery actions of $ServiceName ($($_.Exception.Message))"
		}
		Stop-Install $problem
	}
}

# Assert-NoLink stops the uninstall, before anything is changed, when $Path
# is a symlink or junction, and with -Below when anything inside it is:
# Windows PowerShell 5.1's Remove-Item -Recurse can follow a junction into
# its target and delete files outside the agent's folders. The walk does
# not enter a link; it stops at the first one.
function Assert-NoLink([string]$Path, [switch]$Below) {
	if (-not (Test-Path -LiteralPath $Path)) {
		return
	}
	if (Test-ReparsePoint $Path) {
		Stop-Install "$Path is a link (symlink or junction); nothing was removed. Remove the link itself and run the uninstall again"
	}
	if ($Below -and (Get-Item -LiteralPath $Path -Force).PSIsContainer) {
		foreach ($child in @(Get-ChildItem -LiteralPath $Path -Force)) {
			Assert-NoLink $child.FullName -Below
		}
	}
}

# Uninstall-Agent removes the agent (backlog 3.49): it stops the service
# and waits for its process (Stop-AgentService), deletes the service, then
# removes the binary, Program Files\Pulsix once it is empty, and the data
# directory unless -KeepData keeps it. A service that does not stop ends
# the uninstall before anything is removed. Every step tolerates its
# target being absent.
function Uninstall-Agent {
	Assert-NoLink $BinDir
	if (-not $KeepData) {
		Assert-NoLink $DataDir -Below
	}
	$removed = $false
	if ($null -ne (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
		Stop-AgentService 'nothing was removed'
		Invoke-Sc "delete $ServiceName"
		Write-Host "pulsix-agent: stopped and deleted the $ServiceName service"
		$removed = $true
	}
	if (Test-Path -LiteralPath $BinPath) {
		Remove-Item -LiteralPath $BinPath -Force
		Write-Host "pulsix-agent: removed $BinPath"
		$removed = $true
	}
	if ((Test-Path -LiteralPath $BinDir) -and ($null -eq (Get-ChildItem -LiteralPath $BinDir -Force | Select-Object -First 1))) {
		Remove-Item -LiteralPath $BinDir -Force
		Write-Host "pulsix-agent: removed $BinDir"
		$removed = $true
	}
	if ($KeepData) {
		if (Test-Path -LiteralPath $DataDir) {
			Write-Host "pulsix-agent: kept $DataDir (-KeepData)"
		}
	} elseif (Test-Path -LiteralPath $DataDir) {
		Remove-Item -LiteralPath $DataDir -Recurse -Force
		Write-Host "pulsix-agent: removed $DataDir"
		$removed = $true
	}
	if (-not $removed) {
		Write-Host 'pulsix-agent: nothing to remove'
		return
	}
	Write-Host 'pulsix-agent: the agent is still registered in Pulsix; revoke or delete it on the Agents page'
}

# 1. Administrator only.
$principal = New-Object Security.Principal.WindowsPrincipal ([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
	Stop-Install 'must run as administrator (open PowerShell with "Run as administrator")'
}

# 2. Where things go: the 64-bit Program Files even from a 32-bit
# PowerShell, and the data directory the agent itself defaults to.
$programFiles = [Environment]::GetEnvironmentVariable('ProgramW6432')
if (-not $programFiles) {
	$programFiles = [Environment]::GetEnvironmentVariable('ProgramFiles')
}
$BinDir = Join-Path $programFiles 'Pulsix'
$BinPath = Join-Path $BinDir 'pulsix-agent.exe'
$programData = [Environment]::GetEnvironmentVariable('ProgramData')
if (-not $programData) {
	$programData = 'C:\ProgramData'
}
$DataDir = Join-Path $programData 'Pulsix'
$EnvFile = Join-Path $DataDir 'agent.env'
$LogFile = Join-Path (Join-Path $DataDir 'logs') 'agent.log'

# 3. The uninstall needs nothing more: no manager, no token, no download.
if ($Uninstall) {
	Uninstall-Agent
	return
}
if ($KeepData) {
	Stop-Install '-KeepData only goes with -Uninstall'
}

# 4. TLS 1.2 for the downloads, added to what is already enabled, in case
# the script was started without the dialog's leading statement.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072

# A line break in any value would add lines of its own to agent.env.
foreach ($pair in @(@('-Manager', $Manager), @('-ServerName', $ServerName), @('-Name', $Name))) {
	if ([string]$pair[1] -match '[\r\n]') {
		Stop-Install "$($pair[0]) must be a single line"
	}
}

# 5. The token.
if ($TokenFile) {
	if ($Token) {
		Stop-Install '-Token and -TokenFile exclude each other'
	}
	$Token = Read-TokenFile $TokenFile
} elseif (-not $Token) {
	$Token = Read-TokenHidden
}
$Token = [string]$Token -replace '\s', ''
if (-not $Token) {
	Stop-Install 'no enrollment token given'
}

# 6. The binary for this machine. A 32-bit PowerShell on a 64-bit Windows
# sees x86 in PROCESSOR_ARCHITECTURE and the real one in
# PROCESSOR_ARCHITEW6432.
$arch = [Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITEW6432')
if (-not $arch) {
	$arch = [Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITECTURE')
}
switch ($arch) {
	'AMD64' { $goArch = 'amd64' }
	'ARM64' { $goArch = 'arm64' }
	default { Stop-Install "unsupported architecture $arch (only AMD64 and ARM64 have agent binaries)" }
}

# From the stop of a running agent on, a failure must not leave its
# service without the recovery actions Stop-AgentService cleared: the
# finally below sets them again.
$restoreRecovery = $false
try {
	$tmp = Join-Path ([IO.Path]::GetTempPath()) ('pulsix-install-' + [guid]::NewGuid().ToString('N'))
	New-Item -ItemType Directory -Path $tmp | Out-Null
	try {
		# 7. Download and verify.
		$url = "$BaseUrl/agents/bin/windows/$goArch"
		$downloaded = Join-Path $tmp 'pulsix-agent.exe'
		$checksumFile = Join-Path $tmp 'pulsix-agent.exe.sha256'
		Write-Host "pulsix-agent: downloading the binary for windows/$goArch from $BaseUrl"
		Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $downloaded
		Invoke-WebRequest -UseBasicParsing -Uri ($url + '.sha256') -OutFile $checksumFile

		$first = Get-Content -LiteralPath $checksumFile -TotalCount 1
		if ($null -eq $first) {
			$first = ''
		}
		$expected = ([string]$first).Trim().Split()[0].ToLower()
		if ($expected -notmatch '^[0-9a-f]{64}$') {
			Stop-Install 'the manager returned an unusable checksum'
		}
		$actual = (Get-FileHash -LiteralPath $downloaded -Algorithm SHA256).Hash.ToLower()
		if ($actual -ne $expected) {
			Stop-Install "checksum mismatch (got $actual, expected $expected); do not install an unverified binary"
		}

		# 8. An existing data directory is checked while a running agent
		# still runs, so a problem with it leaves that agent running.
		if (Test-Path -LiteralPath $DataDir) {
			Assert-DataDirReusable
		}

		# 9. Stop a running agent and wait for its process, so its binary
		# can be replaced.
		if ($null -ne (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
			$restoreRecovery = $true
			Stop-AgentService 'the binary was not replaced'
		}

		# 10. The binary. A virus scanner may hold the file for a moment
		# after the download or the stop: up to 5 tries, 1 second apart.
		New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
		for ($attempt = 1; $true; $attempt++) {
			try {
				Copy-Item -LiteralPath $downloaded -Destination $BinPath -Force
				break
			} catch {
				if ($attempt -ge 5) {
					Stop-Install "cannot write $BinPath ($($_.Exception.Message)); run the installer again"
				}
				Start-Sleep -Seconds 1
			}
		}
	} finally {
		Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
	}

	# 11. The data directory, private to SYSTEM and Administrators. An
	# existing one is checked again: it may have changed since step 8.
	if (Test-Path -LiteralPath $DataDir) {
		Assert-DataDirReusable
		$created = $false
	} else {
		# Without -Force, New-Item fails if someone created the folder
		# since the check above.
		New-Item -ItemType Directory -Path $DataDir | Out-Null
		$created = $true
	}
	& icacls.exe $DataDir /inheritance:r /grant:r "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F" | Out-Null
	if ($LASTEXITCODE -ne 0) {
		Stop-Install "icacls could not protect $DataDir (exit code $LASTEXITCODE)"
	}
	if ($created) {
		# Not recursive: the folder is new, and its owner is the account
		# that ran this script.
		& icacls.exe $DataDir /setowner "*S-1-5-32-544" | Out-Null
		if ($LASTEXITCODE -ne 0) {
			Stop-Install "icacls could not hand $DataDir to Administrators (exit code $LASTEXITCODE)"
		}
	}
	if (Test-Path -LiteralPath $EnvFile) {
		Remove-Item -LiteralPath $EnvFile -Force
	}
	Assert-OnlyAdmins $DataDir
	Get-ChildItem -LiteralPath $DataDir -Force | Where-Object { $_.Name -ne 'agent.yaml' } | ForEach-Object { Assert-OnlyAdmins $_.FullName }

	# UTF-8 without a byte order mark, LF line endings.
	$lines = @("PULSIX_MANAGER_ADDR=$Manager", "PULSIX_ENROLL_TOKEN=$Token")
	if ($Name) {
		$lines += "PULSIX_AGENT_NAME=$Name"
	}
	if ($ServerName) {
		$lines += "PULSIX_MANAGER_SERVER_NAME=$ServerName"
	}
	[IO.File]::WriteAllText($EnvFile, (($lines -join "`n") + "`n"), (New-Object Text.UTF8Encoding $false))
	& icacls.exe $EnvFile /setowner "*S-1-5-32-544" | Out-Null
	if ($LASTEXITCODE -ne 0) {
		Stop-Install "icacls could not hand $EnvFile to Administrators (exit code $LASTEXITCODE)"
	}
	Assert-OnlyAdmins $EnvFile

	# 12. The service: created, or pointed at the new binary path if it
	# exists, and its recovery actions.
	$scArgs = 'binPath= "\"' + $BinPath + '\" --env-file \"' + $EnvFile + '\" --log-file \"' + $LogFile + '\"" start= delayed-auto DisplayName= "Pulsix agent"'
	if ($null -ne (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
		$verb = 'config'
	} else {
		$verb = 'create'
	}
	Invoke-Sc "$verb $ServiceName $scArgs"
	Set-AgentRecovery
	$restoreRecovery = $false
} finally {
	if ($restoreRecovery) {
		try {
			Set-AgentRecovery
		} catch {
			Write-Warning "pulsix-agent: could not restore the recovery actions of $ServiceName ($($_.Exception.Message)); run the installer again"
		}
	}
}

# 13. Start.
try {
	Start-Service -Name $ServiceName
} catch {
	Stop-Install "the $ServiceName service did not start ($($_.Exception.Message)); see $LogFile"
}

Write-Host "pulsix-agent: installed and started ($ServiceName)"
Write-Host "pulsix-agent: identity lives in $DataDir; the enrollment token is spent on first start"
