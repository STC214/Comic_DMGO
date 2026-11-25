$ErrorActionPreference = 'Stop'
$source = Join-Path $PSScriptRoot '..\src'
Push-Location -LiteralPath $source
try {
  go run ./tools/playwright_setup
  if ($LASTEXITCODE -ne 0) { throw "Playwright setup failed: exit=$LASTEXITCODE" }
} finally {
  Pop-Location
}
