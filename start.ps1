param(
    [switch]$BuildOnly,
    [switch]$SkipInstall,
    [string]$Addr = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$frontend = Join-Path $root "frontend"

function Find-CommandOrFail([string]$name, [string]$hint) {
    $cmd = Get-Command $name -ErrorAction SilentlyContinue
    if (-not $cmd) {
        throw "$name was not found. $hint"
    }
    return $cmd.Source
}

function Add-ToolToPath([string]$name, [string[]]$candidates, [string]$hint) {
    $cmd = Get-Command $name -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    foreach ($candidate in $candidates) {
        if (Test-Path $candidate) {
            $dir = Split-Path -Parent $candidate
            if (-not (($env:PATH -split ';') -contains $dir)) {
                $env:PATH = "$dir;$env:PATH"
            }
            return $candidate
        }
    }
    throw "$name was not found. $hint"
}

Set-Location $root
$userHome = [Environment]::GetFolderPath('UserProfile')
$nodeInstallDirs = @(
    (Join-Path ${env:ProgramFiles} "nodejs"),
    (Join-Path $env:LOCALAPPDATA "Programs\nodejs")
)
foreach ($nodeDir in $nodeInstallDirs) {
    if (Test-Path (Join-Path $nodeDir "node.exe")) {
        if (-not (($env:PATH -split ';') -contains $nodeDir)) { $env:PATH = "$nodeDir;$env:PATH" }
        break
    }
}
$pnpmPath = $null
$pnpmCmd = Get-Command pnpm -ErrorAction SilentlyContinue
if ($pnpmCmd) { $pnpmPath = $pnpmCmd.Source }
$npmPath = $null
$npmCmd = Get-Command npm -ErrorAction SilentlyContinue
if ($npmCmd) { $npmPath = $npmCmd.Source }
if (-not $pnpmPath -and -not $npmPath) {
    throw "pnpm or npm was not found. Please install Node.js first."
}
$goPath = Add-ToolToPath "go" @(
    "C:\Program Files\Go\bin\go.exe",
    "C:\Go\bin\go.exe",
    (Join-Path $userHome "scoop\apps\go\current\bin\go.exe")
) "Please install Go 1.25 or newer."

if (-not (Test-Path (Join-Path $frontend "node_modules")) -and -not $SkipInstall) {
    if ($pnpmPath) {
        & $pnpmPath --dir $frontend install
    } else {
        & $npmPath --prefix $frontend install
    }
}

if ($pnpmPath) {
    & $pnpmPath --dir $frontend run build
} else {
    & $npmPath --prefix $frontend run build
}

if ($LASTEXITCODE -ne 0) {
    throw "Frontend build failed."
}

if ($BuildOnly) {
    Write-Host "Frontend build completed."
    exit 0
}

if ($env:OS -eq "Windows_NT") {
    Write-Warning "Windows 模式仅保证管理台/API、邮箱池和公共运维能力；Grok 自动注册的 Turnstile helper 需要 Linux/CloakBrowser 环境。"
}

if ($Addr) {
    $env:ADDR = $Addr
}
Write-Host "Starting xh-grok-reg..."
& $goPath run .
exit $LASTEXITCODE
