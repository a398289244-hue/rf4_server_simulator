param(
    [int]$TcpPort = 19213,
    [int]$HttpPort = 19214,
    [int]$HttpCompatPort = 7515,
    [string]$MyHost = '127.0.0.1',
    [int]$MyPort = 3306,
    [string]$MyUser = 'rf4',
    [string]$MyPass = 'rf4pass',
    [string]$MyDb = 'rf4',
    [string]$Logs = (Join-Path $PSScriptRoot 'logs')
)

$ErrorActionPreference = 'Stop'
$dataDir = Join-Path $PSScriptRoot 'data'
$exe = Join-Path $PSScriptRoot 'bin\rf4server.exe'

if (-not (Test-Path -LiteralPath (Join-Path $dataDir 'wire_tables.json'))) {
    throw "缺少静态数据目录: $dataDir"
}
if (-not (Test-Path -LiteralPath $exe)) {
    throw "缺少后端程序: $exe，请先运行 .\build.ps1"
}

$env:RF4_DATA_DIR = $dataDir
New-Item -ItemType Directory -Force -Path $Logs | Out-Null

& $exe `
    --tcp-port $TcpPort `
    --http-port $HttpPort `
    --http-compat-port $HttpCompatPort `
    --logs $Logs `
    --mysql-host $MyHost `
    --mysql-port $MyPort `
    --mysql-user $MyUser `
    --mysql-pass $MyPass `
    --mysql-db $MyDb
