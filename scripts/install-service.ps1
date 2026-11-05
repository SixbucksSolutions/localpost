[CmdletBinding()]
param(
    [ValidateSet('Install', 'Start', 'Stop', 'Uninstall')][string]$Action = 'Install',
    [string]$PackageDirectory = '',
    [ValidatePattern('^[A-Za-z][A-Za-z0-9_-]{0,63}$')][string]$ServiceName = 'localpost',
    [string]$InstallRoot = "$env:ProgramFiles\localpost\0.2.1",
    [string]$MailboxRoot = "$env:ProgramData\localpost\mailboxes",
    [ValidateRange(1024, 65535)][int]$Port = 53147,
    [string]$AgentAccount = ([Security.Principal.WindowsIdentity]::GetCurrent().Name)
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($Action -eq 'Install' -and -not $PackageDirectory) {
    $PackageDirectory = Split-Path -Parent $PSScriptRoot
}
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not ([Security.Principal.WindowsPrincipal]::new($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this script from an elevated PowerShell session.'
}
function Resolve-LocalPath([string]$Value) {
    if ($Value -notmatch '^[A-Za-z]:[\\/]' -or $Value -match '["%\r\n]') { throw 'Paths must be absolute local drive paths without quotes or environment expansions.' }
    $full = [IO.Path]::GetFullPath($Value).TrimEnd('\')
    if ($full.Length -le 3) { throw 'A drive root is not an installation or mailbox directory.' }
    $cursor = $full
    while ($cursor) {
        if (Test-Path -LiteralPath $cursor) {
            if ((Get-Item -LiteralPath $cursor -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Reparse points are not supported in service paths.' }
        }
        $cursor = Split-Path -Parent $cursor
    }
    return $full
}
$InstallRoot = Resolve-LocalPath $InstallRoot
$MailboxRoot = Resolve-LocalPath $MailboxRoot
if ($InstallRoot -eq $MailboxRoot -or $MailboxRoot.StartsWith($InstallRoot + '\', [StringComparison]::OrdinalIgnoreCase) -or $InstallRoot.StartsWith($MailboxRoot + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Installation and mailbox directories must be separate.' }
# TrustedInstaller, SYSTEM and built-in Administrators may own service code.
$trustedSids = @('S-1-5-18', 'S-1-5-32-544', 'S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464')
function Assert-ProtectedPath([string]$Path, [bool]$CodeDirectory = $false) {
    $cursor = $Path
    while ($cursor) {
        if (Test-Path -LiteralPath $cursor) {
            $item = Get-Item -LiteralPath $cursor -Force
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Reparse points are not supported in service code paths.' }
            $acl = Get-Acl -LiteralPath $cursor
            if ($trustedSids -notcontains $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value) { throw "Untrusted service path owner: $cursor" }
            $unsafe = [Security.AccessControl.FileSystemRights]'Delete, DeleteSubdirectoriesAndFiles, ChangePermissions, TakeOwnership'
            if ($CodeDirectory -and $cursor -eq $Path) { $unsafe = $unsafe -bor [Security.AccessControl.FileSystemRights]'WriteData, AppendData' }
            foreach ($rule in $acl.Access) {
                if ($rule.AccessControlType -ne 'Allow' -or ($rule.PropagationFlags -band [Security.AccessControl.PropagationFlags]::InheritOnly)) { continue }
                $sid = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
                if ($trustedSids -notcontains $sid -and ($rule.FileSystemRights -band $unsafe)) { throw "Untrusted service path write/delete permissions: $cursor" }
            }
        }
        $cursor = Split-Path -Parent $cursor
    }
}
Assert-ProtectedPath $InstallRoot $true
$wrapper = Join-Path $InstallRoot 'localpost-service.exe'
$markerPath = Join-Path $InstallRoot 'localpost-service-owner.json'
$wrapperHash = '05b82d46ad331cc16bdc00de5c6332c1ef818df8ceefcd49c726553209b3a0da'
$service = Get-CimInstance Win32_Service -Filter "Name='$ServiceName'"
function Assert-RegularFile([string]$Path) {
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Service files must be regular files.' }
}
function Assert-Ownership {
    Assert-ProtectedPath $InstallRoot $true
    foreach ($file in @($markerPath, $wrapper, (Join-Path $InstallRoot 'localpost-service.xml'), (Join-Path $InstallRoot 'localpost.exe'), (Join-Path $InstallRoot 'build-info.json'))) {
        Assert-RegularFile $file
        Assert-ProtectedPath $file $true
    }
    if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf)) { throw 'Missing localpost service ownership record.' }
    $owner = Get-Content -LiteralPath $markerPath -Raw | ConvertFrom-Json
    if ($owner.serviceName -ne $ServiceName -or $owner.installRoot -ne $InstallRoot) { throw 'Service ownership record does not match.' }
    if ($owner.wrapperSHA256 -ne $wrapperHash -or (Get-FileHash -LiteralPath $wrapper).Hash -ne $wrapperHash) { throw 'Installed wrapper hash differs from pin.' }
    $installedManifest = Get-Content -LiteralPath (Join-Path $InstallRoot 'build-info.json') -Raw | ConvertFrom-Json
    if ($installedManifest.binarySHA256 -ne $owner.binarySHA256 -or (Get-FileHash -LiteralPath (Join-Path $InstallRoot 'localpost.exe')).Hash -ne $owner.binarySHA256) { throw 'Installed daemon hash differs from ownership record.' }
    if ((Get-FileHash -LiteralPath (Join-Path $InstallRoot 'localpost-service.xml')).Hash -ne $owner.configSHA256) { throw 'Installed service configuration changed.' }
    if ($service -and $service.PathName.Trim('"') -ne $wrapper) { throw 'Existing service belongs to another installation.' }
}
function Invoke-ServiceControl([string]$Arguments) {
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = Join-Path $env:SystemRoot 'System32\sc.exe'
    $info.Arguments = $Arguments
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $process = [Diagnostics.Process]::Start($info)
    if (-not $process.WaitForExit(60000)) {
        $process.Kill()
        throw 'SCM control exceeded 60 seconds.'
    }
    $process.StandardOutput.ReadToEnd() | Set-Content -LiteralPath (Join-Path $InstallRoot 'control-logs\command.stdout.log')
    $process.StandardError.ReadToEnd() | Set-Content -LiteralPath (Join-Path $InstallRoot 'control-logs\command.stderr.log')
    if ($process.ExitCode -ne 0) { throw "SCM control failed with exit code $($process.ExitCode). Inspect $InstallRoot\control-logs." }
}
function Start-OwnedService {
    Invoke-ServiceControl ('failure {0} reset= 3600 actions= restart/10000' -f $ServiceName)
    (Get-Service -Name $ServiceName).Start()
    Wait-ServiceState 'Running'
}
function Stop-OwnedService {
    (Get-Service -Name $ServiceName).Stop()
    Wait-ServiceState 'Stopped'
}
function Wait-ServiceState([string]$State) {
    (Get-Service -Name $ServiceName).WaitForStatus($State, [TimeSpan]::FromSeconds(60))
}
if ($Action -ne 'Install') {
    Assert-Ownership
    if (-not $service) { throw 'Owned service is not installed.' }
    switch ($Action) {
        'Start' { Start-OwnedService }
        'Stop' { Stop-OwnedService }
        'Uninstall' {
            if ($service.State -ne 'Stopped') { Stop-OwnedService }
            Invoke-ServiceControl ('delete ' + $ServiceName)
            $owner = Get-Content -LiteralPath $markerPath -Raw | ConvertFrom-Json
            if ($owner.eventSourceOwned -and [Diagnostics.EventLog]::SourceExists($ServiceName)) {
                if ([Diagnostics.EventLog]::LogNameFromSourceName($ServiceName, '.') -ne 'Application') { throw 'Unexpected event log source ownership.' }
                [Diagnostics.EventLog]::DeleteEventSource($ServiceName)
            }
        }
    }
    return
}
if ($service) { throw 'Service already exists. Stop and uninstall the owned service before installing a new release.' }
$resume = (Test-Path -LiteralPath $InstallRoot) -and @(Get-ChildItem -LiteralPath $InstallRoot -Force).Count -gt 0
if ($resume) {
    Assert-RegularFile $markerPath
    Assert-ProtectedPath $markerPath $true
    $previousOwner = Get-Content -LiteralPath $markerPath -Raw | ConvertFrom-Json
    if ($previousOwner.serviceName -ne $ServiceName -or $previousOwner.installRoot -ne $InstallRoot -or $previousOwner.mailboxRoot -ne $MailboxRoot -or $previousOwner.port -ne $Port) { throw 'Cannot resume an installation with different settings.' }
}
$agentSid = ([Security.Principal.NTAccount]::new($AgentAccount)).Translate([Security.Principal.SecurityIdentifier])
$package = (Resolve-Path -LiteralPath $PackageDirectory).Path
$binary = Join-Path $package 'localpost.exe'
$manifest = Get-Content -LiteralPath (Join-Path $package 'build-info.json') -Raw | ConvertFrom-Json
if ($manifest.version -ne '0.2.1' -or $manifest.target -ne 'windows/amd64' -or $manifest.sourceCommit -notmatch '^[a-f0-9]{40}$' -or $manifest.binarySHA256 -notmatch '^[a-f0-9]{64}$') { throw 'Expected a Windows amd64 0.2.1 release manifest.' }
if ((Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash -ne $manifest.binarySHA256) { throw 'Release executable SHA-256 differs from build-info.json.' }
if (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) { throw "Port $Port already has a listener." }
function Set-PrivateAcl([string]$Path, [string]$ServiceRights, [bool]$IncludeAgent) {
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $acl.SetOwner([Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'))
    $inherit = [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
    $propagate = [Security.AccessControl.PropagationFlags]::None
    foreach ($sid in @('S-1-5-18', 'S-1-5-32-544')) {
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($sid), 'FullControl', $inherit, $propagate, 'Allow'))
    }
    if ($ServiceRights) { $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new('S-1-5-19'), $ServiceRights, $inherit, $propagate, 'Allow')) }
    if ($IncludeAgent) { $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($agentSid, 'Modify', $inherit, $propagate, 'Allow')) }
    Set-Acl -LiteralPath $Path -AclObject $acl
}
if ($resume -and ($previousOwner.sourceCommit -ne $manifest.sourceCommit -or $previousOwner.binarySHA256 -ne $manifest.binarySHA256)) { throw 'Resume requires the same release manifest.' }
$missing = [Collections.Generic.List[string]]::new()
$cursor = $InstallRoot
while (-not (Test-Path -LiteralPath $cursor)) { $missing.Add($cursor); $cursor = Split-Path -Parent $cursor }
for ($index = $missing.Count - 1; $index -ge 0; $index--) {
    Assert-ProtectedPath (Split-Path -Parent $missing[$index])
    New-Item -ItemType Directory -Path $missing[$index] | Out-Null
    Set-PrivateAcl $missing[$index] 'ReadAndExecute' $false
}
Assert-ProtectedPath $InstallRoot $true
Set-PrivateAcl $InstallRoot 'ReadAndExecute' $false
$controlLogs = Join-Path $InstallRoot 'control-logs'
New-Item -ItemType Directory -Path $controlLogs -Force | Out-Null
Set-PrivateAcl $controlLogs '' $false
$record = @{ serviceName = $ServiceName; installRoot = $InstallRoot; mailboxRoot = $MailboxRoot; port = $Port; agentSid = $agentSid.Value; sourceCommit = $manifest.sourceCommit; binarySHA256 = $manifest.binarySHA256; wrapperSHA256 = $wrapperHash; eventSourceOwned = ($resume -and $previousOwner.PSObject.Properties.Name -contains 'eventSourceOwned' -and $previousOwner.eventSourceOwned) }
$record | ConvertTo-Json | Set-Content -LiteralPath $markerPath -Encoding UTF8
$logs = Join-Path $InstallRoot 'logs'
New-Item -ItemType Directory -Path $logs -Force | Out-Null
Set-PrivateAcl $logs 'Modify' $false
New-Item -ItemType Directory -Path $MailboxRoot -Force | Out-Null
Set-PrivateAcl $MailboxRoot 'Modify' $true
Copy-Item -LiteralPath $binary -Destination (Join-Path $InstallRoot 'localpost.exe') -Force
if ((Get-FileHash -LiteralPath (Join-Path $InstallRoot 'localpost.exe')).Hash -ne $manifest.binarySHA256) { throw 'Installed executable hash mismatch.' }
Copy-Item -LiteralPath (Join-Path $package 'build-info.json') -Destination $InstallRoot -Force
Copy-Item -LiteralPath (Join-Path $package 'docs\windows-service.md') -Destination $InstallRoot -Force
# Official v2.12.0 release asset, independently hashed on 2026-11-05.
$wrapperUrl = 'https://github.com/winsw/winsw/releases/download/v2.12.0/WinSW-x64.exe'
$wrapperHash = '05b82d46ad331cc16bdc00de5c6332c1ef818df8ceefcd49c726553209b3a0da'
Invoke-WebRequest -Uri $wrapperUrl -OutFile $wrapper -UseBasicParsing -TimeoutSec 60
if ((Get-FileHash -LiteralPath $wrapper -Algorithm SHA256).Hash -ne $wrapperHash) { throw 'WinSW checksum mismatch. Installation stopped before executing it.' }
$escape = { param($value) [Security.SecurityElement]::Escape([string]$value) }
$exeXml = & $escape (Join-Path $InstallRoot 'localpost.exe')
$rootXml = & $escape $MailboxRoot
$logXml = & $escape $logs
@"
<service>
  <id>$ServiceName</id>
  <name>$ServiceName</name>
  <description>localpost managed-mailbox loopback service</description>
  <executable>$exeXml</executable>
  <arguments>--mailbox-root &quot;$rootXml&quot; --port $Port</arguments>
  <startmode>Automatic</startmode>
  <serviceaccount><domain>NT AUTHORITY</domain><user>LocalService</user></serviceaccount>
  <stoptimeout>40 sec</stoptimeout>
  <stopparentprocessfirst>true</stopparentprocessfirst>
  <onfailure action="restart" delay="10 sec"/>
  <resetfailure>1 hour</resetfailure>
  <logpath>$logXml</logpath>
  <log mode="roll-by-size"><sizeThreshold>1024</sizeThreshold><keepFiles>4</keepFiles></log>
</service>
"@ | Set-Content -LiteralPath (Join-Path $InstallRoot 'localpost-service.xml') -Encoding UTF8
foreach ($codeFile in @('localpost.exe', 'build-info.json', 'windows-service.md', 'localpost-service.exe', 'localpost-service.xml', 'localpost-service-owner.json')) {
    $codePath = Join-Path $InstallRoot $codeFile
    $codeAcl = Get-Acl -LiteralPath $codePath
    $codeAcl.SetOwner([Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'))
    Set-Acl -LiteralPath $codePath -AclObject $codeAcl
}
$record.configSHA256 = (Get-FileHash -LiteralPath (Join-Path $InstallRoot 'localpost-service.xml')).Hash
$eventSourceOwned = $false
if ([Diagnostics.EventLog]::SourceExists($ServiceName)) {
    if (-not $resume -or $previousOwner.PSObject.Properties.Name -notcontains 'eventSourceOwned' -or -not $previousOwner.eventSourceOwned -or [Diagnostics.EventLog]::LogNameFromSourceName($ServiceName, '.') -ne 'Application') { throw 'Event log source already belongs to another installation.' }
    $eventSourceOwned = $true
} else {
    $record.eventSourceOwned = $true
    $record | ConvertTo-Json | Set-Content -LiteralPath $markerPath -Encoding UTF8
    [Diagnostics.EventLog]::CreateEventSource($ServiceName, 'Application')
    $eventSourceOwned = $true
}
$record.eventSourceOwned = $eventSourceOwned
$record | ConvertTo-Json | Set-Content -LiteralPath $markerPath -Encoding UTF8
Assert-Ownership
Invoke-ServiceControl ('create {0} binPath= "\"{1}\"" start= auto obj= "NT AUTHORITY\LocalService"' -f $ServiceName, $wrapper)
Start-OwnedService
$ready = $false
for ($attempt = 0; $attempt -lt 40; $attempt++) {
    try {
        $response = Invoke-WebRequest "http://127.0.0.1:$Port/api/2026-11-05/registrations" -Method Options -TimeoutSec 2 -UseBasicParsing
        if ($response.StatusCode -eq 204) { $ready = $true; break }
    } catch { Start-Sleep -Milliseconds 250 }
}
if (-not $ready) { throw "Installed service did not become HTTP-ready. Inspect $InstallRoot\logs." }
Write-Output "Installed $ServiceName with automatic startup under LocalService. Mailbox root: $MailboxRoot"
