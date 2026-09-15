package main

import (
	"testing"
	"time"
)

func TestCounterRate(t *testing.T) {
	tests := []struct {
		name              string
		current, previous uint64
		seconds, want     float64
	}{
		{name: "normal", current: 300, previous: 100, seconds: 2, want: 100},
		{name: "counter reset", current: 10, previous: 100, seconds: 2, want: 0},
		{name: "zero duration", current: 300, previous: 100, seconds: 0, want: 0},
		{name: "negative duration", current: 300, previous: 100, seconds: -1, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := counterRate(tt.current, tt.previous, tt.seconds); got != tt.want {
				t.Fatalf("counterRate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProcessCPUPercent(t *testing.T) {
	if got := processCPUPercent(10, 12, 2, 4); got != 25 {
		t.Fatalf("processCPUPercent() = %v, want 25", got)
	}
	if got := processCPUPercent(12, 10, 2, 4); got != 0 {
		t.Fatalf("processCPUPercent() after reset = %v, want 0", got)
	}
	if got := processCPUPercent(0, 100, 1, 1); got != 100 {
		t.Fatalf("processCPUPercent() clamp = %v, want 100", got)
	}
}

func TestParseSmartTemp(t *testing.T) {
	data := make([]byte, 2+2*12)
	data[2] = 190
	data[2+5] = 41
	data[14] = 194
	data[14+5] = 38

	got, ok := parseSmartTemp(data)
	if !ok || got != 38 {
		t.Fatalf("parseSmartTemp() = (%v, %v), want (38, true)", got, ok)
	}
}

func TestParseSmartTempRejectsInvalidValue(t *testing.T) {
	data := make([]byte, 14)
	data[2] = 194
	data[2+5] = 120

	if got, ok := parseSmartTemp(data); ok || got != 0 {
		t.Fatalf("parseSmartTemp() = (%v, %v), want (0, false)", got, ok)
	}
}

func TestMergeTemperaturesKeepsLastValidValues(t *testing.T) {
	previous := &TemperatureStats{
		CPUTempAvail: true,
		CPUTemp:      52,
		CPUSource:    "sensor",
		Disks:        []DiskTemperature{{Name: "disk", Temp: 40, Avail: true}},
		UpdatedAt:    123,
	}
	merged := mergeTemperatures(previous, &TemperatureStats{})
	if !merged.CPUTempAvail || merged.CPUTemp != 52 || merged.CPUSource != "sensor" {
		t.Fatalf("CPU last-valid value was not retained: %+v", merged)
	}
	if len(merged.Disks) != 1 || !merged.Disks[0].Avail || merged.Disks[0].Temp != 40 {
		t.Fatalf("disk last-valid value was not retained: %+v", merged.Disks)
	}
	if merged.UpdatedAt != 123 {
		t.Fatalf("UpdatedAt = %v, want 123", merged.UpdatedAt)
	}
}

func TestRequestApprovalRejectsWhenDesktopWindowCannotRestore(t *testing.T) {
	t.Setenv("MONITOR_NO_GUI", "0")
	guiActive.Store(false)
	select {
	case <-trayShowCh:
	default:
	}
	previousTimeout := guiRestoreTimeout
	guiRestoreTimeout = 20 * time.Millisecond
	defer func() { guiRestoreTimeout = previousTimeout }()
	if requestApproval("192.0.2.10") {
		t.Fatal("requestApproval() allowed a remote device when the approval window could not be restored")
	}
	select {
	case <-trayShowCh:
	default:
	}
}

func TestRequestApprovalRestoresTrayWindow(t *testing.T) {
	if !traySupported {
		t.Skip("system tray is not supported on this platform")
	}
	t.Setenv("MONITOR_NO_GUI", "0")
	guiActive.Store(false)
	select {
	case <-trayShowCh:
	default:
	}
	previousTimeout := guiRestoreTimeout
	guiRestoreTimeout = time.Second
	defer func() {
		guiRestoreTimeout = previousTimeout
		guiActive.Store(false)
	}()
	go func() {
		<-trayShowCh
		guiActive.Store(true)
		request := <-accessCh
		request.resp <- true
	}()
	if !requestApproval("192.0.2.11") {
		t.Fatal("requestApproval() did not continue after the tray window was restored")
	}
}

func TestRequestApprovalAllowsExplicitHeadlessMode(t *testing.T) {
	t.Setenv("MONITOR_NO_GUI", "1")
	guiActive.Store(false)
	if !requestApproval("192.0.2.10") {
		t.Fatal("requestApproval() rejected a remote device in explicit headless mode")
	}
}
