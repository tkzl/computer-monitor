# 时间框反弹测试

先在 `android-client` 目录运行 `gradlew.bat assembleDebug`，然后使用构建所用的 JDK 和 Gradle 缓存中的 Kotlin 运行库执行：

```powershell
$javaBin = Join-Path $env:JAVA_HOME 'bin'
$stdlib = Get-ChildItem "$env:USERPROFILE/.gradle/caches/modules-2/files-2.1/org.jetbrains.kotlin/kotlin-stdlib/2.1.0" -Recurse -Filter '*.jar' | Select-Object -First 1 -ExpandProperty FullName
& "$javaBin/javac.exe" -cp app/build/tmp/kotlin-classes/debug -d build/bounce-tests tests/BouncingAxisCheck.java
& "$javaBin/java.exe" -cp "build/bounce-tests;app/build/tmp/kotlin-classes/debug;$stdlib" com.qianwen.monitor.BouncingAxisCheck
```

覆盖左右/上下反弹、恰好碰边、一次跨越多个边界、可用区域缩小、零宽区域，以及 100000 帧移动不越界。无额外测试依赖。
