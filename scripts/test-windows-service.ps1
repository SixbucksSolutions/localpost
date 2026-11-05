[CmdletBinding()]
param([Parameter(Mandatory)][string]$PackageDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$runId = [guid]::NewGuid().ToString('N')
$root = Join-Path $env:ProgramData "localpost-ci-$runId"
$install = Join-Path $root 'service'
$mailboxes = Join-Path $root 'mailboxes'
$name = "localpost-ci-$runId"
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
$listener.Start()
$port = $listener.LocalEndpoint.Port
$listener.Stop()
$base = "http://127.0.0.1:$port/api/2026-11-05"
$installer = Join-Path $PSScriptRoot 'install-service.ps1'
$args = @{ ServiceName = $name; InstallRoot = $install; MailboxRoot = $mailboxes }
function Assert([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
function Wait-Ready {
    for ($attempt = 0; $attempt -lt 40; $attempt++) {
        try { return Invoke-RestMethod "$base/registrations" -Method Post -ContentType 'application/json' -Body '{}' -TimeoutSec 2 }
        catch { Start-Sleep -Milliseconds 250 }
    }
    throw 'Service did not become HTTP-ready within bounded retry window.'
}
try {
    # Build an explicitly protected test ancestor, then prove hostile code paths fail closed.
    New-Item -ItemType Directory -Path $root | Out-Null
    $rootAcl = [Security.AccessControl.DirectorySecurity]::new()
    $rootAcl.SetAccessRuleProtection($true, $false)
    $rootAcl.SetOwner([Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'))
    foreach ($sid in @('S-1-5-18', 'S-1-5-32-544')) {
        $rootAcl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($sid), 'FullControl', 'ContainerInherit, ObjectInherit', 'None', 'Allow'))
    }
    Set-Acl -LiteralPath $root -AclObject $rootAcl
    $unsafe = Join-Path $root 'untrusted-owner'
    New-Item -ItemType Directory -Path $unsafe | Out-Null
    $unsafeAcl = Get-Acl -LiteralPath $unsafe
    $unsafeAcl.SetOwner([Security.Principal.WindowsIdentity]::GetCurrent().User)
    Set-Acl -LiteralPath $unsafe -AclObject $unsafeAcl
    $rejected = $false
    try { & $installer -InstallRoot $unsafe -MailboxRoot $mailboxes -ServiceName $name -PackageDirectory $PackageDirectory -Port $port }
    catch { $rejected = $_.Exception.Message -like 'Untrusted service path owner:*' }
    Assert $rejected 'Installer accepted a user-owned code directory.'
    $unsafeAcl.SetOwner([Security.Principal.SecurityIdentifier]::new('S-1-5-32-544'))
    $unsafeAcl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new('S-1-5-32-545'), 'Modify', 'ContainerInherit, ObjectInherit', 'None', 'Allow'))
    Set-Acl -LiteralPath $unsafe -AclObject $unsafeAcl
    $rejected = $false
    try { & $installer -InstallRoot (Join-Path $unsafe 'child') -MailboxRoot $mailboxes -ServiceName $name -PackageDirectory $PackageDirectory -Port $port }
    catch { $rejected = $_.Exception.Message -like 'Untrusted service path write/delete permissions:*' }
    Assert $rejected 'Installer accepted a replaceable code ancestor.'
    & $installer @args -PackageDirectory $PackageDirectory -Port $port
    $service = Get-CimInstance Win32_Service -Filter "Name='$name'"
    Assert ($service.StartMode -eq 'Auto') 'Service is not configured for automatic boot startup.'
    Assert ($service.StartName -eq 'NT AUTHORITY\LocalService') 'Unexpected service account.'
    $registration = Wait-Ready
    Assert ($registration.id -cmatch '^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$') 'Registration ID is not canonical UUIDv4.'
    Assert ($registration.directory -eq (Join-Path $mailboxes $registration.id)) 'Mailbox path does not match registration UUID.'
    $payload = [Text.Encoding]::UTF8.GetBytes('service-lifecycle-probe')
    Invoke-WebRequest "$base/registrations/$($registration.id)/messages" -Method Post -ContentType 'application/octet-stream' -Body $payload -TimeoutSec 10 | Out-Null
    $files = @(Get-ChildItem -LiteralPath $registration.directory -Filter 'localpost_*.dat')
    Assert ($files.Count -eq 1) 'Expected one service-created delivery file.'
    Assert ([IO.File]::ReadAllText($files[0].FullName) -eq 'service-lifecycle-probe') 'Delivered bytes differ.'
    $agentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    foreach ($path in @($registration.directory, $files[0].FullName)) {
        $rules = @((Get-Acl -LiteralPath $path).Access | Where-Object { $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -eq $agentSid -and $_.IsInherited -and $_.AccessControlType -eq 'Allow' -and ($_.FileSystemRights -band [Security.AccessControl.FileSystemRights]::Modify) -eq [Security.AccessControl.FileSystemRights]::Modify })
        Assert ($rules.Count -gt 0) 'Agent Modify rights were not inherited by mailbox and file.'
    }
    Invoke-WebRequest "$base/registrations/$($registration.id)" -Method Delete -TimeoutSec 10 | Out-Null
    Assert (Test-Path -LiteralPath $files[0].FullName) 'Retirement removed delivered data.'
    & node (Join-Path $PSScriptRoot 'test-windows-service-upload.mjs') --service-url "http://127.0.0.1:$port" --mailbox-root $mailboxes --installer $installer --service-name $name --install-root $install
    if ($LASTEXITCODE -ne 0) { throw 'Accepted-upload service stop check failed.' }
    & $installer @args -Action Start
    $restartRegistration = Wait-Ready
    & $installer @args -Action Stop
    Assert (Test-Path -LiteralPath $files[0].FullName) 'Stop removed delivered data.'
    & $installer @args -Action Start
    $fresh = Wait-Ready
    Assert ($fresh.id -ne $restartRegistration.id) 'Restart reused registration ID.'
    try {
        Invoke-WebRequest "$base/registrations/$($restartRegistration.id)/messages" -Method Post -ContentType 'application/octet-stream' -Body $payload -TimeoutSec 10 | Out-Null
        throw 'Restart retained an active registration.'
    } catch {
        Assert ($null -ne $_.Exception.Response -and [int]$_.Exception.Response.StatusCode -eq 404) 'Expected retired registration HTTP 404 after restart.'
    }
    & node (Join-Path $PSScriptRoot 'check-openapi-service.mjs') "http://127.0.0.1:$port" $mailboxes
    if ($LASTEXITCODE -ne 0) { throw 'Live OpenAPI check failed.' }
    & $installer @args -Action Uninstall
        Assert (Test-Path -LiteralPath $files[0].FullName) 'Uninstall removed delivered data.'
    Write-Output 'PASS Windows SCM automatic configuration, LocalService delivery, inherited agent ACL, stop/restart, retirement, retained files, uninstall and live API contract. No OS reboot was performed.'
} catch {
    Get-CimInstance Win32_Service -Filter "Name='$name'" | Select-Object Name, State, StartMode, StartName, ExitCode | Format-List | Out-Host
    if (Test-Path -LiteralPath (Join-Path $install 'logs')) {
        Get-ChildItem -LiteralPath (Join-Path $install 'logs') -File | Select-Object -First 6 | ForEach-Object { Write-Host $_.Name; Get-Content -LiteralPath $_.FullName -Tail 40 | Out-Host }
    }
    throw
} finally {
    # Even a partially completed install may have registered the service.
    if (Get-Service -Name $name -ErrorAction SilentlyContinue) { & $installer @args -Action Uninstall }
    $resolved = [IO.Path]::GetFullPath($root)
    $parent = [IO.Path]::GetFullPath($env:ProgramData).TrimEnd('\') + '\'
    if ($resolved.StartsWith($parent, [StringComparison]::OrdinalIgnoreCase) -and (Split-Path -Leaf $resolved) -eq "localpost-ci-$runId") {
        if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force }
    } else { throw 'Refused cleanup outside owned test directory.' }
}
