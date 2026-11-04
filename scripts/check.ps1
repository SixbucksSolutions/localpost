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
    $unformatted = & (Join-Path $goRoot 'bin/gofmt.exe') -l .
    if ($LASTEXITCODE -ne 0) { throw 'gofmt failed.' }
    if ($unformatted) { throw "Go files require formatting: $unformatted" }

    & go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'go test failed.' }
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'go vet failed.' }
    New-Item -ItemType Directory -Force bin | Out-Null
    # Keep development checks portable across sandbox checkout ownership; do not
    # change persistent Git trust. Release VCS stamping belongs to packaging.
    & go build -buildvcs=false -o bin/localpost.exe ./cmd/localpost
    if ($LASTEXITCODE -ne 0) { throw 'go build failed.' }
}
finally {
    $env:GOTOOLCHAIN = $previousToolchain
    Set-Location $previousLocation
}
