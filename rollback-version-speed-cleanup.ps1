param([switch]$Apply)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$previous = Join-Path $root "artifacts\original\ComicPC.pre-version-speed-cleanup.exe"
$current = Join-Path $root "bin\ComicPC.exe"
if (!(Test-Path -LiteralPath $previous)) { throw "Missing previous artifact: $previous" }
$previousHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $previous).Hash
if (!$Apply) { Write-Output "VERIFY_ONLY previous_sha256=$previousHash current_exists=$(Test-Path -LiteralPath $current)"; exit 0 }
Copy-Item -LiteralPath $previous -Destination $current -Force
$restored = (Get-FileHash -Algorithm SHA256 -LiteralPath $current).Hash
if ($restored -ne $previousHash) { throw "Rollback hash mismatch" }
Write-Output "ROLLBACK_OK restored=$current sha256=$restored"
