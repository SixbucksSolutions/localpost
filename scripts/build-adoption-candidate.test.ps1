#Requires -Version 7.0
# Focused packaging tests use a real clean Git fixture and a deterministic fake Go builder.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$temporaryDirectory = Join-Path ([IO.Path]::GetTempPath()) ('localpost-candidate-tests-' + [Guid]::NewGuid())
$previousLocation = Get-Location
$candidateTestState = @{ mode = 'stable'; count = 0 }
$fixtureGoRoot = Join-Path $temporaryDirectory 'go'

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Assert-Rejected {
    param([scriptblock]$Action, [string]$Expected)
    $message = ''
    try { & $Action | Out-Null } catch { $message = $_.Exception.Message }
    Assert-True ($message.Contains($Expected)) "Expected rejection containing '$Expected', received '$message'."
}

function go {
    $global:LASTEXITCODE = 0
    if ($args[0] -eq 'env' -and $args[1] -eq 'GOVERSION') { return 'go1.27.3' }
    if ($args[0] -eq 'env' -and $args[1] -eq 'GOROOT') { return $fixtureGoRoot }
    if ($args[0] -eq 'list') { return $fixtureGoRoot }
    Assert-True ($args[0] -eq 'build') 'Unexpected Go invocation.'
    $candidateTestState.count++
    Assert-True ($env:CGO_ENABLED -eq '0' -and $env:GOTOOLCHAIN -eq 'go1.27.3') 'Build environment was not pinned.'
    Assert-True ($args -contains '-trimpath' -and $args -contains '-buildvcs=false') 'Reproducibility flags are missing.'
    $destination = $args[[Array]::IndexOf($args, '-o') + 1]
    $contents = "$env:GOOS/$env:GOARCH"
    if ($candidateTestState.mode -eq 'unstable') { $contents += $candidateTestState.count }
    [IO.File]::WriteAllText($destination, $contents)
    if ($candidateTestState.mode -eq 'redirect' -and $candidateTestState.count -eq $candidateTestState.redirectAt) {
        New-Item -ItemType Junction -Path $candidateTestState.junction -Target $fixture | Out-Null
    }
}

try {
    $fixture = Join-Path $temporaryDirectory 'repository'
    $sourceScript = Join-Path $PSScriptRoot 'build-adoption-candidate.ps1'
    New-Item -ItemType Directory -Path (Join-Path $fixture 'scripts') -Force | Out-Null
    Copy-Item -LiteralPath $sourceScript -Destination (Join-Path $fixture 'scripts/build-adoption-candidate.ps1')
    $fixtureFiles = @{
        '.go-version' = '1.27.3'
        'LICENSE' = 'fixture license'
        'docs/contracts/valkey-webdis.md' = 'fixture client contract'
        'docs/operations/valkey-webdis.md' = 'fixture operator guide'
        'deploy/valkey-webdis/README.md' = 'fixture profile readme'
        'deploy/valkey-webdis/compose.yaml' = ('image: fixture/image@sha256:' + ('a' * 64))
        'deploy/valkey-webdis/webdis.json' = '{}'
        'deploy/valkey-webdis/images.json' = ('{"images":[{"image":"fixture/image","indexDigest":"sha256:' + ('a' * 64) + '"}]}')
    }
    foreach ($relative in $fixtureFiles.Keys) {
        $destination = Join-Path $fixture $relative
        New-Item -ItemType Directory -Path (Split-Path -Parent $destination) -Force | Out-Null
        [IO.File]::WriteAllText($destination, $fixtureFiles[$relative])
    }
    New-Item -ItemType Directory -Path (Join-Path $temporaryDirectory 'go') | Out-Null
    foreach ($notice in @('LICENSE', 'PATENTS')) {
        [IO.File]::WriteAllText((Join-Path $temporaryDirectory "go/$notice"), "fixture Go $notice")
    }
    Set-Location $fixture
    & git init --quiet
    if ($LASTEXITCODE -ne 0) { throw 'Fixture git init failed.' }
    & git -c "safe.directory=$fixture" add .
    if ($LASTEXITCODE -ne 0) { throw 'Fixture git add failed.' }
    & git -c "safe.directory=$fixture" -c user.name=Fixture -c user.email=fixture@example.invalid commit --quiet -m fixture
    if ($LASTEXITCODE -ne 0) { throw 'Fixture git commit failed.' }
    $scriptPath = Join-Path $fixture 'scripts/build-adoption-candidate.ps1'
    $output = Join-Path $temporaryDirectory 'candidate-one'
    $previousCGO = $env:CGO_ENABLED
    & $scriptPath -OutputDirectory $output | Out-Null
    Assert-True ($candidateTestState.count -eq 6) 'Expected two builds for each of three targets.'
    Assert-True ([string]$env:CGO_ENABLED -eq [string]$previousCGO) 'Build environment was not restored.'
    $manifest = Get-Content -LiteralPath (Join-Path $output 'manifest.json') -Raw | ConvertFrom-Json
    Assert-True ($manifest.binaries.Count -eq 3 -and $manifest.candidateVersion -eq 'adoption-r1') 'Candidate manifest identity is incomplete.'
    Assert-True ($manifest.publicationStatus -eq 'unpublished' -and $manifest.qualificationStatus -eq 'pending') 'Candidate must remain unpublished and unqualified.'
    Assert-True (Test-Path -LiteralPath (Join-Path $output 'deploy/valkey-webdis/webdis.json')) 'Profile-relative bind source was not preserved.'
    $checksumLines = Get-Content -LiteralPath (Join-Path $output 'SHA256SUMS')
    foreach ($line in $checksumLines) {
        $hash, $relative = $line -split '  ', 2
        Assert-True ((Get-FileHash -LiteralPath (Join-Path $output $relative)).Hash.ToLowerInvariant() -eq $hash) 'Delivered file checksum mismatch.'
    }
    $secondOutput = Join-Path $temporaryDirectory 'candidate-two'
    & $scriptPath -OutputDirectory $secondOutput | Out-Null
    Assert-True ((Get-FileHash (Join-Path $output 'SHA256SUMS')).Hash -eq (Get-FileHash (Join-Path $secondOutput 'SHA256SUMS')).Hash) 'Repeated candidate bundle differed.'
    Assert-Rejected { & $scriptPath -OutputDirectory $output } 'new or empty'
    Assert-Rejected { & $scriptPath -OutputDirectory (Join-Path $fixture 'candidate') } 'outside the repository'
    Assert-Rejected { & $scriptPath -OutputDirectory 'relative-output' } 'absolute private directory'
    if ($IsWindows) {
        $junction = Join-Path $temporaryDirectory 'checkout-junction'
        New-Item -ItemType Junction -Path $junction -Target $fixture | Out-Null
        try {
            $countBefore = $candidateTestState.count
            Assert-Rejected { & $scriptPath -OutputDirectory (Join-Path $junction 'rejected-candidate') } 'reparse point'
            Assert-True ($candidateTestState.count -eq $countBefore) 'Ancestor junction was not rejected before building.'
            Assert-True (-not (Test-Path -LiteralPath (Join-Path $fixture 'rejected-candidate'))) 'Junction redirected output into the checkout.'
        }
        finally { Remove-Item -LiteralPath $junction -Force }

        # Introduce the junction only after both builds to exercise delivery's recheck.
        $candidateTestState.mode = 'redirect'
        $candidateTestState.junction = $junction
        $candidateTestState.redirectAt = $candidateTestState.count + 6
        try {
            Assert-Rejected { & $scriptPath -OutputDirectory (Join-Path $junction 'rejected-candidate') } 'reparse point'
            Assert-True (-not (Test-Path -LiteralPath (Join-Path $fixture 'rejected-candidate'))) 'Delivery recheck allowed redirected files into the checkout.'
        }
        finally {
            if (Test-Path -LiteralPath $junction) { Remove-Item -LiteralPath $junction -Force }
        }
    }
    $candidateTestState.mode = 'unstable'
    $failedOutput = Join-Path $temporaryDirectory 'unstable'
    Assert-Rejected { & $scriptPath -OutputDirectory $failedOutput } 'not reproducible'
    Assert-True (-not (Test-Path -LiteralPath $failedOutput)) 'Failed reproducibility check delivered candidate files.'
    [IO.File]::WriteAllText((Join-Path $fixture 'untracked.txt'), 'dirty')
    Assert-Rejected { & $scriptPath -OutputDirectory (Join-Path $temporaryDirectory 'dirty') } 'clean Git checkout'
    Write-Output 'Candidate packaging tests passed.'
}
finally {
    Set-Location $previousLocation
    $expectedParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/')
    $actualParent = [IO.Path]::GetFullPath((Split-Path -Parent $temporaryDirectory)).TrimEnd('\', '/')
    if ($actualParent -ne $expectedParent -or (Split-Path -Leaf $temporaryDirectory) -notmatch '^localpost-candidate-tests-[0-9a-f-]{36}$') {
        throw 'Refusing cleanup outside the owned packaging test directory.'
    }
    Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force
}
