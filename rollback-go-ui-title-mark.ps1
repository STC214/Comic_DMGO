$ErrorActionPreference = 'Stop'
$source = Join-Path $PSScriptRoot 'artifacts\original\go-ui-title-mark\Comic_PC.before-go-title-mark.exe'
$target = 'D:\tools\03_Shota_tools\crawler_NH\20260812_Go_myreading\Comic_PC.exe'
if (!(Test-Path -LiteralPath $source -PathType Leaf)) { throw "Rollback source missing: $source" }
Copy-Item -LiteralPath $source -Destination $target -Force
$actual = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash
if ($actual -ne '4F78B6295A2ECFCFEC68C23467525FD16D22990EAB8AB0A10EBA06EE35B3E0A1') { throw "Rollback hash mismatch: $actual" }
Write-Output "Rollback restored: $target"
Write-Output "SHA256: $actual"
