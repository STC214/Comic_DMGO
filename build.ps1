$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
  New-Item -ItemType Directory -Force -Path bin | Out-Null
  $version = Get-Date -Format 'yyyyMMddHHmmss'
  if ($version -notmatch '^\d{14}$') {
    throw "Invalid build version: $version"
  }
  go test ./...
  if ($LASTEXITCODE -ne 0) { throw "go test failed: exit=$LASTEXITCODE" }
  go build -trimpath -ldflags "-H=windowsgui -X main.appVersion=$version" -o bin/ComicPC.exe .
  if ($LASTEXITCODE -ne 0) { throw "go build failed: exit=$LASTEXITCODE" }
  Write-Output "Version: $version"
  Get-FileHash bin/ComicPC.exe -Algorithm SHA256
} finally {
  Pop-Location
}
