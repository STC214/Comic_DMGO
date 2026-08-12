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
$portableReadme = @"
Comic PC 便携版 $Version

【启动】
解压完整目录后直接运行 Comic_PC.exe。

【环境】
不要求安装 Go、Node.js、Python、Playwright、Chrome 或 Edge。

【内置组件】
- 浏览器：runtime\chromium\chrome.exe
- Playwright 驱动和 Node.js：runtime\playwright\driver
- 下载线路：Rod（默认）和 Playwright-Go
- 配置：config.json，可将 engine 设置为 rod 或 playwright

【运行行为】
程序最高优先级使用本目录内的 Chromium。普通任务启动时浏览器保持隐藏；检测到验证时才显示窗口，验证完成后重新隐藏并继续下载。
下载结果默认写入 download，日志、状态、profile 和缩略图保留在本目录内。

【完整性】
manifest-sha256.json 记录包内文件的大小和 SHA256。
"@
$portableReadme | Set-Content -LiteralPath (Join-Path $bundle 'PORTABLE_README.txt') -Encoding UTF8
$manifest = Get-ChildItem -LiteralPath $bundle -Recurse -File | ForEach-Object {
  [pscustomobject]@{ Path = $_.FullName.Substring($bundle.Length + 1); Size = $_.Length; SHA256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash }
}
$manifest | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath (Join-Path $bundle 'manifest-sha256.json') -Encoding UTF8
Compress-Archive -LiteralPath $bundle -DestinationPath $zip -CompressionLevel Optimal
Write-Output "Bundle: $bundle"
Write-Output "Archive: $zip"
Get-FileHash -LiteralPath $zip -Algorithm SHA256
