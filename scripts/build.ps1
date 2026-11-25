param([string]$OutputPath)
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$exe = if ($OutputPath) {
  $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputPath)
} else {
  Join-Path $root 'bin\ComicPC.exe'
}
Push-Location -LiteralPath (Join-Path $root 'src')
try {
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $exe) | Out-Null
  $version = Get-Date -Format 'yyyyMMddHHmmss'
  if ($version -notmatch '^\d{14}$') {
    throw "Invalid build version: $version"
  }
  go test ./...
  if ($LASTEXITCODE -ne 0) { throw "go test failed: exit=$LASTEXITCODE" }
  go build -trimpath -ldflags "-H=windowsgui -X main.appVersion=$version" -o $exe .
  if ($LASTEXITCODE -ne 0) { throw "go build failed: exit=$LASTEXITCODE" }
  Write-Output "Version: $version"
  Get-FileHash -LiteralPath $exe -Algorithm SHA256
} finally {
  Pop-Location
}
