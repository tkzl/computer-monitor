# 电脑运行监控

## 目录结构

```text
.
├─ android-client/              Android 原生客户端
├─ assets/                      可编辑的源资源（图标等）
├─ bin/                         Windows 运行产物与 SQLite 数据库
├─ cmd/monitor/                 Go 服务端主程序
│  ├─ web/dashboard.html        嵌入 EXE 的 Web 监控页
│  ├─ main.go / gui.go          HTTP 服务与 Gio 桌面界面
│  └─ *_test.go                 服务端测试
├─ scripts/                     构建与诊断脚本
│  ├─ build.ps1                 Windows 无控制台构建
│  └─ check_temp.ps1            温度传感器诊断
├─ tools/                       开发期生成工具
│  └─ icon_generator.go         Windows ICO 生成器
├─ go.mod / go.sum              Go 模块依赖
└─ README.md / AGENTS.md        项目说明与长期维护记录
```

## 构建

```powershell
.\scripts\build.ps1
```

产物为 `bin/monitor.exe`。脚本会使用项目内 `.gocache`，并验证产物是不会弹出控制台黑框的 Windows GUI 程序。Web 页面已嵌入 EXE，无需额外复制 `dashboard.html`。

## 幻灯片目录设置

服务端窗口右上角提供“幻灯片目录设置”。多个目录按一行一个路径填写，保存到 `bin/monitor.db` 的 `slideshow_directories` 表中；数据库及其 WAL/SHM 文件不会提交到 Git。

当前只保存目录设置，图片扫描、服务端接口和 Android 幻灯片播放将在后续功能中实现。

## 许可证

本项目采用 [MIT License](LICENSE)。
