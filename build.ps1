$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
  New-Item -ItemType Directory -Force -Path bin | Out-Null
  go test ./...
  go build -trimpath -ldflags '-H=windowsgui' -o bin/ComicPC.exe .
  Get-FileHash bin/ComicPC.exe -Algorithm SHA256
} finally {
  Pop-Location
}
