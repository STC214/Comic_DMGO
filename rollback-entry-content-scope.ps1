$ErrorActionPreference = 'Stop'

$target = 'D:\tools\03_Shota_tools\crawler_NH\20260812_Go_myreading\Comic_PC.exe'
$backup = 'D:\tools\03_Shota_tools\crawler_NH\20260812_Go_myreading\Comic_PC.exe.pre-entry-content-20260908.bak'
$expectedBackupHash = '3BF643C4BEB32E26B64AE59411F43ACBAB16B2A46D265B1C269A1BF3D6F94A85'

if ((Get-FileHash -LiteralPath $backup -Algorithm SHA256).Hash -ne $expectedBackupHash) {
    throw "Rollback backup hash mismatch: $backup"
}
Copy-Item -LiteralPath $backup -Destination $target -Force
$restoredHash = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash
if ($restoredHash -ne $expectedBackupHash) {
    throw "Rollback verification failed: $target"
}
Write-Output "Restored: $target"
Write-Output "SHA256: $restoredHash"
