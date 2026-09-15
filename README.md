# 电脑运行监控

电脑运行监控是一套面向 Windows 主机和 Android 局域网终端的实时系统监控工具。它由 Go 后端、Windows Gio 桌面窗口、内嵌 Web 仪表盘和 Android 原生客户端组成，三端采用统一的赛博朋克 HUD 界面。

程序适合把闲置手机或平板改造成电脑状态屏、桌面时钟和相册播放器。Windows 端负责采集数据并通过 SSE 实时推送，Android 端可自动发现同一局域网中的电脑，无需云服务。

## 项目组成

| 组件 | 作用 |
| --- | --- |
| Go 服务端 | 采集系统数据、提供 HTTP/SSE 接口、响应局域网发现请求并管理幻灯片图片 |
| Windows 桌面端 | 使用 Gio 显示本机监控面板、管理幻灯片目录和审批新设备接入 |
| Web 仪表盘 | 嵌入 `monitor.exe`，浏览器访问服务地址即可查看实时数据 |
| Android 客户端 | 在手机或平板上显示监控面板、桌面时钟、应用启动器和图片幻灯片 |

## 功能介绍

### 系统状态监控

- CPU 总占用率和每个核心的实时占用率
- 内存使用量、占用比例和可用容量
- 磁盘容量、读写速度和忙碌程度
- 网络实时上传/下载速度与累计流量
- CPU、GPU 和磁盘温度
- 系统运行时间和当前时间

### 进程与端口

- 展示进程名、PID、CPU、内存、网络流量和监听端口
- 支持按表头排序
- 支持同名进程合并视图和单进程视图
- 进程采集与端口扫描独立刷新，避免阻塞主监控数据

### Windows、Web 与 Android 三端界面

- Windows Gio 原生窗口与系统托盘
- 无需额外静态文件的内嵌 Web 仪表盘
- Android 原生 HUD 面板，兼容 Android 5.0（API 21）及以上系统
- Android 端同时显示平板自身的 CPU、电量、充电状态和开机时长
- CPU 超阈值红框呼吸告警，阈值可配置
- 万年历大时钟，时钟宽度比例可调

### 局域网连接与设备审批

- 服务端默认监听 TCP `8765`
- Android 通过 UDP `8766` 自动发现同一局域网中的服务端
- 也可在 Android 客户端中手动输入电脑地址
- 新局域网 IP 首次读取监控数据时，Windows 窗口会显示允许或拒绝提示
- 审批 30 秒超时自动拒绝；拒绝后 60 秒内不重复弹窗
- 已允许设备在本次程序运行期间无需再次审批

### Android 桌面与应用启动器

- 可注册为 Android HOME 桌面候选
- 提供设备已安装应用的图标列表并可直接启动应用
- 适合将旧手机或平板作为长期亮屏的电脑状态面板

### 图片幻灯片

- 在 Windows 端配置一个或多个图片目录
- 递归扫描 JPG、JPEG、PNG、WebP、GIF 和 BMP 图片
- 可手动扫描各磁盘中名称包含“相册”的目录，并在确认后保存
- Android 设备无触摸达到设定时间后进入沉浸式全屏播放
- 支持完整显示和铺满裁切两种模式，并可设置单张停留时间
- 任意触摸即可退出幻灯片并返回监控面板
- 可选平板本地缓存；可用空间低于 200 MiB 时自动停止缓存
- 使用稳定图片 ID 保存下一张播放位置，应用或设备重启后可以续播
- 提供按需加载的缩略图列表，轻触后可全屏查看单张图片
- 服务端接口不向客户端暴露电脑上的原始文件路径

## 安全说明

本项目面向可信局域网，不应直接暴露到公网。服务端默认监听所有网卡并使用 HTTP 明文传输，监控数据可能包含进程名、资源占用和监听端口，幻灯片功能会向获准设备提供所选目录中的图片。

- 只需本机访问时，设置 `MONITOR_LAN=0`
- `MONITOR_NO_GUI=1` 会关闭桌面窗口和设备审批，并自动允许远端数据请求
- 不要在公共 Wi-Fi 或开启公网端口转发的环境下使用默认配置
- 只添加允许局域网设备查看的图片目录

## 目录结构

```text
.
├─ android-client/              Android 原生客户端
├─ assets/                      图标等源资源
├─ bin/                         本地构建产物和运行数据库（不提交）
├─ cmd/monitor/                 Go 服务端和 Windows Gio 界面
│  ├─ web/dashboard.html        嵌入 EXE 的 Web 仪表盘
│  ├─ main.go / gui.go          HTTP 服务与桌面界面
│  ├─ slideshow.go              幻灯片扫描与图片接口
│  └─ *_test.go                 Go 测试
├─ scripts/                     构建与诊断脚本
├─ tools/                       开发期资源生成工具
├─ go.mod / go.sum              Go 模块依赖
└─ LICENSE                      MIT 许可证
```

## 构建 Windows 程序

需要 Windows、PowerShell 和与 `go.mod` 兼容的 Go 版本。

```powershell
.\scripts\build.ps1
```

产物为 `bin\monitor.exe`。脚本使用项目内的 `.gocache`，并校验生成文件为不会弹出控制台黑框的 Windows GUI 程序。Web 页面已经嵌入 EXE，无需额外复制。

运行测试：

```powershell
$env:GOCACHE = "$PWD\.gocache"
go test ./cmd/monitor
```

## 构建 Android 客户端

需要 JDK 17 或更高版本和 Android SDK 34。仓库已经包含 Gradle Wrapper。

```powershell
cd android-client
.\gradlew.bat assembleDebug
```

调试 APK 位于 `android-client\app\build\outputs\apk\debug\app-debug.apk`。

## 运行配置

| 环境变量 | 说明 |
| --- | --- |
| `MONITOR_PORT` | 修改 HTTP 服务端口，默认 `8765` |
| `MONITOR_LAN=0` | 仅监听本机 `127.0.0.1` |
| `MONITOR_NO_GUI=1` | 不显示 Gio 窗口，并自动允许设备访问 |
| `MONITOR_CPU_ALARM` | 设置 Windows 桌面端 CPU 告警阈值 |
| `MONITOR_DB` | 指定 SQLite 设置数据库路径 |

幻灯片目录保存在 SQLite 数据库 `monitor.db` 中。数据库及其 WAL/SHM 文件、本地构建产物、APK 和开发环境配置均已通过 `.gitignore` 排除。

## 已知限制

- Windows 磁盘 SMART 温度查询通常需要管理员权限；普通权限下可能无法显示磁盘温度。
- Android 客户端允许局域网 HTTP 明文连接，以兼容 Android 5–7 老设备。
- 将 Android 客户端设为 HOME 应用前，请确认设备设置中能够切换回原桌面。

## 许可证

本项目采用 [MIT License](LICENSE)。
