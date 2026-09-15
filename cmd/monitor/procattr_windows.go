//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideWindow 让子进程不创建控制台窗口。
// monitor.exe 以 -H=windowsgui 构建后自身没有控制台，
// 再拉起 powershell / nvidia-smi 这类控制台程序时，
// Windows 会为其新建一个黑色窗口并随命令结束关闭（表现为反复闪烁）；
// CREATE_NO_WINDOW 标志可彻底隐藏该窗口。
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
}
