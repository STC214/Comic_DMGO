param([switch]$Apply)
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..')).Path
$source = Join-Path $root 'artifacts\original\myreading-webp-browser-resource\Comic_PC.before-webp-resource-fix.exe'
$target = 'D:\tools\03_Shota_tools\crawler_NH\20260812_Go_myreading\Comic_PC.exe'
if (!(Test-Path -LiteralPath $source -PathType Leaf)) { throw "Rollback source missing: $source" }
$expected = '1A2BD31C9EB5C359BCF45352E45AD5D99D0E10299E2423185FCB31AF17A94D78'
if ((Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash -ne $expected) { throw "Rollback backup hash mismatch: $source" }
if (!$Apply) { Write-Output "VERIFY_ONLY backup_sha256=$expected target=$target"; exit 0 }
Copy-Item -LiteralPath $source -Destination $target -Force
$actual=(Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash
if ($actual -ne $expected) { throw "Rollback hash mismatch: $actual" }
Write-Output "Rollback restored: $target"
Write-Output "SHA256: $actual"
