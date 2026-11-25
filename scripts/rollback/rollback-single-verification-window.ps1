param([switch]$Apply)
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..')).Path
$source = Join-Path $root 'artifacts\original\single-verification-window\Comic_PC.before-single-window.exe'
$target = 'D:\tools\03_Shota_tools\crawler_NH\20260812_Go_myreading\Comic_PC.exe'
if (!(Test-Path -LiteralPath $source -PathType Leaf)) { throw "Rollback source missing: $source" }
$expected = 'D2D354A909C0B19367F15916F41B87D718D0760063E847CEF38889C5D0B4B741'
if ((Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash -ne $expected) { throw "Rollback backup hash mismatch: $source" }
if (!$Apply) { Write-Output "VERIFY_ONLY backup_sha256=$expected target=$target"; exit 0 }
Copy-Item -LiteralPath $source -Destination $target -Force
$actual = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash
if ($actual -ne $expected) { throw "Rollback hash mismatch: $actual" }
Write-Output "Rollback restored: $target"
Write-Output "SHA256: $actual"
