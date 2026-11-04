#requires -Version 7.0
$ErrorActionPreference = 'Stop'

if (-not $IsWindows -or [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -ne 'X64') {
    throw 'This development installer currently supports Windows x64.'
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$version = (Get-Content -LiteralPath (Join-Path $repoRoot '.golangci-version') -Raw).Trim()
if ($version -notmatch '^\d+\.\d+\.\d+$') { throw 'Invalid pinned golangci-lint version.' }

$toolRoot = Join-Path $repoRoot ".tools/golangci-lint/$version"
$executable = Join-Path $toolRoot 'golangci-lint.exe'
if (Test-Path -LiteralPath $executable) {
    if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) {
        throw "Expected a tool file at $executable; found a directory."
    }
    $cachedVersionMatches = $false
    try {
        $cachedInfo = & $executable version --json 2>$null
        if ($LASTEXITCODE -eq 0) {
            $cachedVersionMatches = ($cachedInfo | ConvertFrom-Json).version -eq $version
        }
    } catch {
        # A corrupt cached executable is repaired by the verified download below.
    }
    if ($cachedVersionMatches) {
        Write-Output "golangci-lint $version is already installed locally."
        return
    }
    Write-Output 'Replacing an unusable or mismatched local golangci-lint.'
}

$downloadRoot = Join-Path $repoRoot '.tools/downloads'
New-Item -ItemType Directory -Force -Path $downloadRoot, $toolRoot | Out-Null
$archiveName = "golangci-lint-$version-windows-amd64.zip"
$archive = Join-Path $downloadRoot $archiveName
$release = "https://github.com/golangci/golangci-lint/releases/download/v$version"
$checksumResponse = Invoke-WebRequest "$release/golangci-lint-$version-checksums.txt"
$checksums = if ($checksumResponse.Content -is [byte[]]) {
    [System.Text.Encoding]::UTF8.GetString($checksumResponse.Content)
} else {
    [string]$checksumResponse.Content
}
$pattern = '(?m)^([a-fA-F0-9]{64})\s+\*?' + [regex]::Escape($archiveName) + '\s*$'
$checksumMatches = [regex]::Matches($checksums, $pattern)
if ($checksumMatches.Count -ne 1) { throw 'Expected exactly one official archive checksum.' }

Invoke-WebRequest "$release/$archiveName" -OutFile $archive
$actualHash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash
if ($actualHash -ine $checksumMatches[0].Groups[1].Value) {
    throw 'golangci-lint archive checksum mismatch; the tool was not installed.'
}

Expand-Archive -LiteralPath $archive -DestinationPath $toolRoot -Force
$unpacked = Join-Path $toolRoot "golangci-lint-$version-windows-amd64/golangci-lint.exe"
if (-not (Test-Path -LiteralPath $unpacked)) { throw 'Release archive lacks the expected executable.' }
Copy-Item -LiteralPath $unpacked -Destination $executable
Write-Output "Installed golangci-lint $version under .tools; no global installation changed."
