//go:build !windows

package main

func diskBusyPercent() (float64, bool) {
	return 0, false
}

func cpuUsagePercent() (float64, bool) {
	return 0, false
}
