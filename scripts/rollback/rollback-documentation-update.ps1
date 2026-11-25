param([switch]$Apply)
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..')).Path
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
if (Test-Path -LiteralPath (Join-Path $root 'src\go.mod') -PathType Leaf) {
  throw 'This rollback needs a checkout with the former root layout.'
}
$docs = Join-Path $root 'docs'
foreach ($path in @($docs, $bundle)) {
  if (!(Test-Path -LiteralPath $path -PathType Container)) { throw "Missing rollback destination: $path" }
}

# Read the two required entries before touching any destination. Do not unpack
# the complete historical portable bundle merely to restore these files.
Add-Type -AssemblyName System.IO.Compression.FileSystem
$packageDocs = @{}
$bundleName = Split-Path -Leaf $bundle
$archive = [IO.Compression.ZipFile]::OpenRead($oldZip)
try {
  foreach ($name in @('PORTABLE_README.txt', 'manifest-sha256.json')) {
    $entry = $archive.GetEntry("$bundleName/$name")
    if (!$entry) { $entry = $archive.GetEntry("$bundleName\$name") }
    if (!$entry) { throw "Missing entry in rollback archive: $bundleName/$name" }
    $stream = $entry.Open()
    $bytes = New-Object IO.MemoryStream
    try {
      $stream.CopyTo($bytes)
      $packageDocs[$name] = $bytes.ToArray()
    } finally {
      $bytes.Dispose()
      $stream.Dispose()
    }
  }
} finally {
  $archive.Dispose()
}

Copy-Item -LiteralPath (Join-Path $original 'README.md') -Destination (Join-Path $root 'README.md') -Force
Copy-Item -LiteralPath (Join-Path $original '思路.md') -Destination (Join-Path $docs '思路.md') -Force
Copy-Item -LiteralPath (Join-Path $original '旧项目说明.md') -Destination (Join-Path $docs '旧项目说明.md') -Force
Copy-Item -LiteralPath (Join-Path $original 'package-portable.ps1') -Destination (Join-Path $root 'package-portable.ps1') -Force
Copy-Item -LiteralPath $oldZip -Destination $zip -Force
foreach ($name in $packageDocs.Keys) {
  [IO.File]::WriteAllBytes((Join-Path $bundle $name), $packageDocs[$name])
}
Write-Output "ROLLBACK_OK documentation restored; archive_sha256=$((Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash)"
