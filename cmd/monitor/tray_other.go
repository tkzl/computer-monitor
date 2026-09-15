//go:build !windows

package main

// 非 Windows 平台不提供系统托盘：traySupported=false，
// 主循环按旧行为处理——关闭窗口即退出进程。

const traySupported = false

var (
	trayShowCh = make(chan struct{}, 1)
	trayQuitCh = make(chan struct{}, 1)
)

func startTray() {}
