$ErrorActionPreference = 'Stop'
$source = Join-Path $PSScriptRoot 'artifacts\original\single-verification-window\Comic_PC.before-single-window.exe'
$target = 'D:\tools\03_Shota_tools\crawler_NH\20260812_Go_myreading\Comic_PC.exe'
if (!(Test-Path -LiteralPath $source -PathType Leaf)) { throw "Rollback source missing: $source" }
Copy-Item -LiteralPath $source -Destination $target -Force
$actual = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash
if ($actual -ne 'D2D354A909C0B19367F15916F41B87D718D0760063E847CEF38889C5D0B4B741') { throw "Rollback hash mismatch: $actual" }
Write-Output "Rollback restored: $target"
Write-Output "SHA256: $actual"
