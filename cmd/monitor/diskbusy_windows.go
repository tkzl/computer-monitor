//go:build windows

package main

import (
	"math"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const pdhFormatDouble = 0x00000200

var (
	pdhDLL                      = windows.NewLazySystemDLL("pdh.dll")
	pdhOpenQuery                = pdhDLL.NewProc("PdhOpenQueryW")
	pdhAddEnglishCounter        = pdhDLL.NewProc("PdhAddEnglishCounterW")
	pdhCollectQueryData         = pdhDLL.NewProc("PdhCollectQueryData")
	pdhGetFormattedCounterValue = pdhDLL.NewProc("PdhGetFormattedCounterValue")
	pdhCloseQuery               = pdhDLL.NewProc("PdhCloseQuery")
	diskBusyCounter             pdhPercentCounter
	cpuUsageCounter             pdhPercentCounter
)

type pdhFormattedCounterValue struct {
	status uint32
	_      uint32
	value  float64
}

type pdhPercentCounter struct {
	mu         sync.Mutex
	query      windows.Handle
	counter    windows.Handle
	primed     bool
	retryAfter time.Time
}

func (c *pdhPercentCounter) close() {
	if c.query != 0 {
		pdhCloseQuery.Call(uintptr(c.query))
	}
	c.query = 0
	c.counter = 0
	c.primed = false
}

func (c *pdhPercentCounter) fail(now time.Time) (float64, bool) {
	c.close()
	c.retryAfter = now.Add(30 * time.Second)
	return 0, false
}

func pdhPercent(c *pdhPercentCounter, counterPath string) (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if now.Before(c.retryAfter) {
		return 0, false
	}
	if c.query == 0 {
		if status, _, _ := pdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&c.query))); status != 0 {
			return c.fail(now)
		}
		path, err := windows.UTF16PtrFromString(counterPath)
		if err != nil {
			return c.fail(now)
		}
		if status, _, _ := pdhAddEnglishCounter.Call(uintptr(c.query), uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&c.counter))); status != 0 {
			return c.fail(now)
		}
	}
	if status, _, _ := pdhCollectQueryData.Call(uintptr(c.query)); status != 0 {
		return c.fail(now)
	}
	if !c.primed {
		c.primed = true
		return 0, false
	}
	var formatted pdhFormattedCounterValue
	if status, _, _ := pdhGetFormattedCounterValue.Call(uintptr(c.counter), pdhFormatDouble, 0, uintptr(unsafe.Pointer(&formatted))); status != 0 || formatted.status != 0 {
		return c.fail(now)
	}
	value := formatted.value
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, false
	}
	if value > 100 {
		value = 100
	}
	return value, true
}

// diskBusyPercent 使用 Windows PDH 的英文性能计数器，口径接近任务管理器。
func diskBusyPercent() (float64, bool) {
	return pdhPercent(&diskBusyCounter, `\PhysicalDisk(_Total)\% Disk Time`)
}

// cpuUsagePercent 优先使用会考虑动态频率的 Processor Utility 计数器。
func cpuUsagePercent() (float64, bool) {
	return pdhPercent(&cpuUsageCounter, `\Processor Information(_Total)\% Processor Utility`)
}
