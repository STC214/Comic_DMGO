param([switch]$Apply)
$ErrorActionPreference = 'Stop'

$root = (Resolve-Path -LiteralPath $PSScriptRoot).Path
if ($root -ne 'F:\Project\01_Comics_Images\202608_Comic_PC') {
  throw "Unexpected rollback root: $root"
}

$current = Join-Path $root 'bin\ComicPC.exe'
$previous = Join-Path $root 'artifacts\original\ComicPC.pre-browser-resource-fix.exe'
$expectedPreviousSHA256 = '4BAAB47568DAD765DCC0153CAB7ACB3C7D02A0BD06D9925B221DA18D25C9A019'

if (-not (Test-Path -LiteralPath $previous -PathType Leaf)) {
  throw "Previous executable missing: $previous"
}
$actualPreviousSHA256 = (Get-FileHash -LiteralPath $previous -Algorithm SHA256).Hash
if ($actualPreviousSHA256 -ne $expectedPreviousSHA256) {
  throw "Previous executable hash mismatch: $actualPreviousSHA256"
}

if (-not $Apply) {
  Write-Output "VERIFY_ONLY root=$root previous_sha256=$actualPreviousSHA256 current_exists=$(Test-Path -LiteralPath $current -PathType Leaf)"
  exit 0
}

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $current) | Out-Null
$staging = "$current.rollback-staging"
Copy-Item -LiteralPath $previous -Destination $staging -Force
Move-Item -LiteralPath $staging -Destination $current -Force
$restoredSHA256 = (Get-FileHash -LiteralPath $current -Algorithm SHA256).Hash
if ($restoredSHA256 -ne $expectedPreviousSHA256) {
  throw "Restored executable hash mismatch: $restoredSHA256"
}
Write-Output "ROLLBACK_OK restored=$current sha256=$restoredSHA256"
