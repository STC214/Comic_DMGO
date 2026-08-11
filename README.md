# Comic PC（Go 双引擎版）

复用原 `comic_downloader` 的 Windows/Fyne UI 与任务管理模型，`myreading` 下载链路已改为纯 Go。

## 两条线路

1. **Rod + Stealth**（默认）：Go 原生 CDP 控制、持久 profile、Stealth 初始化。
2. **Playwright-Go**：持久 BrowserContext，复用已配置的 Chromium、Chrome 或 Edge。

两条线路共用完整流程：进入阅读页 → 滚动触发懒加载 → 提取分页与原图 → 去重 → 携带浏览器 Cookie/Referer 下载 → `.part` 原子落盘 → 更新 UI 进度、标题、目录与缩略图。

## 使用

1. Rod 线路可直接运行；Playwright 线路首次使用前执行：

   ```powershell
   .\setup-playwright.ps1
   ```

2. 编辑 `config.json`，将 `engine` 设为 `rod` 或 `playwright`。环境变量 `MYREADING_ENGINE` 可临时覆盖配置。

3. 构建并测试：

   ```powershell
   .\build.ps1
   ```

4. 启动 `bin\ComicPC.exe`，在 UI 设置 Chromium 路径，添加阅读页 URL 后开始任务。

下载默认写入 `download\<作品标题>\0001.jpg ...`。profile、日志、状态与下载目录在运行时按需创建。

## 验证

```powershell
go test ./...
$env:MYREADING_BROWSER_SMOKE='1'
$env:MYREADING_BROWSER_BIN='C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe'
go test -run TestBrowserEnginesCollectReaderFixture -v -count=1
```

冒烟测试使用本地两页 reader fixture，分别验证 Rod 与 Playwright 的浏览器启动、标题、图片 URL 和下一页提取。
