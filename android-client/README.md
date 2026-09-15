# 电脑监控 · 安卓客户端

监控面板的手机端：原生 Kotlin 应用，用 WebView 加载电脑上监控程序（monitor.exe）的科幻仪表盘，实时查看 CPU/内存/磁盘/网络/温度与进程排行。

## 兼容性

- 支持 **Android 5.0（API 21）到最新版**，零第三方依赖
- 当前安装包为 debug 版（约 826KB）

## 使用步骤

1. **电脑端**：在项目目录用局域网模式启动监控程序
   ```powershell
   $env:MONITOR_LAN="1"; .\monitor.exe
   ```
   启动后终端会显示「手机/局域网设备访问：http://192.168.x.x:8765/」

2. **手机端**：安装 `app-debug.apk`（首次安装需允许"未知来源"应用）

3. **连接**：打开应用，输入终端显示的地址（手机与电脑需在同一 Wi-Fi）

4. 右上角齿轮按钮可随时更换地址；连接失败时会提示重试

## 重新构建（可选）

本机已配置好 Android Studio SDK 与 Gradle，修改代码后可直接重新编译：

```powershell
cd android-client
.\gradlew.bat assembleDebug
```

产物位置：`app\build\outputs\apk\debug\app-debug.apk`
