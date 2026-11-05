#Requires -Version 7.0
# Builds a private, reproducible client/profile bundle. Does not publish or deploy.
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$OutputDirectory,
    [ValidateSet('adoption-r1')][string]$Version = 'adoption-r1'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$repoRoot = Split-Path -Parent $PSScriptRoot
$previousLocation = Get-Location
$buildEnvironment = [ordered]@{
    GOTOOLCHAIN = 'go1.27.3'; GOENV = 'off'; GOROOT = ''; GO111MODULE = 'on'
    GOFIPS140 = 'off'; GOWORK = 'off'; GOFLAGS = ''; GOEXPERIMENT = ''
    CGO_ENABLED = '0'; GOAMD64 = 'v1'; GOARM64 = 'v8.0'; GOOS = ''; GOARCH = ''
}
$previousEnvironment = @{}
$temporaryDirectory = $null

function Invoke-Git {
    param([string[]]$Arguments)
    $result = & git -c "safe.directory=$($repoRoot.Replace('\', '/'))" @Arguments
    if ($LASTEXITCODE -ne 0) { throw 'Git candidate source operation failed.' }
    return $result
}

function Assert-EmptyOutput {
    param([string]$Directory)
    # Check every existing ancestor too. A junction above a new output directory
    # can otherwise redirect a lexically private path into the public checkout.
    $ancestor = $Directory
    while ($ancestor) {
        if (Test-Path -LiteralPath $ancestor) {
            $ancestorItem = Get-Item -LiteralPath $ancestor -Force
            if ($ancestorItem.Attributes -band [IO.FileAttributes]::ReparsePoint) {
                throw 'Candidate output path must not traverse a reparse point.'
            }
        }
        $parent = [IO.Directory]::GetParent($ancestor)
        $ancestor = if ($parent) { $parent.FullName } else { $null }
    }
    if (Test-Path -LiteralPath $Directory) {
        $item = Get-Item -LiteralPath $Directory -Force
        if (-not $item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw 'Candidate output must be a real directory.'
        }
        if (@(Get-ChildItem -LiteralPath $Directory -Force).Count -ne 0) {
            throw 'Candidate output directory must be new or empty.'
        }
    }
}

function Get-FileDigest {
    param([string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Write-Utf8File {
    param([string]$Path, [string]$Text)
    [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false))
}

function Expand-Source {
    param([string]$Archive, [string]$Destination)
    $zip = [IO.Compression.ZipFile]::OpenRead($Archive)
    try {
        foreach ($entry in $zip.Entries) {
            $segments = $entry.FullName.Split('/')
            $unixType = ($entry.ExternalAttributes -shr 16) -band 0xF000
            if ($entry.FullName.Contains('\') -or $entry.FullName.StartsWith('/') -or
                $entry.FullName.Contains(':') -or $segments -contains '..' -or
                $unixType -eq 0xA000) {
                throw 'Source archive contains an unsafe path or symbolic link.'
            }
        }
    }
    finally { $zip.Dispose() }
    [IO.Compression.ZipFile]::ExtractToDirectory($Archive, $Destination)
}

try {
    Set-Location $repoRoot
    if (-not [IO.Path]::IsPathFullyQualified($OutputDirectory)) {
        throw 'Candidate output must be an absolute private directory outside the repository.'
    }
    $OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
    $repositoryPrefix = [IO.Path]::GetFullPath($repoRoot).TrimEnd('\', '/')
    if ($OutputDirectory.Equals($repositoryPrefix, [StringComparison]::OrdinalIgnoreCase) -or
        $OutputDirectory.StartsWith($repositoryPrefix + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Candidate output must be outside the repository.'
    }
    Assert-EmptyOutput $OutputDirectory
    $status = @(Invoke-Git @('status', '--porcelain=v1', '--untracked-files=all'))
    if ($status.Count -ne 0) { throw 'Candidate build requires a clean Git checkout, including untracked files.' }
    $commit = (Invoke-Git @('rev-parse', '--verify', 'HEAD^{commit}')).Trim()
    if ($commit -cnotmatch '^[0-9a-f]{40}$') { throw 'Git did not return a full source commit.' }

    foreach ($name in $buildEnvironment.Keys) {
        $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        [Environment]::SetEnvironmentVariable($name, $buildEnvironment[$name], 'Process')
    }
    $temporaryDirectory = Join-Path ([IO.Path]::GetTempPath()) ('localpost-adoption-' + [Guid]::NewGuid())
    New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
    $archive = Join-Path $temporaryDirectory 'source.zip'
    Invoke-Git @('archive', '--format=zip', "--output=$archive", $commit) | Out-Null
    $bundle = Join-Path $temporaryDirectory 'bundle'
    New-Item -ItemType Directory -Path $bundle | Out-Null
    $source = Join-Path $temporaryDirectory 'source-1'
    Expand-Source $archive $source
    if ((Get-Content -LiteralPath (Join-Path $source '.go-version') -Raw).Trim() -ne '1.27.3') {
        throw 'Source Go pin differs from candidate toolchain.'
    }
    Set-Location $source
    $actualToolchain = & go env GOVERSION
    if ($LASTEXITCODE -ne 0 -or $actualToolchain.Trim() -ne 'go1.27.3') {
        throw 'Candidate build requires Go 1.27.3.'
    }

    $assets = @('LICENSE', 'docs/contracts/valkey-webdis.md', 'docs/operations/valkey-webdis.md',
        'deploy/valkey-webdis/README.md', 'deploy/valkey-webdis/compose.yaml',
        'deploy/valkey-webdis/webdis.json', 'deploy/valkey-webdis/images.json')
    foreach ($relative in $assets) {
        $destination = Join-Path $bundle $relative
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
        [IO.File]::Copy((Join-Path $source $relative), $destination)
    }
    $images = Get-Content -LiteralPath (Join-Path $source 'deploy/valkey-webdis/images.json') -Raw | ConvertFrom-Json
    $compose = Get-Content -LiteralPath (Join-Path $source 'deploy/valkey-webdis/compose.yaml') -Raw
    foreach ($image in $images.images) {
        if ($image.indexDigest -cnotmatch '^sha256:[0-9a-f]{64}$' -or
            -not $compose.Contains("$($image.image)@$($image.indexDigest)")) {
            throw 'Compose image pin differs from the image manifest.'
        }
    }
    $goRoot = & go env GOROOT
    if ($LASTEXITCODE -ne 0) { throw 'Unable to locate Go license files.' }
    $goRoot = $goRoot.Trim()
    New-Item -ItemType Directory -Path (Join-Path $bundle 'third-party/go') -Force | Out-Null
    foreach ($notice in @('LICENSE', 'PATENTS')) {
        [IO.File]::Copy((Join-Path $goRoot $notice), (Join-Path $bundle "third-party/go/$notice"))
    }

    $targets = @('windows/amd64', 'linux/amd64', 'linux/arm64')
    $binaries = @()
    $ldflags = "-X main.releaseVersion=$Version -X main.sourceCommit=$commit"
    foreach ($target in $targets) {
        $env:GOOS, $env:GOARCH = $target.Split('/')
        $dependencies = & go list -deps -f '{{.Dir}}' ./cmd/localpost-client
        if ($LASTEXITCODE -ne 0) { throw "Unable to inspect Go notices for $target." }
        foreach ($dependency in $dependencies) {
            $directory = $dependency.Trim()
            $goPrefix = $goRoot.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
            if (-not $directory.StartsWith($goPrefix, [StringComparison]::OrdinalIgnoreCase)) { continue }
            while ($directory -ne $goRoot) {
                foreach ($notice in @('LICENSE', 'NOTICE', 'PATENTS')) {
                    $noticeSource = Join-Path $directory $notice
                    if (Test-Path -LiteralPath $noticeSource -PathType Leaf) {
                        $noticeRelative = [IO.Path]::GetRelativePath($goRoot, $noticeSource)
                        $noticeDestination = Join-Path $bundle "third-party/go/$noticeRelative"
                        if (-not (Test-Path -LiteralPath $noticeDestination)) {
                            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $noticeDestination) | Out-Null
                            [IO.File]::Copy($noticeSource, $noticeDestination)
                        }
                    }
                }
                $directory = Split-Path -Parent $directory
            }
        }
        $name = 'localpost-client'
        if ($env:GOOS -eq 'windows') { $name += '.exe' }
        $relative = "bin/$($target.Replace('/', '-'))/$name"
        $destination = Join-Path $bundle $relative
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
        & go build -trimpath -buildvcs=false -ldflags $ldflags -o $destination ./cmd/localpost-client
        if ($LASTEXITCODE -ne 0) { throw "Candidate build failed for $target." }
        $binaries += [ordered]@{ target = $target; path = $relative; sha256 = Get-FileDigest $destination }
    }
    # A second extraction changes absolute source/build paths as well as outputs.
    $secondSource = Join-Path $temporaryDirectory 'source-2'
    Expand-Source $archive $secondSource
    Set-Location $secondSource
    foreach ($binary in $binaries) {
        $env:GOOS, $env:GOARCH = $binary.target.Split('/')
        $secondBinary = Join-Path $temporaryDirectory "second-$($env:GOOS)-$($env:GOARCH)"
        & go build -trimpath -buildvcs=false -ldflags $ldflags -o $secondBinary ./cmd/localpost-client
        if ($LASTEXITCODE -ne 0) { throw "Repeated candidate build failed for $($binary.target)." }
        if ((Get-FileDigest $secondBinary) -ne $binary.sha256) {
            throw "Candidate binary is not reproducible for $($binary.target)."
        }
    }
    $files = @(Get-ChildItem -LiteralPath $bundle -Recurse -File | ForEach-Object {
        [ordered]@{ path = [IO.Path]::GetRelativePath($bundle, $_.FullName).Replace('\', '/'); sha256 = Get-FileDigest $_.FullName }
    } | Sort-Object { $_.path })
    $manifest = [ordered]@{
        candidateVersion = $Version; publicationStatus = 'unpublished'; qualificationStatus = 'pending'
        sourceCommit = $commit; sourceArchiveSHA256 = Get-FileDigest $archive
        toolchain = 'go1.27.3'; buildEnvironment = $buildEnvironment
        buildFlags = @('-trimpath', '-buildvcs=false', '-ldflags', $ldflags)
        reproducibility = 'identical SHA256 across two builds from separate extractions of the same source archive'
        binaries = $binaries; images = $images; files = $files
    }
    Write-Utf8File (Join-Path $bundle 'manifest.json') (($manifest | ConvertTo-Json -Depth 20) + "`n")
    $files += [ordered]@{ path = 'manifest.json'; sha256 = Get-FileDigest (Join-Path $bundle 'manifest.json') }
    $sums = ($files | Sort-Object { $_.path } | ForEach-Object { "$($_.sha256)  $($_.path)`n" }) -join ''
    Write-Utf8File (Join-Path $bundle 'SHA256SUMS') $sums

    Assert-EmptyOutput $OutputDirectory
    New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
    foreach ($file in Get-ChildItem -LiteralPath $bundle -Recurse -File) {
        $destination = Join-Path $OutputDirectory ([IO.Path]::GetRelativePath($bundle, $file.FullName))
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $destination) | Out-Null
        # Copy without overwrite, even if another process changes the output directory.
        [IO.File]::Copy($file.FullName, $destination, $false)
    }
    Write-Output "Built unpublished $Version candidate from $commit in $OutputDirectory"
}
finally {
    Set-Location $previousLocation
    foreach ($name in $previousEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], 'Process')
    }
    if ($temporaryDirectory) {
        $expectedParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/')
        $actualParent = [IO.Path]::GetFullPath((Split-Path -Parent $temporaryDirectory)).TrimEnd('\', '/')
        if ($actualParent -ne $expectedParent -or (Split-Path -Leaf $temporaryDirectory) -notmatch '^localpost-adoption-[0-9a-f-]{36}$') {
            throw 'Refusing cleanup outside the owned candidate temporary directory.'
        }
        Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force
    }
}
