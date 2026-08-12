param([switch]$Apply)
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$original = Join-Path $root 'artifacts\original\docs-pre-update'
$oldZip = Join-Path $root 'artifacts\original\Comic_PC_Portable_20260812100307.pre-doc-update.zip'
$bundle = Join-Path $root 'dist\Comic_PC_Portable_20260812100307'
$zip = Join-Path $root 'dist\Comic_PC_Portable_20260812100307.zip'
$required = @(
  (Join-Path $original 'README.md'),
  (Join-Path $original '思路.md'),
  (Join-Path $original '旧项目说明.md'),
  (Join-Path $original 'package-portable.ps1'),
  $oldZip
)
foreach ($path in $required) {
  if (!(Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing rollback input: $path" }
}
if (!$Apply) {
  Write-Output "VERIFY_ONLY documentation_originals=$($required.Count) old_zip_sha256=$((Get-FileHash -LiteralPath $oldZip -Algorithm SHA256).Hash)"
  exit 0
}
Copy-Item -LiteralPath (Join-Path $original 'README.md') -Destination (Join-Path $root 'README.md') -Force
Copy-Item -LiteralPath (Join-Path $original '思路.md') -Destination (Join-Path $root '思路.md') -Force
Copy-Item -LiteralPath (Join-Path $original '旧项目说明.md') -Destination (Join-Path $root '旧项目说明.md') -Force
Copy-Item -LiteralPath (Join-Path $original 'package-portable.ps1') -Destination (Join-Path $root 'package-portable.ps1') -Force
Copy-Item -LiteralPath $oldZip -Destination $zip -Force
$temp = Join-Path $root 'artifacts\.rollback-documentation-package'
if (Test-Path -LiteralPath $temp) { throw "Rollback temp path already exists: $temp" }
Expand-Archive -LiteralPath $oldZip -DestinationPath $temp
$oldBundle = Join-Path $temp 'Comic_PC_Portable_20260812100307'
Copy-Item -LiteralPath (Join-Path $oldBundle 'PORTABLE_README.txt') -Destination (Join-Path $bundle 'PORTABLE_README.txt') -Force
Copy-Item -LiteralPath (Join-Path $oldBundle 'manifest-sha256.json') -Destination (Join-Path $bundle 'manifest-sha256.json') -Force
Remove-Item -LiteralPath $temp -Recurse -Force
Write-Output "ROLLBACK_OK documentation restored; archive_sha256=$((Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash)"
