//go:build !windows

package main

import "os/exec"

// hideWindow 非 Windows 平台为空实现（不存在控制台窗口问题）。
func hideWindow(cmd *exec.Cmd) {}
