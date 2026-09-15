//go:build windows

// 系统托盘：主窗口点关闭后进程缩到托盘继续运行（网页端/平板端不断线），
// 右键托盘图标弹出菜单：「打开监控界面 / 退出」，点「退出」才结束进程。
// 托盘图标按安卓端 ic_launcher 矢量图逐像素复刻（深色圆底+青环+品红弧+中心点），
// 打包成多尺寸 ICO 供不同 DPI 的托盘挑选。

package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"math"
	"os"
	"time"
	"unsafe"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

const traySupported = true

// 托盘与主循环之间的信令（重复请求自动合并）
var (
	trayShowCh = make(chan struct{}, 1) // 请求重新显示窗口
	trayQuitCh = make(chan struct{}, 1) // 请求退出进程
)

var (
	trayUser32                   = windows.NewLazySystemDLL("user32.dll")
	trayCreatePopupMenu          = trayUser32.NewProc("CreatePopupMenu")
	trayAppendMenu               = trayUser32.NewProc("AppendMenuW")
	trayDestroyMenu              = trayUser32.NewProc("DestroyMenu")
	trayFindWindowEx             = trayUser32.NewProc("FindWindowExW")
	trayGetWindowThreadProcessID = trayUser32.NewProc("GetWindowThreadProcessId")
	trayGetCursorPos             = trayUser32.NewProc("GetCursorPos")
	traySetForegroundWindow      = trayUser32.NewProc("SetForegroundWindow")
	trayTrackPopupMenu           = trayUser32.NewProc("TrackPopupMenu")
	trayPostMessage              = trayUser32.NewProc("PostMessageW")
)

type trayPoint struct {
	X int32
	Y int32
}

// startTray 启动托盘图标与菜单。
// 用 go startTray() 调用：即使底层实现阻塞也不影响主线程（主线程留给 gio app.Main）。
func startTray() {
	icon := encodeICO(renderTrayIcon(16), renderTrayIcon(24), renderTrayIcon(32), renderTrayIcon(48))

	systray.Run(func() {
		systray.SetIcon(icon)
		systray.SetTitle("电脑运行监控")
		systray.SetTooltip("电脑运行监控 — 右键菜单可打开界面或退出")

		// fyne/systray v1.12.2 的 Windows 菜单遗漏了 TrackPopupMenu 后必需的
		// WM_NULL。接管点击后使用下方的 Win32 菜单，避免运行一段时间后右键失效。
		systray.SetOnTapped(trayRequestShow)
		systray.SetOnSecondaryTapped(showNativeTrayMenu)
	}, nil)
}

// systrayWindow 查找属于当前进程的 systray 隐藏窗口，避免误取其他应用
// 同名的 SystrayClass 窗口。
func systrayWindow() windows.Handle {
	className, _ := windows.UTF16PtrFromString("SystrayClass")
	var after windows.Handle
	for {
		hwnd, _, _ := trayFindWindowEx.Call(0, uintptr(after), uintptr(unsafe.Pointer(className)), 0)
		if hwnd == 0 {
			return 0
		}
		var pid uint32
		trayGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == uint32(os.Getpid()) {
			return windows.Handle(hwnd)
		}
		after = windows.Handle(hwnd)
	}
}

// showNativeTrayMenu 遵循微软通知区菜单的完整调用顺序：先把隐藏窗口设为
// 前景窗口，菜单结束后再投递 WM_NULL，保证下一次右键仍能可靠弹出。
func showNativeTrayMenu() {
	const (
		mfString       = 0x0000
		mfSeparator    = 0x0800
		tpmRightButton = 0x0002
		tpmBottomAlign = 0x0020
		tpmNoNotify    = 0x0080
		tpmReturnCmd   = 0x0100
		wmNull         = 0x0000
		menuShow       = 1
		menuQuit       = 2
	)
	hwnd := systrayWindow()
	if hwnd == 0 {
		return
	}
	menu, _, _ := trayCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer trayDestroyMenu.Call(menu)
	showText, _ := windows.UTF16PtrFromString("还原窗口")
	quitText, _ := windows.UTF16PtrFromString("退出")
	trayAppendMenu.Call(menu, mfString, menuShow, uintptr(unsafe.Pointer(showText)))
	trayAppendMenu.Call(menu, mfSeparator, 0, 0)
	trayAppendMenu.Call(menu, mfString, menuQuit, uintptr(unsafe.Pointer(quitText)))

	var cursor trayPoint
	if ok, _, _ := trayGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor))); ok == 0 {
		return
	}
	traySetForegroundWindow.Call(uintptr(hwnd))
	command, _, _ := trayTrackPopupMenu.Call(
		menu,
		tpmRightButton|tpmBottomAlign|tpmNoNotify|tpmReturnCmd,
		uintptr(cursor.X),
		uintptr(cursor.Y),
		0,
		uintptr(hwnd),
		0,
	)
	// TrackPopupMenu 文档要求的良性消息：强制任务切换，使后续菜单不失效。
	trayPostMessage.Call(uintptr(hwnd), wmNull, 0, 0)
	switch command {
	case menuShow:
		trayRequestShow()
	case menuQuit:
		trayRequestQuit()
	}
}

// trayRequestShow 通知主循环重建窗口（重复请求合并，不阻塞调用方）。
func trayRequestShow() {
	// 窗口可见时不积压“还原”信号，否则它会在用户稍后关闭窗口时立即重开。
	if guiActive.Load() {
		return
	}
	select {
	case trayShowCh <- struct{}{}:
	default:
	}
}

// trayRequestQuit 退出进程：先移除托盘图标（避免残留幽灵图标），
// 再唤醒可能停在等待处的主循环；500ms 后兜底强退（窗口开着时主循环收不到信号）。
func trayRequestQuit() {
	select {
	case trayQuitCh <- struct{}{}:
	default:
	}
	systray.Quit()
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
}

// ---------- 图标绘制 ----------

// 与安卓 colors.xml 一致：ic_bg #061120 / ic_ring #00E5FF / ic_arc #FF2D95
var (
	trayBG   = [3]float64{0x06 / 255, 0x11 / 255, 0x20 / 255}
	trayRing = [3]float64{0x00 / 255, 0xE5 / 255, 0xFF / 255}
	trayArc  = [3]float64{0xFF / 255, 0x2D / 255, 0x95 / 255}
)

// sampleIcon 按安卓矢量图的 108×108 视口坐标取色：
// 圆底 r54 深色；品红弧为 12 点方向顺时针 90°（圆头线帽，半宽 3，压在最上层）；
// 青色环 |d-30|≤2.5；中心青点 r8。圆外透明。
func sampleIcon(x, y float64) ([3]float64, float64) {
	dx, dy := x-54, y-54
	d := math.Hypot(dx, dy)
	if d > 54 {
		return trayBG, 0
	}
	// 品红弧：角度 ∈ [-90°, 0°]（顶部→右侧）
	ang := math.Atan2(dy, dx)
	var arcDist float64
	if ang >= -math.Pi/2 && ang <= 0 {
		arcDist = math.Abs(d - 30)
	} else {
		// 圆头端帽：到两端点圆心的距离
		d1 := math.Hypot(dx, dy+30) // 端点 (54,24)
		d2 := math.Hypot(dx-30, dy) // 端点 (84,54)
		arcDist = math.Min(d1, d2)
	}
	if arcDist <= 3 {
		return trayArc, 1
	}
	if math.Abs(d-30) <= 2.5 {
		return trayRing, 1
	}
	if d <= 8 {
		return trayRing, 1
	}
	return trayBG, 1
}

// renderTrayIcon 以 4× 超采样抗锯齿渲染 size×size 的 RGBA 图标。
func renderTrayIcon(size int) *image.RGBA {
	const ss = 4
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	scale := float64(size*ss) / 108.0
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					fy := (float64(py*ss+sy) + 0.5) / scale
					fx := (float64(px*ss+sx) + 0.5) / scale
					col, al := sampleIcon(fx, fy)
					r += col[0] * al
					g += col[1] * al
					b += col[2] * al
					a += al
				}
			}
			n := float64(ss * ss)
			alpha := a / n
			var c color.RGBA
			c.A = uint8(alpha * 255)
			if a > 0 {
				c.R = uint8(r / a * 255)
				c.G = uint8(g / a * 255)
				c.B = uint8(b / a * 255)
			}
			img.SetRGBA(px, py, c)
		}
	}
	return img
}

// ---------- ICO 编码 ----------

// encodeICO 把多张 RGBA 图打包成标准 ICO（32 位 BGRA DIB 条目，全透明 AND 掩码）。
// 条目按传入顺序（小→大）排列。
func encodeICO(imgs ...*image.RGBA) []byte {
	type entry struct {
		w, h int
		data []byte
	}
	list := make([]entry, 0, len(imgs))
	for _, im := range imgs {
		w := im.Bounds().Dx()
		h := im.Bounds().Dy()
		pix := make([]byte, w*h*4) // BGRA，自底向上
		i := 0
		for y := h - 1; y >= 0; y-- {
			for x := 0; x < w; x++ {
				c := im.RGBAAt(x, y)
				pix[i] = c.B
				pix[i+1] = c.G
				pix[i+2] = c.R
				pix[i+3] = c.A
				i += 4
			}
		}
		maskStride := ((w + 31) / 32) * 4
		mask := make([]byte, maskStride*h) // 全 0：按 alpha 混合

		var dib bytes.Buffer
		binary.Write(&dib, binary.LittleEndian, int32(40))
		binary.Write(&dib, binary.LittleEndian, int32(w))
		binary.Write(&dib, binary.LittleEndian, int32(h*2)) // 高×2（XOR+AND）
		binary.Write(&dib, binary.LittleEndian, uint16(1))
		binary.Write(&dib, binary.LittleEndian, uint16(32))
		binary.Write(&dib, binary.LittleEndian, uint32(0)) // BI_RGB
		binary.Write(&dib, binary.LittleEndian, uint32(len(pix)+len(mask)))
		binary.Write(&dib, binary.LittleEndian, uint32(0))
		binary.Write(&dib, binary.LittleEndian, uint32(0))
		binary.Write(&dib, binary.LittleEndian, uint32(0))
		binary.Write(&dib, binary.LittleEndian, uint32(0))
		dib.Write(pix)
		dib.Write(mask)
		list = append(list, entry{w: w, h: h, data: dib.Bytes()})
	}

	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint16(0))         // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))         // type = icon
	binary.Write(&buf, binary.LittleEndian, uint16(len(list))) // 条目数
	offset := 6 + 16*len(list)
	for _, e := range list {
		wb, hb := byte(e.w), byte(e.h)
		if e.w >= 256 {
			wb = 0
		}
		if e.h >= 256 {
			hb = 0
		}
		buf.WriteByte(wb)
		buf.WriteByte(hb)
		buf.WriteByte(0) // 调色板颜色数
		buf.WriteByte(0) // 保留
		binary.Write(&buf, binary.LittleEndian, uint16(1))
		binary.Write(&buf, binary.LittleEndian, uint16(32))
		binary.Write(&buf, binary.LittleEndian, uint32(len(e.data)))
		binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(e.data)
	}
	for _, e := range list {
		buf.Write(e.data)
	}
	return buf.Bytes()
}
