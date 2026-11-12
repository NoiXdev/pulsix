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

param(
	[Parameter(Mandatory)][string]$Manager,
	[string]$ServerName,
	[string]$Name,
	[string]$Token,
	[string]$TokenFile
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
function Assert-OnlyAdmins([string]$Path) {
	if (Test-ReparsePoint $Path) {
		Stop-Install "$Path is a link (symlink or junction); remove it and run the installer again"
	}
	$acl = Get-Acl -LiteralPath $Path
	$owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
	if ($AllowedSids -notcontains $owner) {
		Stop-Install "$Path is owned by $(Format-Sid $owner), not by SYSTEM or Administrators; remove it and run the installer again"
	}
	foreach ($rule in $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])) {
		if ($rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) {
			continue
		}
		if ($AllowedSids -notcontains $rule.IdentityReference.Value) {
			Stop-Install "$Path grants access to $(Format-Sid $rule.IdentityReference.Value); remove it and run the installer again"
		}
	}
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

# 1. Administrator only.
$principal = New-Object Security.Principal.WindowsPrincipal ([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
	Stop-Install 'must run as administrator (open PowerShell with "Run as administrator")'
}

# 2. TLS 1.2 for the downloads, added to what is already enabled, in case
# the script was started without the dialog's leading statement.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072

# A line break in any value would add lines of its own to agent.env.
foreach ($pair in @(@('-Manager', $Manager), @('-ServerName', $ServerName), @('-Name', $Name))) {
	if ([string]$pair[1] -match '[\r\n]') {
		Stop-Install "$($pair[0]) must be a single line"
	}
}

# 3. The token.
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

# 4. The binary for this machine. A 32-bit PowerShell on a 64-bit Windows
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

# Where things go: the 64-bit Program Files even from a 32-bit PowerShell,
# and the data directory the agent itself defaults to.
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

$tmp = Join-Path ([IO.Path]::GetTempPath()) ('pulsix-install-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
	# 5. Download and verify.
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

	# 6. Stop a running agent, so its binary can be replaced.
	$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
	if (($null -ne $service) -and ($service.Status -ne 'Stopped')) {
		Write-Host "pulsix-agent: stopping the running $ServiceName service"
		if ($service.Status -ne 'StopPending') {
			$service.Stop()
		}
		try {
			$service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
		} catch {
			Stop-Install "the running $ServiceName service did not stop within 30 seconds; stop it (Stop-Service $ServiceName) and run the installer again"
		}
	}

	# 7. The binary.
	New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
	Copy-Item -LiteralPath $downloaded -Destination $BinPath -Force
} finally {
	Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

# 8. The data directory, private to SYSTEM and Administrators. Any user may
# create folders under ProgramData, so a directory that is already there
# is only taken over when SYSTEM or Administrators own it: one created by
# someone else may hold files they planted, and taking ownership of those
# would hide where they came from.
if (Test-Path -LiteralPath $DataDir) {
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
	$created = $false
} else {
	# Without -Force, New-Item fails if someone created the folder since
	# the check above.
	New-Item -ItemType Directory -Path $DataDir | Out-Null
	$created = $true
}
& icacls.exe $DataDir /inheritance:r /grant:r "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F" | Out-Null
if ($LASTEXITCODE -ne 0) {
	Stop-Install "icacls could not protect $DataDir (exit code $LASTEXITCODE)"
}
if ($created) {
	# Not recursive: the folder is new, and its owner is the account that
	# ran this script.
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

# 9. The service: created, or pointed at the new binary path if it exists.
$scArgs = 'binPath= "\"' + $BinPath + '\" --env-file \"' + $EnvFile + '\" --log-file \"' + $LogFile + '\"" start= delayed-auto DisplayName= "Pulsix agent"'
if ($null -ne (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
	$verb = 'config'
} else {
	$verb = 'create'
}
Invoke-Sc "$verb $ServiceName $scArgs"

# 10. Restart after 5 seconds on every failure, like the Linux unit's
# Restart=on-failure. The agent reports its failures as a service exit code
# rather than by crashing, and the service manager only applies the
# recovery actions to those with the failure flag set.
Invoke-Sc "failure $ServiceName reset= 86400 actions= restart/5000/restart/5000/restart/5000"
Invoke-Sc "failureflag $ServiceName 1"
try {
	Start-Service -Name $ServiceName
} catch {
	Stop-Install "the $ServiceName service did not start ($($_.Exception.Message)); see $LogFile"
}

Write-Host "pulsix-agent: installed and started ($ServiceName)"
Write-Host "pulsix-agent: identity lives in $DataDir; the enrollment token is spent on first start"
