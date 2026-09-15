// 电脑运行监控 —— Gio 桌面窗口
//
// monitor.exe 启动后打开原生窗口，内容与手机 APK 一致：
// 秒级时钟 + 公历 + 农历、CPU/内存（每核竖条）、网络/磁盘/温度、进程排行（含端口）。
// 同时承载「设备接入审批」：局域网新设备首次连接数据接口时，
// 窗口右下角弹出允许/拒绝确认卡，30 秒未处理自动拒绝。
//
// 设 MONITOR_NO_GUI=1 关闭窗口与审批 UI（此时设备接入自动放行并打印日志）。
package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ---------- 配色（与 APK HUD 主题一致） ----------

var (
	colBG        = color.NRGBA{R: 0x04, G: 0x07, B: 0x0F, A: 0xFF}
	colPanel     = color.NRGBA{R: 0x10, G: 0x1E, B: 0x2E, A: 0xFF}
	colLine      = color.NRGBA{R: 0x2A, G: 0x5C, B: 0x7A, A: 0xFF}
	colText      = color.NRGBA{R: 0xE6, G: 0xEB, B: 0xF5, A: 0xFF}
	colMuted     = color.NRGBA{R: 0x8F, G: 0xB7, B: 0xCC, A: 0xFF}
	colAccent    = color.NRGBA{R: 0x00, G: 0xE5, B: 0xFF, A: 0xFF}
	colGreen     = color.NRGBA{R: 0x37, G: 0xFF, B: 0xB4, A: 0xFF}
	colYellow    = color.NRGBA{R: 0xFF, G: 0xC8, B: 0x57, A: 0xFF}
	colRed       = color.NRGBA{R: 0xFF, G: 0x4D, B: 0x5E, A: 0xFF}
	colMagenta   = color.NRGBA{R: 0xFF, G: 0x2D, B: 0x95, A: 0xFF}
	colTrack     = color.NRGBA{R: 0x0D, G: 0x1A, B: 0x28, A: 0xFF}
	colAlarmGlow = [...]color.NRGBA{
		{R: 0xFF, G: 0x4D, B: 0x5E, A: 0x90},
		{R: 0xFF, G: 0x4D, B: 0x5E, A: 0x68},
		{R: 0xFF, G: 0x4D, B: 0x5E, A: 0x48},
		{R: 0xFF, G: 0x4D, B: 0x5E, A: 0x30},
		{R: 0xFF, G: 0x4D, B: 0x5E, A: 0x20},
		{R: 0xFF, G: 0x4D, B: 0x5E, A: 0x12},
	}
	colBtnOff    = color.NRGBA{R: 0x1C, G: 0x2B, B: 0x3A, A: 0xFF}
	colHover     = color.NRGBA{R: 0x00, G: 0xE5, B: 0xFF, A: 0x26} // 表头悬停：青色 15%
	colHoverStr  = color.NRGBA{R: 0x00, G: 0xE5, B: 0xFF, A: 0x4D} // 表头按压：青色 30%
	colModalMask = color.NRGBA{R: 0x00, G: 0x03, B: 0x08, A: 0x8A} // 审批弹窗：全窗半透明遮罩
)

// ---------- 设备接入审批（HTTP 协程 ↔ UI 线程） ----------

var (
	guiActive         = atomic.Bool{} // gio 窗口是否接管审批 UI
	accessCh          = make(chan accessReq, 8)
	guiRestoreTimeout = 8 * time.Second // 托盘状态下等待窗口重建的最长时间
)

type accessReq struct {
	ip      string
	resp    chan bool
	created time.Time
}

// requestAccess 向窗口 UI 发起一次设备接入审批，阻塞等待用户决定。
// UI 正常情况下会在 30 秒内自动拒绝；这里再设 45 秒兜底，避免窗口在
// 请求已入队后关闭，导致 HTTP 协程永久阻塞并一直占用 accessPrompt。
func requestAccess(ip string) bool {
	r := accessReq{ip: ip, resp: make(chan bool, 1), created: time.Now()}
	timer := time.NewTimer(45 * time.Second)
	windowCheck := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	defer windowCheck.Stop()
	select {
	case accessCh <- r:
		for {
			select {
			case ok := <-r.resp:
				return ok
			case <-windowCheck.C:
				// 请求可能恰好撞上窗口销毁；持续请求托盘主循环重建窗口，
				// 未处理请求会由旧窗口转交给新窗口。
				if traySupported && !guiActive.Load() {
					trayRequestShow()
				}
			case <-timer.C:
				return false
			}
		}
	case <-timer.C: // UI 卡死或队列已满
		return false
	}
}

// restoreGUIForApproval 在窗口缩到托盘后请求重建窗口，并等待它接管审批 UI。
func restoreGUIForApproval() bool {
	if guiActive.Load() {
		return true
	}
	if !traySupported {
		return false
	}
	trayRequestShow()
	timer := time.NewTimer(guiRestoreTimeout)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if guiActive.Load() {
				return true
			}
		case <-timer.C:
			return false
		}
	}
}

// requestApproval 设备准入总入口。
// 只有显式无界面模式才自动放行；正常桌面模式下窗口缩到托盘时，
// 新连接会唤回窗口并继续显示右下角审批卡。
func requestApproval(ip string) bool {
	if os.Getenv("MONITOR_NO_GUI") == "1" {
		fmt.Printf("设备 %s 接入监控（无界面模式自动放行）\n", ip)
		return true
	}
	if restoreGUIForApproval() {
		return requestAccess(ip)
	}
	fmt.Printf("已拒绝设备 %s：审批窗口未能恢复\n", ip)
	return false
}

// ---------- 数字格式化 ----------

func fmtB(v uint64) string {
	switch {
	case v >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(v)/(1<<30))
	case v >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(v)/(1<<20))
	case v >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(v)/(1<<10))
	default:
		return fmt.Sprintf("%d B", v)
	}
}

func fmtRate(bps float64) string { return fmtB(uint64(bps)) + "/s" }

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------- UI 状态 ----------

type guiState struct {
	th               *material.Theme
	procView         int // 0=合并视图 1=单进程视图
	sortKey          int // 0=CPU 1=内存% 2=内存占用
	viewBtns         [2]widget.Clickable
	sortBtns         [3]widget.Clickable
	pageList         widget.List // 整页滚动
	procScroll       widget.List // 进程区滚动
	lastDay          string
	dateStr          string
	lunarStr         string
	pending          []accessReq // 待审批设备队列（一次展示队首）
	approveBt        widget.Clickable
	denyBt           widget.Clickable
	settingsBt       widget.Clickable
	settingsScanBt   widget.Clickable
	settingsSaveBt   widget.Clickable
	settingsCancelBt widget.Clickable
	dirsEditor       widget.Editor
	settingsOpen     bool
	settingsScanning bool
	settingsStatus   string
	directoryScanCh  chan directoryScanResult
	alarmPct         float64 // CPU 告警阈值（默认与 APK 一致 20）
}

type directoryScanResult struct {
	directories []string
	rootCount   int
}

func newGUIState() *guiState {
	th := material.NewTheme()
	g := &guiState{th: th, alarmPct: 20, directoryScanCh: make(chan directoryScanResult, 1)}
	g.dirsEditor.SingleLine = false
	// widget.List 零值轴向是 Horizontal，必须显式设为竖向，
	// 否则会给子内容无界宽度约束，导致横向卡片与表格列塌陷。
	g.pageList.Axis = layout.Vertical
	g.procScroll.Axis = layout.Vertical
	if v := os.Getenv("MONITOR_CPU_ALARM"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 100 {
			g.alarmPct = f
		}
	}
	return g
}

func (g *guiState) label(size unit.Sp, txt string, c color.NRGBA) material.LabelStyle {
	l := material.Label(g.th, size, txt)
	l.Color = c
	return l
}

func (g *guiState) bold(size unit.Sp, txt string, c color.NRGBA) material.LabelStyle {
	l := g.label(size, txt, c)
	l.Font.Weight = 700
	return l
}

// ---------- 装饰绘制 ----------

// paintPanelBg 绘制 HUD 卡片底：面板色 + 顶部扫描线 + 四角霓虹括号（+ 告警红框）。
func paintPanelBg(ops *op.Ops, bg image.Rectangle, alarm bool) {
	paint.FillShape(ops, colPanel, clip.RRect{Rect: bg, SE: 8, SW: 8, NW: 8, NE: 8}.Op(ops))
	const l, t = 16, 1
	for _, r := range cornerRects(bg, l, t) {
		paint.FillShape(ops, colAccent, clip.Rect(r).Op())
	}
	if alarm {
		drawAlarmFrame(ops, bg)
	}
}

// panel 画一张高度贴合内容的 HUD 卡片。
func panel(gtx layout.Context, alarm bool, content layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := layout.UniformInset(unit.Dp(14)).Layout(gtx, content)
	call := macro.Stop()
	paintPanelBg(gtx.Ops, image.Rectangle{Max: dims.Size}, alarm)
	call.Add(gtx.Ops)
	return dims
}

// panelH 画一张指定尺寸的 HUD 卡片（约束须为精确宽高，由 rowPanels 给定）；
// 内容贴顶摆放，卡片底色铺满整个高度。
func panelH(gtx layout.Context, alarm bool, content layout.Widget) layout.Dimensions {
	size := gtx.Constraints.Min
	paintPanelBg(gtx.Ops, image.Rectangle{Max: size}, alarm)
	pad := gtx.Dp(unit.Dp(14))
	cgtx := gtx
	cgtx.Constraints.Min = image.Point{}
	cgtx.Constraints.Max.X = size.X - 2*pad
	cgtx.Constraints.Max.Y = size.Y - 2*pad
	defer op.Offset(image.Pt(pad, pad)).Push(gtx.Ops).Pop()
	content(cgtx)
	return layout.Dimensions{Size: size}
}

// rowPanels 一行多卡片并排且等高：按权重分配宽度并预测量各卡内容高度，
// 取最高者统一行高（对应 Android 布局里的 match_parent 并排卡片）。
func rowPanels(gtx layout.Context, weights []float32, alarms []bool, bodies []layout.Widget) layout.Dimensions {
	pad := gtx.Dp(unit.Dp(14))
	gap := gtx.Dp(unit.Dp(12))
	n := len(weights)
	avail := gtx.Constraints.Max.X - gap*(n-1)
	sum := float32(0)
	for _, w := range weights {
		sum += w
	}
	widths := make([]int, n)
	used := 0
	for i, w := range weights {
		if i == n-1 {
			widths[i] = avail - used // 末卡吸收取整误差，保证总宽精确
		} else {
			widths[i] = int(float32(avail) * w / sum)
			used += widths[i]
		}
	}
	// 测量遍：各卡在目标内宽下的内容高度（用独立 Ops，不影响绘制队列）
	maxH := 0
	for i := 0; i < n; i++ {
		var scratch op.Ops
		mgtx := gtx
		mgtx.Ops = &scratch
		mgtx.Constraints = layout.Constraints{
			Max: image.Pt(widths[i]-2*pad, gtx.Constraints.Max.Y),
		}
		d := bodies[i](mgtx)
		if d.Size.Y > maxH {
			maxH = d.Size.Y
		}
	}
	cardH := maxH + 2*pad
	children := make([]layout.FlexChild, 0, n*2)
	for i := 0; i < n; i++ {
		i := i
		if i > 0 {
			children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout))
		}
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Constraints{
				Min: image.Pt(widths[i], cardH),
				Max: image.Pt(widths[i], cardH),
			}
			return panelH(gtx, alarms[i], bodies[i])
		}))
	}
	return layout.Flex{}.Layout(gtx, children...)
}

func cornerRects(bg image.Rectangle, l, t int) []image.Rectangle {
	mx, my := bg.Dx(), bg.Dy()
	return []image.Rectangle{
		image.Rect(0, 0, l, t),
		image.Rect(0, 0, t, l),
		image.Rect(mx-l, my-t, mx, my),
		image.Rect(mx-t, my-l, mx, my),
	}
}

// drawAlarmFrame CPU 告警红框：主边框向内紧邻六层透明度渐变，
// 各层无空隙，形成连续柔和的内侧辉光；亮灭按墙钟 500ms 交替。
func drawAlarmFrame(ops *op.Ops, bg image.Rectangle) {
	if time.Now().UnixMilli()/500%2 == 0 {
		return // 灭相位
	}
	ring(ops, bg.Inset(1), 2, colRed)
	for i, glow := range colAlarmGlow {
		ring(ops, bg.Inset(3+i), 1, glow)
	}
}

func ring(ops *op.Ops, r image.Rectangle, thick int, c color.NRGBA) {
	paint.FillShape(ops, c, clip.Rect(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+thick)).Op())
	paint.FillShape(ops, c, clip.Rect(image.Rect(r.Min.X, r.Max.Y-thick, r.Max.X, r.Max.Y)).Op())
	paint.FillShape(ops, c, clip.Rect(image.Rect(r.Min.X, r.Min.Y, r.Min.X+thick, r.Max.Y)).Op())
	paint.FillShape(ops, c, clip.Rect(image.Rect(r.Max.X-thick, r.Min.Y, r.Max.X, r.Max.Y)).Op())
}

// ---------- 时钟与农历 ----------

func (g *guiState) layoutClock(gtx layout.Context) layout.Dimensions {
	now := time.Now()
	if dkey := fmt.Sprintf("%04d-%02d-%02d", now.Year(), int(now.Month()), now.Day()); dkey != g.lastDay {
		g.lastDay = dkey
		g.dateStr = fmt.Sprintf("%d年%02d月%02d日 %s", now.Year(), int(now.Month()), now.Day(), weekdayCN(now.Weekday()))
		if lu := solarToLunarGo(now.Year(), int(now.Month()), now.Day()); lu != "" {
			g.lunarStr = lu
		} else {
			g.lunarStr = "农历数据超出 1900–2100 范围"
		}
	}
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(g.bold(56, now.Format("15:04:05"), colText).Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
		layout.Rigid(g.label(15, g.dateStr, colAccent).Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(2)}.Layout),
		layout.Rigid(g.label(14, g.lunarStr, colYellow).Layout),
	)
}

func weekdayCN(d time.Weekday) string {
	return [...]string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}[d]
}

// ---------- 农历（与 APK/网页同一算法，数据表覆盖 1900–2100） ----------

var lunarInfoGo = []int{
	0x04bd8, 0x04ae0, 0x0a570, 0x054d5, 0x0d260, 0x0d950, 0x16554, 0x056a0, 0x09ad0, 0x055d2,
	0x04ae0, 0x0a5b6, 0x0a4d0, 0x0d250, 0x1d255, 0x0b540, 0x0d6a0, 0x0ada2, 0x095b0, 0x14977,
	0x04970, 0x0a4b0, 0x0b4b5, 0x06a50, 0x06d40, 0x1ab54, 0x02b60, 0x09570, 0x052f2, 0x04970,
	0x06566, 0x0d4a0, 0x0ea50, 0x06e95, 0x05ad0, 0x02b60, 0x186e3, 0x092e0, 0x1c8d7, 0x0c950,
	0x0d4a0, 0x1d8a6, 0x0b550, 0x056a0, 0x1a5b4, 0x025d0, 0x092d0, 0x0d2b2, 0x0a950, 0x0b557,
	0x06ca0, 0x0b550, 0x15355, 0x04da0, 0x0a5b0, 0x14573, 0x052b0, 0x0a9a8, 0x0e950, 0x06aa0,
	0x0aea6, 0x0ab50, 0x04b60, 0x0aae4, 0x0a570, 0x05260, 0x0f263, 0x0d950, 0x05b57, 0x056a0,
	0x096d0, 0x04dd5, 0x04ad0, 0x0a4d0, 0x0d4d4, 0x0d250, 0x0d558, 0x0b540, 0x0b6a0, 0x195a6,
	0x095b0, 0x049b0, 0x0a974, 0x0a4b0, 0x0b27a, 0x06a50, 0x06d40, 0x0af46, 0x0ab60, 0x09570,
	0x04af5, 0x04970, 0x064b0, 0x074a3, 0x0ea50, 0x06b58, 0x055c0, 0x0ab60, 0x096d5, 0x092e0,
	0x0c960, 0x0d954, 0x0d4a0, 0x0da50, 0x07552, 0x056a0, 0x0abb7, 0x025d0, 0x092d0, 0x0cab5,
	0x0a950, 0x0b4a0, 0x0baa4, 0x0ad50, 0x055d9, 0x04ba0, 0x0a5b0, 0x15176, 0x052b0, 0x0a930,
	0x07954, 0x06aa0, 0x0ad50, 0x05b52, 0x04b60, 0x0a6e6, 0x0a4e0, 0x0d260, 0x0ea65, 0x0d530,
	0x05aa0, 0x076a3, 0x096d0, 0x04afb, 0x04ad0, 0x0a4d0, 0x1d0b6, 0x0d250, 0x0d520, 0x0dd45,
	0x0b5a0, 0x056d0, 0x055b2, 0x049b0, 0x0a577, 0x0a4b0, 0x0aa50, 0x1b255, 0x06d20, 0x0ada0,
	0x14b63, 0x09370, 0x049f8, 0x04970, 0x064b0, 0x168a6, 0x0ea50, 0x06b20, 0x1a6c4, 0x0aae0,
	0x0a2e0, 0x0d2e3, 0x0c960, 0x0d557, 0x0d4a0, 0x0da50, 0x05d55, 0x056a0, 0x0a6d0, 0x055d4,
	0x052d0, 0x0a9b8, 0x0a950, 0x0b4a0, 0x0b6a6, 0x0ad50, 0x055a0, 0x0aba4, 0x0a5b0, 0x052b0,
	0x0b273, 0x06930, 0x07337, 0x06aa0, 0x0ad50, 0x14b55, 0x04b60, 0x0a570, 0x054e4, 0x0d160,
	0x0e968, 0x0d520, 0x0daa0, 0x16aa6, 0x056d0, 0x04ae0, 0x0a9d4, 0x0a2d0, 0x0d150, 0x0f252,
	0x0d520,
}

const (
	ganStr   = "甲乙丙丁戊己庚辛壬癸"
	zhiStr   = "子丑寅卯辰巳午未申酉戌亥"
	animaStr = "鼠牛虎兔龙蛇马羊猴鸡狗猪"
	ns1      = "日一二三四五六七八九十"
	ns2      = "初十廿卅"
	ns3      = "正二三四五六七八九十冬腊"
)

func dayNum(y, m, d int) int64 {
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC).Unix() / 86400
}

func lunarLeapMonth(y int) int { return lunarInfoGo[y-1900] & 0xf }

func lunarLeapDays(y int) int {
	if lunarLeapMonth(y) == 0 {
		return 0
	}
	if lunarInfoGo[y-1900]&0x10000 != 0 {
		return 30
	}
	return 29
}

func lunarMonthDays(y, m int) int {
	if lunarInfoGo[y-1900]&(0x10000>>m) != 0 {
		return 30
	}
	return 29
}

func lunarYearDays(y int) int {
	sum := 348
	for i := 0x8000; i > 0x8; i >>= 1 {
		if lunarInfoGo[y-1900]&i != 0 {
			sum++
		}
	}
	return sum + lunarLeapDays(y)
}

func lunarDayName(d int) string {
	switch d {
	case 10:
		return "初十"
	case 20:
		return "二十"
	case 30:
		return "三十"
	}
	r2 := []rune(ns2)
	r1 := []rune(ns1)
	return string(r2[d/10]) + string(r1[d%10])
}

// solarToLunarGo 公历转农历，返回 "丙午·马年 七月廿八"；超出范围返回空串。
func solarToLunarGo(sy, sm, sd int) string {
	if sy < 1900 || sy > 2100 {
		return ""
	}
	offset := int(dayNum(sy, sm, sd) - dayNum(1900, 1, 31))
	ly, temp := 1900, 0
	for ly < 2101 && offset > 0 {
		temp = lunarYearDays(ly)
		offset -= temp
		ly++
	}
	if offset < 0 {
		offset += temp
		ly--
	}
	leap := lunarLeapMonth(ly)
	isLeap := false
	lm := 1
	for lm < 13 && offset > 0 {
		if leap > 0 && lm == leap+1 && !isLeap {
			lm--
			isLeap = true
			temp = lunarLeapDays(ly)
		} else {
			temp = lunarMonthDays(ly, lm)
		}
		if isLeap && lm == leap+1 {
			isLeap = false
		}
		offset -= temp
		lm++
	}
	if offset == 0 && leap > 0 && lm == leap+1 {
		if isLeap {
			isLeap = false
		} else {
			isLeap = true
			lm--
		}
	}
	if offset < 0 {
		offset += temp
		lm--
	}
	g := (ly - 3) % 10
	if g == 0 {
		g = 10
	}
	z := (ly - 3) % 12
	if z == 0 {
		z = 12
	}
	gz := string([]rune(ganStr)[g-1]) + string([]rune(zhiStr)[z-1])
	animal := string([]rune(animaStr)[(ly-4)%12])
	leapPre := ""
	if isLeap {
		leapPre = "闰"
	}
	return gz + "·" + animal + "年  " + leapPre + string([]rune(ns3)[lm-1]) + "月" + lunarDayName(offset+1)
}

// ---------- 颜色档位 ----------

func cpuPillColor(p float64) color.NRGBA {
	switch {
	case p >= 50:
		return colRed
	case p >= 20:
		return colYellow
	default:
		return colGreen
	}
}

func coreBarColor(p float64) color.NRGBA {
	switch {
	case p >= 90:
		return colRed
	case p >= 70:
		return colYellow
	default:
		return colAccent
	}
}

func tempColor(t float64) color.NRGBA {
	switch {
	case t <= 0:
		return colMuted
	case t < 55:
		return colAccent
	case t < 75:
		return colYellow
	default:
		return colRed
	}
}

// ---------- 小部件 ----------

// fixH 限制高度为固定 dp（宽度沿用当前约束）。
func fixH(gtx layout.Context, h unit.Dp, content layout.Widget) layout.Dimensions {
	hpx := gtx.Dp(h)
	gtx.Constraints.Min.Y = hpx
	gtx.Constraints.Max.Y = hpx
	return content(gtx)
}

// hbar 水平进度条：8dp 高、底槽 + 按百分比填充。
func hbar(gtx layout.Context, pct float64, fillC color.NRGBA) layout.Dimensions {
	return fixH(gtx, unit.Dp(8), func(gtx layout.Context) layout.Dimensions {
		r := gtx.Constraints.Max
		track := image.Rect(0, 0, r.X, r.Y)
		paint.FillShape(gtx.Ops, colTrack, clip.RRect{Rect: track, SE: 2, SW: 2, NW: 2, NE: 2}.Op(gtx.Ops))
		w := int(float64(r.X) * clampF(pct, 0, 100) / 100)
		if w > 0 {
			bar := image.Rect(0, 0, w, r.Y)
			paint.FillShape(gtx.Ops, fillC, clip.RRect{Rect: bar, SE: 2, SW: 2, NW: 2, NE: 2}.Op(gtx.Ops))
		}
		return layout.Dimensions{Size: r}
	})
}

// coresRow 每核使用率竖条（对齐网页端 .core）。
func (g *guiState) coresRow(gtx layout.Context, perCore []float64) layout.Dimensions {
	if len(perCore) == 0 {
		return layout.Dimensions{}
	}
	gap := gtx.Dp(unit.Dp(3))
	cw := (gtx.Constraints.Max.X - gap*(len(perCore)-1)) / len(perCore)
	return fixH(gtx, unit.Dp(26), func(gtx layout.Context) layout.Dimensions {
		r := gtx.Constraints.Max
		for i, p := range perCore {
			x := i * (cw + gap)
			cell := image.Rect(x, 0, x+cw, r.Y)
			paint.FillShape(gtx.Ops, colTrack, clip.Rect(cell).Op())
			h := int(float64(r.Y) * clampF(p, 0, 100) / 100)
			if p > 0.5 && h < 2 {
				h = 2
			}
			if h > 0 {
				fill := image.Rect(cell.Min.X, cell.Max.Y-h, cell.Max.X, cell.Max.Y)
				paint.FillShape(gtx.Ops, coreBarColor(p), clip.Rect(fill).Op())
			}
		}
		return layout.Dimensions{Size: r}
	})
}

// ---------- 区块：CPU / 内存 ----------

func (g *guiState) topRow(gtx layout.Context, s *Stats) layout.Dimensions {
	alarm := s.CPU.Percent >= g.alarmPct
	cpuBody := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(g.label(13, "CPU 使用率", colAccent).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			layout.Rigid(g.bold(32, fmt.Sprintf("%.1f%%", s.CPU.Percent), colText).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return hbar(gtx, s.CPU.Percent, coreBarColor(s.CPU.Percent))
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return g.coresRow(gtx, s.CPU.PerCore) }),
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			layout.Rigid(g.label(12, fmt.Sprintf("%d 核心", s.CPU.Cores), colMuted).Layout),
		)
	}
	memBody := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(g.label(13, "MEMORY 内存", colAccent).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			layout.Rigid(g.bold(32, fmt.Sprintf("%.1f%%", s.Memory.Percent), colText).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return hbar(gtx, s.Memory.Percent, coreBarColor(s.Memory.Percent))
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(g.label(12, fmt.Sprintf("已用 %s / 共 %s", fmtB(s.Memory.Used), fmtB(s.Memory.Total)), colMuted).Layout),
		)
	}
	return rowPanels(gtx, []float32{1, 1}, []bool{alarm, false}, []layout.Widget{cpuBody, memBody})
}

// ---------- 区块：网络+温度 / 磁盘 / GPU ----------

func (g *guiState) netRow(gtx layout.Context, s *Stats) layout.Dimensions {
	netBody := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(g.label(13, "NETWORK 网络速率", colAccent).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(g.label(12, "▼ 下载", colGreen).Layout),
			layout.Rigid(g.bold(17, fmtRate(s.Network.RecvRate), colText).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(g.label(12, "▲ 上传", colMagenta).Layout),
			layout.Rigid(g.bold(17, fmtRate(s.Network.SentRate), colText).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(g.label(10, s.Network.Interface, colMuted).Layout),
			layout.Rigid(g.label(11, fmt.Sprintf("累计 ↓%s ↑%s", fmtB(s.Network.BytesRecv), fmtB(s.Network.BytesSent)), colMuted).Layout),
		)
	}
	diskBody := func(gtx layout.Context) layout.Dimensions {
		children := make([]layout.FlexChild, 0, len(s.Disks)*3+3)
		busy := "N/A"
		if s.DiskIO.BusyAvail {
			busy = fmt.Sprintf("%.1f%%", s.DiskIO.BusyPercent)
		}
		children = append(children,
			layout.Rigid(g.label(13, "DISK 磁盘用量", colAccent).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(5)}.Layout),
			layout.Rigid(g.label(10, fmt.Sprintf("实时 读 %s  写 %s  忙碌 %s", fmtRate(s.DiskIO.ReadRate), fmtRate(s.DiskIO.WriteRate), busy), colMuted).Layout),
		)
		for _, d := range s.Disks {
			d := d
			children = append(children,
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(g.label(12, fmt.Sprintf("%s  %.1f%%", d.Mount, d.Percent), colText).Layout),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return hbar(gtx, d.Percent, coreBarColor(d.Percent))
						}),
						layout.Rigid(g.label(10, fmt.Sprintf("%s / %s", fmtB(d.Used), fmtB(d.Total)), colMuted).Layout),
					)
				}),
			)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}
	t := s.Temperature
	tempBody := func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			layout.Rigid(g.label(13, "TEMP 温度", colAccent).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(g.label(12, "CPU", colMuted).Layout),
			layout.Rigid(g.bold(17, tempText(t.CPUTemp, t.CPUTempAvail), tempColor(t.CPUTemp)).Layout),
		}
		for _, d := range t.Disks {
			d := d
			children = append(children,
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(g.label(11, fmt.Sprintf("%s  %s", d.Name, tempText(d.Temp, d.Avail)), colMuted).Layout),
			)
		}
		if len(t.Disks) == 0 {
			children = append(children,
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(g.label(10, "磁盘温度需以管理员身份运行", colMuted).Layout),
			)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}
	netTempBody := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(netBody),
			layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
			layout.Rigid(tempBody),
		)
	}
	gpuBody := func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			layout.Rigid(g.label(13, "GPU", colAccent).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			layout.Rigid(g.label(10, t.GPUName, colMuted).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(g.label(12, "使用率", colMuted).Layout),
		}
		if t.GPUUtilAvail {
			children = append(children,
				layout.Rigid(g.bold(20, fmt.Sprintf("%.0f%%", t.GPUUtil), colText).Layout),
				layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return hbar(gtx, t.GPUUtil, coreBarColor(t.GPUUtil))
				}),
			)
		} else {
			children = append(children, layout.Rigid(g.bold(20, "N/A", colMuted).Layout))
		}
		children = append(children, layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout))
		children = append(children, layout.Rigid(g.label(12, "显存", colMuted).Layout))
		if t.GPUMemUsedAvail && t.GPUMemTotal > 0 {
			children = append(children,
				layout.Rigid(g.bold(15, fmt.Sprintf("%s / %s", fmtB(t.GPUMemUsed), fmtB(t.GPUMemTotal)), colText).Layout),
			)
		} else {
			children = append(children, layout.Rigid(g.bold(15, "N/A", colMuted).Layout))
		}
		children = append(children,
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(g.label(12, "温度", colMuted).Layout),
			layout.Rigid(g.bold(20, tempText(t.GPUTemp, t.GPUTempAvail), tempColor(t.GPUTemp)).Layout),
		)
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}
	return rowPanels(gtx,
		[]float32{1.1, 1.3, 0.9},
		[]bool{false, false, false},
		[]layout.Widget{netTempBody, diskBody, gpuBody},
	)
}

func tempText(v float64, ok bool) string {
	if ok && v > 0 {
		return fmt.Sprintf("%.1f°C", v)
	}
	return "N/A"
}

// ---------- 区块：进程排行 ----------

type procCol struct {
	name  string
	we    float32
	sort  int // -1 不可排序
	right bool
}

var procCols = []procCol{
	{"PID", 1.0, -1, false},
	{"进程名", 2.4, -1, false},
	{"CPU %", 1.2, 0, true},
	{"内存 %", 1.1, 1, true},
	{"内存占用", 1.4, 2, true},
	{"端口", 1.9, -1, true},
}

func (g *guiState) procSection(gtx layout.Context, s *Stats) layout.Dimensions {
	body := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(g.label(13, "PROCESS 进程排行", colAccent).Layout),
					layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return g.viewBtn(gtx, 0, "合并视图") }),
					layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return g.viewBtn(gtx, 1, "单进程视图") }),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(g.procHeader),
			layout.Rigid(layout.Spacer{Height: unit.Dp(2)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return g.procList(gtx, s) }),
		)
	}
	return panel(gtx, false, body)
}

func (g *guiState) viewBtn(gtx layout.Context, idx int, txt string) layout.Dimensions {
	bt := &g.viewBtns[idx]
	active := g.procView == idx
	l := material.ButtonLayout(g.th, bt)
	l.Background = colBtnOff
	l.CornerRadius = 4
	inner := g.label(12, txt, colMuted)
	if active {
		l.Background = colAccent
		inner = g.bold(12, txt, colBG)
	}
	return l.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.UniformInset(unit.Dp(6)).Layout(gtx, inner.Layout)
	})
}

func (g *guiState) procHeader(gtx layout.Context) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(procCols))
	for _, c := range procCols {
		c := c
		children = append(children, layout.Flexed(c.we, func(gtx layout.Context) layout.Dimensions {
			txt := c.name
			clr := colMuted
			if c.sort >= 0 && c.sort == g.sortKey {
				txt = "▼ " + c.name
				clr = colAccent
			}
			lbl := g.label(11, txt, clr)
			if c.right {
				lbl.Alignment = text.End
			}
			if c.sort >= 0 && c.sort < 3 {
				bt := &g.sortBtns[c.sort]
				// 裸 Clickable 接管点击；悬停/按压底色手动画在文字尺寸上
				// （不用 ButtonLayout，避免其 38dp 最小热区把表头撑得高低不一）
				return bt.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					m := op.Record(gtx.Ops)
					dims := layout.UniformInset(unit.Dp(2)).Layout(gtx, lbl.Layout)
					call := m.Stop()
					var hl color.NRGBA
					switch {
					case bt.Pressed():
						hl = colHoverStr
					case bt.Hovered():
						hl = colHover
					}
					if hl.A != 0 {
						bg := image.Rectangle{Max: dims.Size}
						paint.FillShape(gtx.Ops, hl,
							clip.RRect{Rect: bg, SE: 4, SW: 4, NW: 4, NE: 4}.Op(gtx.Ops))
					}
					call.Add(gtx.Ops)
					return dims
				})
			}
			return layout.UniformInset(unit.Dp(2)).Layout(gtx, lbl.Layout)
		}))
	}
	return layout.Flex{}.Layout(gtx, children...)
}

func (g *guiState) procList(gtx layout.Context, s *Stats) layout.Dimensions {
	src := s.Processes
	if g.procView == 1 {
		src = s.ProcessesSingle
	}
	rows := topSortedProcs(src, g.sortKey, procLimit)
	lst := material.List(g.th, &g.procScroll)
	lst.AnchorStrategy = material.Overlay
	return fixH(gtx, unit.Dp(320), func(gtx layout.Context) layout.Dimensions {
		return lst.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
			p := rows[i]
			return layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				name := p.Name
				if p.Count > 1 {
					name = fmt.Sprintf("%s ×%d", name, p.Count)
				}
				cells := []string{
					strconv.Itoa(int(p.PID)), name,
					fmt.Sprintf("%.1f", p.CPU),
					fmt.Sprintf("%.2f", p.MemPercent),
					fmtB(p.MemRSS),
					formatPorts(p.Ports),
				}
				colors := []color.NRGBA{colMuted, colText, cpuPillColor(p.CPU), colMuted, colMuted, colAccent}
				children := make([]layout.FlexChild, 0, len(procCols))
				for ci, c := range procCols {
					txt := cells[ci]
					clr := colors[ci]
					right := c.right
					children = append(children, layout.Flexed(c.we, func(gtx layout.Context) layout.Dimensions {
						lbl := g.label(11, txt, clr)
						if right {
							lbl.Alignment = text.End
						}
						lbl.MaxLines = 1
						lbl.Truncator = "…"
						return layout.UniformInset(unit.Dp(2)).Layout(gtx, lbl.Layout)
					}))
				}
				return layout.Flex{}.Layout(gtx, children...)
			})
		})
	})
}

func formatPorts(ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	n := minI(3, len(ports))
	s := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ","
		}
		s += strconv.Itoa(ports[i])
	}
	if len(ports) > n {
		s += fmt.Sprintf(" +%d", len(ports)-n)
	}
	return s
}

func topSortedProcs(list []ProcessStats, key int, limit int) []ProcessStats {
	out := make([]ProcessStats, len(list))
	copy(out, list)
	sort.SliceStable(out, func(i, j int) bool {
		var a, b float64
		switch key {
		case 1:
			a, b = float64(out[i].MemPercent), float64(out[j].MemPercent)
		case 2:
			a, b = float64(out[i].MemRSS), float64(out[j].MemRSS)
		default:
			a, b = out[i].CPU, out[j].CPU
		}
		return a > b
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ---------- 审批浮层 ----------

// drainAccess 收取 HTTP 协程投递的审批请求。
func (g *guiState) drainAccess() {
	for {
		select {
		case r := <-accessCh:
			g.pending = append(g.pending, r)
		default:
			return
		}
	}
}

// requeuePending 把窗口关闭瞬间尚未处理的请求转交给下一次重建的窗口。
func (g *guiState) requeuePending() {
	for _, request := range g.pending {
		select {
		case accessCh <- request:
		default:
			select {
			case request.resp <- false:
			default:
			}
		}
	}
	g.pending = nil
}

// pollClicks 在布局前消费本帧到达的按钮点击。
// 关键：gio 的 Clickable.Layout 开头会把事件队列排干并丢弃，
// 所以 Clicked() 必须在 Layout 之前调用（事件按上一帧注册的区域路由，本帧开头到达）。
func (g *guiState) pollClicks(gtx layout.Context) {
	g.pollDirectoryScanResult()
	backgroundEnabled := !g.settingsOpen && len(g.pending) == 0
	// 视图切换：合并视图 / 单进程视图
	for i := range g.viewBtns {
		for g.viewBtns[i].Clicked(gtx) {
			if backgroundEnabled {
				g.procView = i
			}
		}
	}
	// 表头排序列
	for i := range g.sortBtns {
		for g.sortBtns[i].Clicked(gtx) {
			if backgroundEnabled {
				g.sortKey = i
			}
		}
	}
	for g.settingsBt.Clicked(gtx) {
		if backgroundEnabled {
			g.openDirectorySettings()
		}
	}
	for g.settingsSaveBt.Clicked(gtx) {
		if g.settingsOpen && !g.settingsScanning && len(g.pending) == 0 {
			g.saveDirectorySettings()
		}
	}
	for g.settingsScanBt.Clicked(gtx) {
		if g.settingsOpen && !g.settingsScanning && len(g.pending) == 0 {
			g.startDirectoryScan()
		}
	}
	for g.settingsCancelBt.Clicked(gtx) {
		if g.settingsOpen && !g.settingsScanning && len(g.pending) == 0 {
			g.settingsOpen = false
			g.settingsStatus = ""
		}
	}
	// 审批卡按钮
	if len(g.pending) > 0 {
		for g.approveBt.Clicked(gtx) {
			g.respondHead(true)
		}
		for g.denyBt.Clicked(gtx) {
			g.respondHead(false)
		}
	}
}

func (g *guiState) startDirectoryScan() {
	roots := slideshowSearchRoots()
	if len(roots) == 0 {
		g.settingsStatus = "没有找到可扫描的磁盘"
		return
	}
	g.settingsScanning = true
	g.settingsStatus = fmt.Sprintf("正在扫描 %d 个磁盘中的“相册”目录…", len(roots))
	go func() {
		g.directoryScanCh <- directoryScanResult{
			directories: findAlbumDirectoriesConcurrently(roots, autoAlbumScanTimeout),
			rootCount:   len(roots),
		}
	}()
}

func (g *guiState) pollDirectoryScanResult() {
	select {
	case result := <-g.directoryScanCh:
		existing := normalizeSlideshowDirs(strings.Split(g.dirsEditor.Text(), "\n"))
		merged := mergeSlideshowDirs(existing, result.directories)
		g.dirsEditor.SetText(strings.Join(merged, "\n"))
		g.settingsScanning = false
		added := len(merged) - len(existing)
		g.settingsStatus = fmt.Sprintf("扫描完成：检查 %d 个磁盘，发现 %d 个目录，新增 %d 个；请确认后保存。", result.rootCount, len(result.directories), added)
	default:
	}
}

func (g *guiState) openDirectorySettings() {
	dirs, err := loadSlideshowDirs()
	g.settingsStatus = ""
	if err != nil {
		g.settingsStatus = err.Error()
	}
	g.dirsEditor.SetText(strings.Join(dirs, "\n"))
	g.settingsOpen = true
}

func (g *guiState) saveDirectorySettings() {
	dirs := normalizeSlideshowDirs(strings.Split(g.dirsEditor.Text(), "\n"))
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil {
			g.settingsStatus = fmt.Sprintf("目录不可访问：%s", dir)
			return
		}
		if !info.IsDir() {
			g.settingsStatus = fmt.Sprintf("不是目录：%s", dir)
			return
		}
	}
	if err := saveSlideshowDirs(dirs); err != nil {
		g.settingsStatus = err.Error()
		return
	}
	invalidateSlideshowLibrary()
	g.settingsOpen = false
	g.settingsStatus = ""
}

// settingsLauncher keeps the slideshow directory entry visible in the upper
// right corner without changing the dashboard's measured layout.
func (g *guiState) settingsLauncher(gtx layout.Context) {
	if g.settingsOpen || len(g.pending) > 0 {
		return
	}
	w := gtx.Dp(unit.Dp(44))
	margin := gtx.Dp(unit.Dp(16))
	x := gtx.Constraints.Max.X - w - margin
	if x < margin {
		x = margin
	}
	defer op.Offset(image.Pt(x, margin)).Push(gtx.Ops).Pop()
	gtx.Constraints.Min = image.Pt(w, w)
	gtx.Constraints.Max = image.Pt(w, w)
	g.settingsBt.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		buttonBG := colBtnOff
		if g.settingsBt.Pressed() {
			buttonBG = color.NRGBA{R: 0x0A, G: 0x61, B: 0x70, A: 0xFF}
		} else if g.settingsBt.Hovered() {
			buttonBG = color.NRGBA{R: 0x16, G: 0x3B, B: 0x50, A: 0xFF}
		}
		bounds := image.Rect(0, 0, w, w)
		paint.FillShape(gtx.Ops, buttonBG, clip.Ellipse(bounds).Op(gtx.Ops))
		drawGearIcon(gtx.Ops, image.Pt(w/2, w/2), gtx.Dp(unit.Dp(10)), colAccent, buttonBG)
		return layout.Dimensions{Size: image.Pt(w, w)}
	})
}

// drawGearIcon draws a small settings glyph from vector primitives so its
// appearance never depends on whether the selected system font contains ⚙.
func drawGearIcon(ops *op.Ops, center image.Point, radius int, icon, cutout color.NRGBA) {
	rootRadius := float64(radius) * 0.72
	tipRadius := float64(radius) * 1.16
	// Each tooth has recessed roots, short sloped shoulders and a flat tip.
	offsets := []struct {
		angle  float64
		radius float64
	}{
		{-22.5, rootRadius},
		{-16, rootRadius},
		{-12, tipRadius},
		{12, tipRadius},
		{16, rootRadius},
		{22.5, rootRadius},
	}
	var gear clip.Path
	gear.Begin(ops)
	first := true
	for tooth := 0; tooth < 8; tooth++ {
		centerAngle := -90.0 + float64(tooth)*45.0
		for _, point := range offsets {
			a := (centerAngle + point.angle) * math.Pi / 180
			p := f32.Pt(
				float32(center.X)+float32(math.Cos(a)*point.radius),
				float32(center.Y)+float32(math.Sin(a)*point.radius),
			)
			if first {
				gear.MoveTo(p)
				first = false
			} else {
				gear.LineTo(p)
			}
		}
	}
	gear.Close()
	paint.FillShape(ops, icon, clip.Outline{Path: gear.End()}.Op())

	holeRadius := radius * 38 / 100
	hole := image.Rect(center.X-holeRadius, center.Y-holeRadius, center.X+holeRadius, center.Y+holeRadius)
	paint.FillShape(ops, cutout, clip.Ellipse(hole).Op(ops))
}

func (g *guiState) directoryEditorBox(gtx layout.Context) layout.Dimensions {
	size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(unit.Dp(210)))
	bg := image.Rectangle{Max: size}
	paint.FillShape(gtx.Ops, colTrack, clip.RRect{Rect: bg, SE: 5, SW: 5, NW: 5, NE: 5}.Op(gtx.Ops))
	ring(gtx.Ops, bg, 1, colLine)

	pad := gtx.Dp(unit.Dp(10))
	cgtx := gtx
	cgtx.Constraints.Min = image.Point{}
	cgtx.Constraints.Max = image.Pt(size.X-2*pad, size.Y-2*pad)
	stack := clip.Rect(bg).Push(gtx.Ops)
	offset := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	ed := material.Editor(g.th, &g.dirsEditor, "例如：D:\\照片\\旅行（每行一个目录）")
	ed.Color = colText
	ed.HintColor = colMuted
	ed.TextSize = unit.Sp(13)
	ed.Layout(cgtx)
	offset.Pop()
	stack.Pop()
	return layout.Dimensions{Size: size}
}

// settingsActionButton draws every settings action inside the exact same box.
// material.Button derives part of its size from surrounding Flex constraints,
// which made the first action a few pixels wider than the nested actions.
func (g *guiState) settingsActionButton(gtx layout.Context, clickable *widget.Clickable, label string, background, foreground color.NRGBA) layout.Dimensions {
	size := image.Pt(gtx.Dp(unit.Dp(120)), gtx.Dp(unit.Dp(38)))
	gtx.Constraints.Min = size
	gtx.Constraints.Max = size
	return clickable.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		bounds := image.Rectangle{Max: size}
		paint.FillShape(gtx.Ops, background, clip.RRect{Rect: bounds, SE: 4, SW: 4, NW: 4, NE: 4}.Op(gtx.Ops))
		if clickable.Pressed() {
			paint.FillShape(gtx.Ops, colHoverStr, clip.RRect{Rect: bounds, SE: 4, SW: 4, NW: 4, NE: 4}.Op(gtx.Ops))
		} else if clickable.Hovered() {
			paint.FillShape(gtx.Ops, colHover, clip.RRect{Rect: bounds, SE: 4, SW: 4, NW: 4, NE: 4}.Op(gtx.Ops))
		}
		return layout.Center.Layout(gtx, g.bold(13, label, foreground).Layout)
	})
}

func (g *guiState) directorySettingsOverlay(gtx layout.Context) {
	if !g.settingsOpen {
		return
	}
	mx, my := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	paint.FillShape(gtx.Ops, colModalMask, clip.Rect(image.Rect(0, 0, mx, my)).Op())

	cardW := minI(gtx.Dp(unit.Dp(650)), mx-2*gtx.Dp(unit.Dp(24)))
	cardH := minI(gtx.Dp(unit.Dp(390)), my-2*gtx.Dp(unit.Dp(24)))
	body := func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			layout.Rigid(g.bold(17, "幻灯片目录设置", colAccent).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			layout.Rigid(g.label(12, "每行填写一个目录；也可手动扫描名称中包含“相册”的目录。", colMuted).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			layout.Rigid(g.label(10, "SQLite: "+monitorDBPath(), colMuted).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
			layout.Rigid(g.directoryEditorBox),
		}
		if g.settingsStatus != "" {
			children = append(children,
				layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
				layout.Rigid(g.label(11, g.settingsStatus, colRed).Layout),
			)
		} else {
			children = append(children, layout.Rigid(layout.Spacer{Height: unit.Dp(17)}.Layout))
		}
		children = append(children,
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				scanLabel := "扫描目录"
				if g.settingsScanning {
					scanLabel = "正在扫描…"
				}
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return g.settingsActionButton(gtx, &g.settingsScanBt, scanLabel, colBtnOff, colAccent)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return g.settingsActionButton(gtx, &g.settingsCancelBt, "取 消", colBtnOff, colText)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return g.settingsActionButton(gtx, &g.settingsSaveBt, "保 存", colAccent, colBG)
					}),
				)
			}),
		)
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	}

	x, y := (mx-cardW)/2, (my-cardH)/2
	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()
	shadow := image.Rect(-12, -12, cardW+12, cardH+12)
	paint.FillShape(gtx.Ops, color.NRGBA{A: 0x58}, clip.RRect{Rect: shadow, SE: 16, SW: 16, NW: 16, NE: 16}.Op(gtx.Ops))
	gtx.Constraints.Min = image.Pt(cardW, cardH)
	gtx.Constraints.Max = image.Pt(cardW, cardH)
	panelH(gtx, false, body)
}

// checkApprovalTimeout 队首请求超过 30 秒未处理则自动拒绝。
func (g *guiState) checkApprovalTimeout() {
	if len(g.pending) > 0 && time.Since(g.pending[0].created) > 30*time.Second {
		g.respondHead(false)
	}
}

func (g *guiState) respondHead(ok bool) {
	if len(g.pending) == 0 {
		return
	}
	r := g.pending[0]
	g.pending = g.pending[1:]
	select {
	case r.resp <- ok:
	default:
	}
	if ok {
		fmt.Printf("已允许设备 %s 接入监控\n", r.ip)
	} else {
		fmt.Printf("已拒绝设备 %s 的连接请求（60 秒内不再弹窗）\n", r.ip)
	}
}

// approvalOverlay 在窗口右下角绘制审批卡片（队列非空时）。
func (g *guiState) approvalOverlay(gtx layout.Context) {
	if len(g.pending) == 0 {
		return
	}
	req := g.pending[0]
	cardW := gtx.Dp(unit.Dp(340))
	mx, my := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	// 先压暗整个监控界面，让审批请求具有明确的模态层级。
	paint.FillShape(gtx.Ops, colModalMask, clip.Rect(image.Rect(0, 0, mx, my)).Op())
	left := int(30 - time.Since(req.created).Seconds())
	if left < 0 {
		left = 0
	}
	body := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(g.bold(14, "⚠ 新设备连接请求", colAccent).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			layout.Rigid(g.label(13, fmt.Sprintf("设备 %s 请求接入监控服务", req.ip), colText).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			layout.Rigid(g.label(11, fmt.Sprintf("%d 秒内未操作将自动拒绝", left), colMuted).Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						b := material.Button(g.th, &g.approveBt, "允 许")
						b.Background = colAccent
						b.Color = colBG
						b.Font.Weight = 700
						b.CornerRadius = 4
						b.Inset = layout.UniformInset(unit.Dp(10))
						return b.Layout(gtx)
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						b := material.Button(g.th, &g.denyBt, "拒 绝")
						b.Background = colBtnOff
						b.Color = colText
						b.CornerRadius = 4
						b.Inset = layout.UniformInset(unit.Dp(10))
						return b.Layout(gtx)
					}),
				)
			}),
		)
	}
	// 先在临时绘制队列上测量卡片高度，再按底边对齐定位，避免溢出窗口
	var probe op.Ops
	pgtx := gtx
	pgtx.Ops = &probe
	pgtx.Constraints.Min = image.Point{}
	pgtx.Constraints.Max = image.Pt(cardW, my)
	cardDims := panel(pgtx, false, body)
	cardH := cardDims.Size.Y

	margin := gtx.Dp(unit.Dp(16))
	defer op.Offset(image.Pt(mx-cardW-margin, my-cardH-margin)).Push(gtx.Ops).Pop()
	// 多层半透明圆角底模拟柔和投影；随后绘制的实体卡片会覆盖投影中心。
	shadowLayers := []struct {
		spread int
		alpha  uint8
	}{
		{spread: gtx.Dp(unit.Dp(16)), alpha: 0x22},
		{spread: gtx.Dp(unit.Dp(10)), alpha: 0x38},
		{spread: gtx.Dp(unit.Dp(5)), alpha: 0x62},
	}
	for _, shadow := range shadowLayers {
		r := image.Rect(-shadow.spread, -shadow.spread, cardW+shadow.spread, cardH+shadow.spread)
		paint.FillShape(gtx.Ops, color.NRGBA{A: shadow.alpha}, clip.RRect{
			Rect: r,
			SE:   shadow.spread,
			SW:   shadow.spread,
			NW:   shadow.spread,
			NE:   shadow.spread,
		}.Op(gtx.Ops))
	}
	gtx.Constraints.Min.X = cardW
	gtx.Constraints.Max.X = cardW
	panel(gtx, false, body)
}

// ---------- 主循环 ----------

// runGUI 打开窗口并运行事件循环。
// 返回值表示窗口结束原因：true 为托盘「退出」或窗口异常，进程应结束；
// false 为用户点关闭按钮，窗口已销毁但进程应缩到托盘等待唤回。
func runGUI() (quit bool) {
	w := new(app.Window)
	w.Option(app.Title("电脑运行监控"), app.Size(unit.Dp(980), unit.Dp(760)), app.MinSize(unit.Dp(980), unit.Dp(760)))
	g := newGUIState()
	guiActive.Store(true)
	defer guiActive.Store(false)

	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			if e.Err != nil {
				for len(g.pending) > 0 {
					g.respondHead(false)
				}
				fmt.Fprintf(os.Stderr, "窗口异常退出：%v\n", e.Err)
				return true
			}
			g.requeuePending()
			return false
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			paint.Fill(gtx.Ops, colBG)
			g.drainAccess()

			// 必须在任何 Layout 之前消费点击：Clickable.Layout 会排干事件队列
			g.pollClicks(gtx)
			g.layoutRoot(gtx, snapshot.Load())
			g.settingsLauncher(gtx)
			g.directorySettingsOverlay(gtx)
			g.approvalOverlay(gtx)

			// 每秒重绘：时钟、告警与审批倒计时持续刷新
			gtx.Execute(op.InvalidateCmd{At: time.Now().Add(time.Second)})
			e.Frame(gtx.Ops)
			g.checkApprovalTimeout()
		}
	}
}

func (g *guiState) layoutRoot(gtx layout.Context, s *Stats) layout.Dimensions {
	if s == nil {
		return layout.Center.Layout(gtx, g.label(16, "等待首个数据帧…", colMuted).Layout)
	}
	lst := material.List(g.th, &g.pageList)
	lst.AnchorStrategy = material.Overlay
	return lst.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
		page := gtx
		wmax := gtx.Constraints.Max.X - gtx.Dp(unit.Dp(20))
		if wmax < 200 {
			wmax = 200
		}
		// 列表项的宽度约束默认无界，Flexed 子项会塌成 0；
		// 这里把页宽锁定为窗口宽（Min 保持 0，否则 Inset 收缩 Max 后 Min>Max 会破坏约束）。
		page.Constraints.Max.X = wmax
		return layout.Inset{Left: unit.Dp(10), Right: unit.Dp(10), Top: unit.Dp(14)}.Layout(page, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Center.Layout(gtx, g.layoutClock)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return g.topRow(gtx, s) }),
				layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return g.netRow(gtx, s) }),
				layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return g.procSection(gtx, s) }),
				layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
			)
		})
	})
}

// appMain 必须在主协程调用（gio 要求），驱动窗口事件泵；所有窗口关闭后返回。
func appMain() { app.Main() }

// guiEnabled 报告是否需要打开窗口（MONITOR_NO_GUI=1 关闭）。
func guiEnabled() bool { return os.Getenv("MONITOR_NO_GUI") != "1" }
