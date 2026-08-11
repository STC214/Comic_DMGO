param([switch]$Apply)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$backup = Join-Path $root "artifacts\original\.gitignore.pre-ignore-config"
$target = Join-Path $root ".gitignore"
if (!(Test-Path -LiteralPath $backup)) { throw "Missing backup: $backup" }
$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $backup).Hash
if (!$Apply) { Write-Output "VERIFY_ONLY backup_sha256=$hash target_exists=$(Test-Path -LiteralPath $target)"; exit 0 }
Copy-Item -LiteralPath $backup -Destination $target -Force
Write-Output "ROLLBACK_OK restored=$target sha256=$((Get-FileHash -Algorithm SHA256 -LiteralPath $target).Hash)"
