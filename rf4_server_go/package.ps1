param(
    [string]$Version = '0.1.0',
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$date = Get-Date -Format 'yyyyMMdd'
$name = "rf4_server_go_v${Version}_${date}"
$dist = Join-Path $root 'dist'
$stage = Join-Path $dist $name
$zip = Join-Path $dist ($name + '.zip')
$sha = Join-Path $dist ($name + '.sha256')

if (-not $SkipBuild) {
    & (Join-Path $root 'build.ps1')
    if ($LASTEXITCODE -ne 0) { throw '构建失败，停止打包' }
}

New-Item -ItemType Directory -Force -Path $dist | Out-Null
if (Test-Path -LiteralPath $stage) {
    throw "目标目录已存在，请先确认并移走: $stage"
}

New-Item -ItemType Directory -Force -Path $stage | Out-Null
foreach ($dir in @('cmd', 'internal', 'data', 'docs', 'tools')) {
    $src = Join-Path $root $dir
    if (Test-Path -LiteralPath $src) {
        Copy-Item -LiteralPath $src -Destination $stage -Recurse -Force
    }
}
foreach ($file in @('README.md', '交付说明书.md', '开发交接说明.md', 'VERSION.txt',
                    'LICENSE', '.gitignore', 'go.mod', 'go.sum', 'build.ps1',
                    'start-server.ps1', 'package.ps1')) {
    $src = Join-Path $root $file
    if (Test-Path -LiteralPath $src) {
        Copy-Item -LiteralPath $src -Destination (Join-Path $stage $file) -Force
    }
}

$binSrc = Join-Path $root 'bin\rf4server.exe'
$binDst = Join-Path $stage 'bin'
New-Item -ItemType Directory -Force -Path $binDst | Out-Null
if (-not (Test-Path -LiteralPath $binSrc)) {
    throw "缺少构建产物: $binSrc"
}
Copy-Item -LiteralPath $binSrc -Destination (Join-Path $binDst 'rf4server.exe') -Force

$hashLines = Get-ChildItem -Recurse -File -LiteralPath $stage |
    Sort-Object FullName |
    ForEach-Object {
        $relative = $_.FullName.Substring($stage.Length + 1)
        $hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        "$hash  $relative"
    }
$hashLines | Set-Content -LiteralPath (Join-Path $stage 'SHA256SUMS.txt') -Encoding utf8

if (Test-Path -LiteralPath $zip) { throw "目标压缩包已存在: $zip" }
Compress-Archive -LiteralPath $stage -DestinationPath $zip -CompressionLevel Optimal
$zipHash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
"$zipHash  $($name).zip" | Set-Content -LiteralPath $sha -Encoding utf8

Write-Host "交付包: $zip"
Write-Host "校验文件: $sha"
Write-Host "SHA256: $zipHash"
