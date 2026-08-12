param([switch]$Apply)
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$target = 'D:\tools\03_Shota_tools\crawler_NH\20260812_Go_myreading'
$original = Join-Path $root 'artifacts\original\portable-add-task-fix'
$oldExe = Join-Path $original 'Comic_PC.portable-before-add-task-fix.exe'
$oldManifest = Join-Path $original 'manifest-sha256.json'
$oldReadme = Join-Path $original 'PORTABLE_README.txt'
foreach ($path in @($oldExe, $oldManifest, $oldReadme)) {
  if (!(Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing rollback input: $path" }
}
if (!$Apply) {
  Write-Output "VERIFY_ONLY old_exe_sha256=$((Get-FileHash -LiteralPath $oldExe -Algorithm SHA256).Hash) target=$target"
  exit 0
}
$running = Get-CimInstance Win32_Process | Where-Object {
  $_.Name -eq 'Comic_PC.exe' -and $_.ExecutablePath -like "$target*"
}
if ($running) { throw "Close the portable Comic_PC.exe before rollback" }
Copy-Item -LiteralPath $oldExe -Destination (Join-Path $target 'Comic_PC.exe') -Force
Copy-Item -LiteralPath $oldManifest -Destination (Join-Path $target 'manifest-sha256.json') -Force
Copy-Item -LiteralPath $oldReadme -Destination (Join-Path $target 'PORTABLE_README.txt') -Force
Copy-Item -LiteralPath $oldExe -Destination (Join-Path $root 'bin\ComicPC.exe') -Force
Write-Output "ROLLBACK_OK target_exe_sha256=$((Get-FileHash -LiteralPath (Join-Path $target 'Comic_PC.exe') -Algorithm SHA256).Hash)"
