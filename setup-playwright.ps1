$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
  go run ./tools/playwright_setup
} finally {
  Pop-Location
}
