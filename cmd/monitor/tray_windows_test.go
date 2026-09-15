//go:build windows

package main

import "testing"

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
