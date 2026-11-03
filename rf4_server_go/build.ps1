param(
    [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
$env:RF4_DATA_DIR = Join-Path $PSScriptRoot 'data'
$binDir = Join-Path $PSScriptRoot 'bin'
New-Item -ItemType Directory -Force -Path $binDir | Out-Null

Push-Location $PSScriptRoot
try {
    if (-not $SkipTests) {
        go test ./...
        if ($LASTEXITCODE -ne 0) { throw '测试失败' }
        go vet ./...
        if ($LASTEXITCODE -ne 0) { throw 'go vet 失败' }
    }

    go build -o (Join-Path $binDir 'rf4server.exe') ./cmd/rf4server
    if ($LASTEXITCODE -ne 0) { throw '后端构建失败' }
    Write-Host "构建完成: $binDir\rf4server.exe"
}
finally {
    Pop-Location
}
