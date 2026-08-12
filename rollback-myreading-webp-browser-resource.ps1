$ErrorActionPreference = 'Stop'
$source = Join-Path $PSScriptRoot 'artifacts\original\myreading-webp-browser-resource\Comic_PC.before-webp-resource-fix.exe'
$target = 'D:\tools\03_Shota_tools\crawler_NH\20260812_Go_myreading\Comic_PC.exe'
if (!(Test-Path -LiteralPath $source -PathType Leaf)) { throw "Rollback source missing: $source" }
Copy-Item -LiteralPath $source -Destination $target -Force
$actual=(Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash
if ($actual -ne '1A2BD31C9EB5C359BCF45352E45AD5D99D0E10299E2423185FCB31AF17A94D78') { throw "Rollback hash mismatch: $actual" }
Write-Output "Rollback restored: $target"
Write-Output "SHA256: $actual"
