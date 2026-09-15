//go:build windows

package main

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// systray 具有进程级单次初始化状态，使用独立进程测试真实 Windows 消息泵。
func TestTrayMessageLoopSurvivesScheduling(t *testing.T) {
	if os.Getenv("MONITOR_TRAY_TEST_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTrayMessageLoopSurvivesScheduling$", "-test.timeout=45s")
		cmd.Env = append(os.Environ(), "MONITOR_TRAY_TEST_CHILD=1")
		hideWindow(cmd)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tray subprocess: %v\n%s", err, output)
		}
		return
	}
	guiActive.Store(false)
	go startTray()
	deadline := time.Now().Add(5 * time.Second)
	hwnd := systrayWindow()
	for hwnd == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		hwnd = systrayWindow()
	}
	if hwnd == 0 {
		t.Fatal("tray window was not created")
	}
	defer trayPostMessage.Call(uintptr(hwnd), 0x0010, 0, 0) // WM_CLOSE
	for i := 0; i < 100; i++ {
		runtime.GC()
		runtime.Gosched()
		// systray v1.12.2 的通知消息 WM_USER+1，模拟左键松开。
		if ok, _, err := trayPostMessage.Call(uintptr(hwnd), 0x0401, 0, 0x0202); ok == 0 {
			t.Fatalf("post click %d: %v", i, err)
		}
		select {
		case <-trayShowCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("tray stopped processing clicks at iteration %d", i)
		}
	}
}

func drainTrayShowRequests() {
	for {
		select {
		case <-trayShowCh:
		default:
			return
		}
	}
}

func TestTrayRequestShowDoesNotQueueWhileWindowIsVisible(t *testing.T) {
	drainTrayShowRequests()
	guiActive.Store(true)
	defer guiActive.Store(false)

	trayRequestShow()

	select {
	case <-trayShowCh:
		t.Fatal("trayRequestShow queued a stale restore request while the window was visible")
	default:
	}
}

func TestTrayRequestShowQueuesOnceWhileWindowIsHidden(t *testing.T) {
	drainTrayShowRequests()
	guiActive.Store(false)
	defer drainTrayShowRequests()

	trayRequestShow()
	trayRequestShow()

	select {
	case <-trayShowCh:
	default:
		t.Fatal("trayRequestShow did not queue a restore request while the window was hidden")
	}
	select {
	case <-trayShowCh:
		t.Fatal("trayRequestShow queued duplicate restore requests")
	default:
	}
}
