# Comic PC

Windows 漫画下载器。目前 `myreadingmanga` 下载链路由 Go 实现，并保留两条可切换的浏览器控制线路：

- `rod`：Rod + CDP，默认线路。
- `playwright`：Playwright-Go + CDP，备用线路。

两条线路共用相同 UI、任务状态、验证会话、图片校验、断点续传、实时速度和资源回收逻辑。

## 当前下载流程

1. 使用真实 Chromium 和任务独立 profile 启动浏览器。
2. 浏览器以普通有头模式启动，但窗口在首次显示前移出桌面并隐藏。
3. 检测到验证页面时显示同一个浏览器窗口，等待用户完成验证。
4. 验证完成后隐藏窗口，采集标题、分页和原图地址。
5. 优先通过浏览器会话读取图片；失败时携带 Cookie、Referer 和 User-Agent 走 HTTP 回退。
6. 图片先写入 `.part`，解码校验成功后再原子重命名。
7. 已存在且校验通过的图片直接复用，支持任务重试和断点续传。
8. 任务结束、失败或取消后关闭浏览器并清理任务 profile 和相关进程。

## 实时下载速度

- Rod 和 Playwright 通过 CDP `Network.dataReceived` 报告单张图片传输中的字节增量。
- HTTP 回退通过 `io.Copy` 写入增量报告传输字节。
- UI 约每 500ms 采样一次；停滞时显示 `实时 0 B/s`，恢复传输后继续显示实时速率。
- 断点续传时复用的本地文件不计入网络速度。

## 配置

`config.json`：

```json
{
  "engine": "rod",
  "proxy": "",
  "maxPages": 100,
  "retries": 3,
  "requestDelayMs": 250,
  "pageTimeoutSeconds": 90
}
```

将 `engine` 设为 `rod` 或 `playwright`。环境变量 `MYREADING_ENGINE` 可临时覆盖该值。

浏览器选择优先级：

1. 程序目录下的 `runtime\chromium\chrome.exe`。
2. 开发期原项目 Chromium。
3. UI 中保存的浏览器路径。
4. 系统 Chrome、Edge、Chromium 或 Brave。

因此便携包始终优先使用包内 Chromium。

## 构建与测试

```powershell
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
.\build.ps1
```

`build.ps1` 会先执行测试，再生成 `bin\ComicPC.exe`。版本号采用本地构建时间：

```text
yyyyMMddHHmmss
```

例如：`20260812100307`。

## 开发环境运行

默认 Rod 线路不需要额外安装。Playwright 开发线路首次使用可执行：

```powershell
.\setup-playwright.ps1
```

然后启动：

```powershell
.\bin\ComicPC.exe
```

默认下载目录为 `download\<作品标题>\`。

## 便携包

便携包包括：

- `Comic_PC.exe`
- Chromium 完整运行文件
- Playwright 驱动及 Node.js
- Rod 和 Playwright 所需资源
- 字体、配置及可写运行目录
- SHA256 文件清单

当前包：

```text
dist\Comic_PC_Portable_20260812105826.zip
```

解压后直接运行 `Comic_PC.exe`，不要求目标机器预装 Go、Node.js、Python、Playwright 或浏览器。

重新制作便携包：

```powershell
.\package-portable.ps1 -Version <14位版本号>
```

## 主要文件

- `main.go`：任务、状态、持久化和通用资源路径。
- `fyne_ui_windows.go`：Windows/Fyne UI。
- `myreading_worker.go`：下载全流程、续传、校验与实时速度。
- `myreading_rod.go`：Rod 浏览器实现。
- `myreading_playwright.go`：Playwright-Go 浏览器实现。
- `chromium_windows.go`：Chromium 解析、下载与便携优先级。
- `build.ps1`：测试、版本注入和构建。
- `package-portable.ps1`：自包含便携包生成。
- `artifacts\portable-verification.txt`：便携包验证记录。

## 已验证行为

- 完整阅读任务下载成功。
- 验证窗口仅在需要用户操作时显示。
- 验证完成后浏览器保持隐藏并继续下载。
- Rod、Playwright 两条线路可采集页面和图片。
- 实时速度、零速显示、断点续传和资源回收通过单元测试及竞态检测。
- 便携包在空 `LOCALAPPDATA`、无外部 Playwright 路径的隔离条件下启动成功。
