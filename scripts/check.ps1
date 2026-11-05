param([string]$ServiceUrl, [string]$MailboxRoot)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$previousToolchain = $env:GOTOOLCHAIN
$previousLocation = Get-Location
try {
    Set-Location $repoRoot
    # Pin the selected version without modifying persistent Go configuration.
    $env:GOTOOLCHAIN = 'go1.27.3'
    $version = & go version
    if ($LASTEXITCODE -ne 0) { throw 'Unable to run the pinned Go toolchain.' }
    Write-Output $version

    $goRoot = & go env GOROOT
    if ($LASTEXITCODE -ne 0) { throw 'Unable to locate the pinned Go toolchain.' }
    # Independent worktrees and cached tools are not this checkout's source.
    $goFiles = Get-ChildItem -LiteralPath $repoRoot -Recurse -File -Filter '*.go' |
        Where-Object {
            $_.Name -notlike '.*' -and
            [System.IO.Path]::GetRelativePath($repoRoot, $_.FullName) -notmatch '^(\.git|\.tools|\.worktrees|bin)[\\/]'
        }
    $unformatted = foreach ($file in $goFiles) {
        & (Join-Path $goRoot 'bin/gofmt.exe') -l $file.FullName
        if ($LASTEXITCODE -ne 0) { throw 'gofmt failed.' }
    }
    if ($unformatted) { throw "Go files require formatting: $unformatted" }

    $lintVersion = (Get-Content (Join-Path $repoRoot '.golangci-version') -Raw).Trim()
    $lint = Join-Path $repoRoot ".tools/golangci-lint/$lintVersion/golangci-lint.exe"
    if (-not (Test-Path -LiteralPath $lint -PathType Leaf)) {
        throw 'Pinned golangci-lint is missing. Run ./scripts/install-tools.ps1 first.'
    }
    $lintInfo = & $lint version --json
    if ($LASTEXITCODE -ne 0) { throw 'Unable to inspect the pinned golangci-lint version.' }
    if (($lintInfo | ConvertFrom-Json).version -ne $lintVersion) {
        throw 'golangci-lint version does not match .golangci-version. Run ./scripts/install-tools.ps1.'
    }
    & $lint config verify --config .golangci.yml
    if ($LASTEXITCODE -ne 0) { throw 'golangci-lint configuration validation failed.' }
    & $lint run --config .golangci.yml ./...
    if ($LASTEXITCODE -ne 0) { throw 'golangci-lint failed.' }

    # The development-only browser pilot uses Node's built-in test runner.
    $expectedNode = (Get-Content (Join-Path $repoRoot '.node-version') -Raw).Trim()
    $nodeVersion = & node --version
    if ($LASTEXITCODE -ne 0 -or $nodeVersion.Trim() -ne "v$expectedNode") {
        throw "Browser pilot checks require Node $expectedNode."
    }
    & node --check examples/browser-diagnostics/pilot.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Browser pilot syntax check failed.' }
    & node --check examples/browser-diagnostics/browser-check.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Browser proof syntax check failed.' }
    & node --test examples/browser-diagnostics/pilot.test.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Browser pilot tests failed.' }
    & node scripts/validate-openapi.mjs
    if ($LASTEXITCODE -ne 0) { throw 'OpenAPI validation failed. Run npm ci if dependencies are missing.' }
    & node --test scripts/validate-openapi.test.mjs scripts/check-openapi-service.test.mjs scripts/qualify-packaged-service.test.mjs
    if ($LASTEXITCODE -ne 0) { throw 'OpenAPI validation tests failed.' }
    foreach ($script in @('container-package.mjs', 'qualify-container.mjs', 'publish-containers.mjs', 'qualify-adoption.mjs')) {
        & node --check (Join-Path $PSScriptRoot $script)
        if ($LASTEXITCODE -ne 0) { throw "Container tooling syntax check failed: $script" }
    }
    & node --check deploy/valkey-webdis/qualify.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Adoption runtime syntax check failed.' }
    & node --test scripts/qualify-adoption.test.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Adoption qualifier tests failed.' }
    & node --test scripts/publish-containers.test.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Container publication tests failed.' }
    & node --check scripts/test-windows-service-upload.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Windows service upload probe syntax check failed.' }
    foreach ($script in @('install-service.ps1', 'test-windows-service.ps1', 'build-adoption-candidate.ps1', 'build-adoption-candidate.test.ps1')) {
        $parseTokens = $null
        $parseErrors = $null
        [System.Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot $script), [ref]$parseTokens, [ref]$parseErrors) | Out-Null
        if ($parseErrors.Count -gt 0) { throw "PowerShell script syntax check failed: $script" }
    }
    & (Join-Path $PSScriptRoot 'build-adoption-candidate.test.ps1')
    if (-not $?) { throw 'Adoption candidate packaging tests failed.' }

    if ($ServiceUrl) {
        if (-not $MailboxRoot) { throw 'Live service checks require -MailboxRoot for the expected server root.' }
        & node scripts/check-openapi-service.mjs $ServiceUrl $MailboxRoot
        if ($LASTEXITCODE -ne 0) { throw 'Live OpenAPI service checks failed.' }
    }

    & go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'go test failed.' }
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'go vet failed.' }
    New-Item -ItemType Directory -Force bin | Out-Null
    # Keep development checks portable across sandbox checkout ownership; do not
    # change persistent Git trust. Release VCS stamping belongs to packaging.
    & go build -buildvcs=false -o bin/localpost.exe ./cmd/localpost
    if ($LASTEXITCODE -ne 0) { throw 'go build failed.' }
    & go build -buildvcs=false -o bin/localpost-client.exe ./cmd/localpost-client
    if ($LASTEXITCODE -ne 0) { throw 'client build failed.' }
    & node --check scripts/qualify-client-process.mjs
    if ($LASTEXITCODE -ne 0) { throw 'Client process proof syntax check failed.' }
    & node scripts/qualify-client-process.mjs bin/localpost-client.exe
    if ($LASTEXITCODE -ne 0) { throw 'Native client process proof failed.' }
}
finally {
    $env:GOTOOLCHAIN = $previousToolchain
    Set-Location $previousLocation
}
