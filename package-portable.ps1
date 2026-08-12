param(
  [Parameter(Mandatory = $true)][string]$Version,
  [string]$ChromiumSource = 'F:\Project\01_Comics_Images\comic_downloader\runtime\chromium',
  [string]$PlaywrightDriver = "$env:LOCALAPPDATA\ms-playwright-go\1.61.1"
)
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$exe = Join-Path $root 'bin\ComicPC.exe'
if (!(Test-Path -LiteralPath $exe -PathType Leaf)) { throw "Missing executable: $exe" }
if (!(Test-Path -LiteralPath (Join-Path $ChromiumSource 'chrome.exe') -PathType Leaf)) { throw "Missing Chromium: $ChromiumSource" }
if (!(Test-Path -LiteralPath (Join-Path $PlaywrightDriver 'node.exe') -PathType Leaf) -or
    !(Test-Path -LiteralPath (Join-Path $PlaywrightDriver 'package\cli.js') -PathType Leaf)) {
  throw "Missing Playwright driver: $PlaywrightDriver"
}
$distRoot = Join-Path $root 'dist'
$bundle = Join-Path $distRoot ("Comic_PC_Portable_" + $Version)
$zip = $bundle + '.zip'
if (Test-Path -LiteralPath $bundle) { throw "Bundle already exists: $bundle" }
if (Test-Path -LiteralPath $zip) { throw "Archive already exists: $zip" }
New-Item -ItemType Directory -Force -Path $bundle,(Join-Path $bundle 'runtime'),(Join-Path $bundle 'workers'),(Join-Path $bundle 'download'),(Join-Path $bundle 'logs'),(Join-Path $bundle '.tmp') | Out-Null
Copy-Item -LiteralPath $exe -Destination (Join-Path $bundle 'Comic_PC.exe')
Copy-Item -LiteralPath (Join-Path $root 'config.json') -Destination $bundle
Copy-Item -LiteralPath (Join-Path $root 'assets') -Destination $bundle -Recurse
Copy-Item -LiteralPath $ChromiumSource -Destination (Join-Path $bundle 'runtime') -Recurse
$portableDriver = Join-Path $bundle 'runtime\playwright\driver'
New-Item -ItemType Directory -Force -Path $portableDriver | Out-Null
Copy-Item -Path (Join-Path $PlaywrightDriver '*') -Destination $portableDriver -Recurse
Set-Content -LiteralPath (Join-Path $bundle 'workers\.keep') -Value 'Portable-root marker; Go myreading workers are built into Comic_PC.exe.' -Encoding UTF8
@"
Comic PC portable $Version

Run Comic_PC.exe directly. No Go, Node.js, Python, browser installation, or Playwright setup is required.
Bundled routes: Rod and Playwright. Bundled browser: runtime\chromium\chrome.exe.
Writable state, logs, profiles, thumbnails, and downloads remain inside this directory.
"@ | Set-Content -LiteralPath (Join-Path $bundle 'PORTABLE_README.txt') -Encoding UTF8
$manifest = Get-ChildItem -LiteralPath $bundle -Recurse -File | ForEach-Object {
  [pscustomobject]@{ Path = $_.FullName.Substring($bundle.Length + 1); Size = $_.Length; SHA256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash }
}
$manifest | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath (Join-Path $bundle 'manifest-sha256.json') -Encoding UTF8
Compress-Archive -LiteralPath $bundle -DestinationPath $zip -CompressionLevel Optimal
Write-Output "Bundle: $bundle"
Write-Output "Archive: $zip"
Get-FileHash -LiteralPath $zip -Algorithm SHA256
