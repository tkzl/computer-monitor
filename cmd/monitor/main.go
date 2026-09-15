// 电脑运行监控程序（Go 版）
//
// 使用 gopsutil 采集 CPU、内存、磁盘、网络与进程排行，
// 通过 SSE（Server-Sent Events）向前端实时推送系统状态快照。
//
// 用法：
//
//	go run .
//
// 启动后自动打开浏览器访问 http://127.0.0.1:8765/
// 按 Ctrl+C 停止。
//
// 可选环境变量：
//
//	MONITOR_PORT        修改端口（默认 8765）
//	MONITOR_NO_BROWSER  设为 1 时不自动打开浏览器
//	MONITOR_LAN         默认监听所有网卡，局域网设备（如手机）可访问；
//	                    设为 0 时回退为仅本机可访问
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	pnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/shirou/gopsutil/v4/sensors"
	"github.com/yusufpapurcu/wmi"
)

const (
	procLimit      = 15               // 排行榜返回的进程数
	sampleInterval = 2 * time.Second  // 采样与 SSE 推送间隔
	partCacheSec   = 60               // 磁盘分区列表缓存时长（秒）
	portInterval   = 10 * time.Second // 端口表较重，独立降频刷新
	diskIOInterval = 2 * time.Second  // 磁盘吞吐采样间隔
	tempInterval   = 10 * time.Second // 温度/SMART 变化慢，低频读取避免唤醒磁盘
	discoverPort   = 8766             // 局域网发现服务端口（UDP，供手机客户端自动扫描）
)

//go:embed web/dashboard.html
var dashboardHTML []byte

// ---------- 数据结构（字段名与前端约定一致） ----------

type CPUStats struct {
	Percent float64   `json:"percent"`  // CPU 总使用率（百分比，0-100）
	PerCore []float64 `json:"per_core"` // 每个逻辑核心的使用率列表（百分比）
	Cores   int       `json:"cores"`    // 逻辑核心总数
	Source  string    `json:"source"`   // 总 CPU 使用率的数据来源
}

type MemoryStats struct {
	Total     uint64  `json:"total"`     // 物理内存总量（字节）
	Used      uint64  `json:"used"`      // 已使用内存（字节）
	Available uint64  `json:"available"` // 可用内存（字节）
	Percent   float64 `json:"percent"`   // 内存使用率（百分比，0-100）
}

type DiskStats struct {
	Mount   string  `json:"mount"`   // 挂载点（Windows 下为盘符，如 C:）
	FSType  string  `json:"fstype"`  // 文件系统类型（如 NTFS）
	Total   uint64  `json:"total"`   // 分区总容量（字节）
	Used    uint64  `json:"used"`    // 已使用容量（字节）
	Percent float64 `json:"percent"` // 使用率（百分比，0-100）
}

type NetworkStats struct {
	SentRate  float64 `json:"sent_rate"`  // 当前上传速率（字节/秒）
	RecvRate  float64 `json:"recv_rate"`  // 当前下载速率（字节/秒）
	BytesSent uint64  `json:"bytes_sent"` // 本次开机累计上传字节数
	BytesRecv uint64  `json:"bytes_recv"` // 本次开机累计下载字节数
	Interface string  `json:"interface"`  // 参与统计的物理网卡名称
}

// DiskIOStats 是全部本地磁盘卷的实时吞吐与平均忙碌率。
type DiskIOStats struct {
	ReadRate    float64 `json:"read_rate"`    // 读取速率（字节/秒）
	WriteRate   float64 `json:"write_rate"`   // 写入速率（字节/秒）
	ReadBytes   uint64  `json:"read_bytes"`   // 本次开机累计读取字节数
	WriteBytes  uint64  `json:"write_bytes"`  // 本次开机累计写入字节数
	BusyPercent float64 `json:"busy_percent"` // Windows 物理磁盘总体忙碌率（0-100）
	BusyAvail   bool    `json:"busy_avail"`   // 忙碌率性能计数器是否可用
	Devices     int     `json:"devices"`      // 参与聚合的本地磁盘卷数
}

type ProcessStats struct {
	PID        int32   `json:"pid"`         // 进程 ID（聚合条目为组内某一进程的 PID）
	Name       string  `json:"name"`        // 进程名（可执行文件名）
	CPU        float64 `json:"cpu"`         // CPU 占用（占总 CPU 的百分比，聚合条目为组内之和）
	MemPercent float32 `json:"mem_percent"` // 内存占用（占总物理内存的百分比，聚合条目为组内之和）
	MemRSS     uint64  `json:"mem_rss"`     // 常驻内存集大小（字节，聚合条目为组内之和）
	Count      int     `json:"count"`       // 该条目包含的同名进程数；1 表示单进程
	Ports      []int   `json:"ports"`       // 监听的端口列表（TCP LISTEN + UDP）；空表示未开放任何端口
}

type SystemStats struct {
	BootTime  uint64  `json:"boot_time"`  // 系统开机时间（Unix 秒级时间戳）
	Uptime    float64 `json:"uptime"`     // 已运行时长（秒）
	ProcCount int     `json:"proc_count"` // 当前进程总数
}

// DiskTemperature 单个物理磁盘的温度（来自 SMART 属性）。
type DiskTemperature struct {
	Name  string  `json:"name"`  // 磁盘型号（如 NVMe Micron 2500）
	Temp  float64 `json:"temp"`  // 温度（℃）；Avail 为 false 时无意义
	Avail bool    `json:"avail"` // 是否成功读到温度
}

// TemperatureStats 汇总 CPU、GPU 与各磁盘的温度。
// 温度读取较慢（WMI / nvidia-smi），由后台低频刷新并缓存。
type TemperatureStats struct {
	CPUTempAvail    bool              `json:"cpu_temp_avail"`     // CPU/整机温度是否可用
	CPUTemp         float64           `json:"cpu_temp"`           // CPU/整机温度（℃）
	GPUTempAvail    bool              `json:"gpu_temp_avail"`     // GPU 温度是否可用
	GPUTemp         float64           `json:"gpu_temp"`           // GPU 温度（℃）
	GPUName         string            `json:"gpu_name"`           // GPU 型号
	GPUUtilAvail    bool              `json:"gpu_util_avail"`     // GPU 使用率是否可用
	GPUUtil         float64           `json:"gpu_util"`           // GPU 使用率（%）
	GPUMemUsedAvail bool              `json:"gpu_mem_used_avail"` // GPU 显存已用是否可用
	GPUMemUsed      uint64            `json:"gpu_mem_used"`       // GPU 显存已用（字节）
	GPUMemTotal     uint64            `json:"gpu_mem_total"`      // GPU 显存总量（字节）
	Disks           []DiskTemperature `json:"disks"`              // 各磁盘温度列表
	CPUSource       string            `json:"cpu_source"`         // CPU/整机温度传感器来源
	GPUSource       string            `json:"gpu_source"`         // GPU 温度传感器来源
	UpdatedAt       float64           `json:"updated_at"`         // 最近一次取得任意新温度的 Unix 时间
}

type Stats struct {
	Time            float64          `json:"time"`             // 快照采集时间（Unix 秒，含小数）
	CPU             CPUStats         `json:"cpu"`              // CPU 状态
	Memory          MemoryStats      `json:"memory"`           // 内存状态
	Disks           []DiskStats      `json:"disks"`            // 各磁盘分区用量列表
	DiskIO          DiskIOStats      `json:"disk_io"`          // 本地磁盘实时读写状态
	Network         NetworkStats     `json:"network"`          // 网络速率与累计流量
	Processes       []ProcessStats   `json:"processes"`        // 同名聚合后的进程排行（对齐任务管理器"应用"视图）
	ProcessesSingle []ProcessStats   `json:"processes_single"` // 单进程视图（对齐任务管理器"详细信息"标签）
	System          SystemStats      `json:"system"`           // 系统整体信息
	Temperature     TemperatureStats `json:"temperature"`      // CPU/GPU/磁盘温度（后台低频刷新缓存）
}

// ---------- 采样状态 ----------

var (
	snapshot    atomic.Pointer[Stats]              // 最新快照，SSE 与 /api/stats 直接读取
	lastNet     *pnet.IOCountersStat               // 上一次采样的网络累计计数，用于计算速率差值
	lastNetT    time.Time                          // 上一次网络采样的时间
	lastNetName string                             // 上一次参与统计的网卡集合，切网时用于重建基线
	partCache   []disk.PartitionStat               // 磁盘分区列表缓存（枚举较慢，缓存复用）
	partCacheT  time.Time                          // 分区缓存的写入时间
	logicalCPU  int                                // 逻辑核心数，用于把单进程 CPU 占用换算为占总 CPU 的百分比
	temperature atomic.Pointer[TemperatureStats]   // 温度缓存（后台低频刷新；WMI/nvidia-smi 调用慢，不能每帧都读）
	diskIO      atomic.Pointer[DiskIOStats]        // 磁盘吞吐缓存（独立采样，避免拖慢主快照）
	listenPorts atomic.Pointer[listenPortSnapshot] // 监听端口缓存（独立低频刷新）
	processes   atomic.Pointer[processSnapshot]    // 进程排行缓存（独立刷新）
	procSamples = make(map[int32]processCPUSample) // 每进程上一帧 CPU 累计时间
)

type listenPortSnapshot struct {
	ports map[int32][]int
}

type processSnapshot struct {
	grouped []ProcessStats
	single  []ProcessStats
}

type processCPUSample struct {
	createTime int64
	total      float64
	at         time.Time
}

// skipFSTypes 为需要忽略的文件系统类型集合：
// 这些多为虚拟/光盘类介质，通常不是监控目标。
var skipFSTypes = map[string]struct{}{
	"tmpfs": {}, "cdfs": {}, "udf": {}, "iso9660": {},
}

// collectDisks 汇总各本地磁盘分区的用量。
// 分区列表在本机枚举较慢，故缓存 partCacheSec 秒复用；各分区用量实时读取（很快）。
func collectDisks() []DiskStats {
	now := time.Now()
	// 缓存过期或首次调用时重新枚举分区
	if now.Sub(partCacheT) >= partCacheSec*time.Second || len(partCache) == 0 {
		parts, err := disk.Partitions(false) // false：只枚举物理分区
		if err == nil {
			filtered := parts[:0] // 原地过滤，避免额外内存分配
			for _, p := range parts {
				// 跳过光盘/镜像等虚拟文件系统
				if _, skip := skipFSTypes[strings.ToLower(p.Fstype)]; skip {
					continue
				}
				filtered = append(filtered, p)
			}
			partCache = filtered
			partCacheT = now
		}
	}
	disks := make([]DiskStats, 0, len(partCache))
	for _, p := range partCache {
		usage, err := disk.Usage(p.Mountpoint)
		// 跳过无法访问或容量为 0 的分区（如空光驱）
		if err != nil || usage.Total == 0 {
			continue
		}
		disks = append(disks, DiskStats{
			Mount:   p.Mountpoint,
			FSType:  p.Fstype,
			Total:   usage.Total,
			Used:    usage.Used,
			Percent: usage.UsedPercent,
		})
	}
	return disks
}

type diskIOSample struct {
	readBytes  uint64
	writeBytes uint64
	devices    int
	signature  string
	at         time.Time
}

var lastDiskIO *diskIOSample

// collectDiskIO 聚合物理磁盘累计计数，并用相邻快照计算实时吞吐。
func collectDiskIO() (*DiskIOStats, bool) {
	counters, err := disk.IOCounters()
	if err != nil || len(counters) == 0 {
		return nil, false
	}
	now := time.Now()
	current := diskIOSample{devices: len(counters), at: now}
	names := make([]string, 0, len(counters))
	for name, counter := range counters {
		names = append(names, name)
		current.readBytes += counter.ReadBytes
		current.writeBytes += counter.WriteBytes
	}
	sort.Strings(names)
	current.signature = strings.Join(names, "\x00")
	out := &DiskIOStats{
		ReadBytes:  current.readBytes,
		WriteBytes: current.writeBytes,
		Devices:    current.devices,
	}
	if lastDiskIO != nil && lastDiskIO.signature == current.signature {
		dt := now.Sub(lastDiskIO.at).Seconds()
		if dt > 0 && dt <= (3*diskIOInterval).Seconds() {
			out.ReadRate = counterRate(current.readBytes, lastDiskIO.readBytes, dt)
			out.WriteRate = counterRate(current.writeBytes, lastDiskIO.writeBytes, dt)
		}
	}
	lastDiskIO = &current
	return out, true
}

func diskIOLoop(ctx context.Context) {
	for {
		if value, ok := collectDiskIO(); ok {
			if busy, available := diskBusyPercent(); available {
				value.BusyPercent = round1(busy)
				value.BusyAvail = true
			}
			diskIO.Store(value)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(diskIOInterval):
		}
	}
}

// physicalNetworkCounters 聚合当前已启用、拥有有效地址的物理网卡。
// 这能避免 VPN/TUN、VMware、Hyper-V 和回环接口与物理网卡重复计流量。
func physicalNetworkCounters() (pnet.IOCountersStat, string, bool) {
	counters, err := pnet.IOCounters(true)
	if err != nil || len(counters) == 0 {
		return pnet.IOCountersStat{}, "", false
	}
	byName := make(map[string]pnet.IOCountersStat, len(counters))
	for _, counter := range counters {
		byName[strings.ToLower(counter.Name)] = counter
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return pnet.IOCountersStat{}, "", false
	}
	var total pnet.IOCountersStat
	names := make([]string, 0, 2)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || nicScore(iface.Name) == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		usable := false
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				usable = true
				break
			}
		}
		if !usable {
			continue
		}
		counter, ok := byName[strings.ToLower(iface.Name)]
		if !ok {
			continue
		}
		total.BytesSent += counter.BytesSent
		total.BytesRecv += counter.BytesRecv
		names = append(names, iface.Name)
	}
	return total, strings.Join(names, " + "), len(names) > 0
}

// collectNetwork 计算物理网卡的实时上传/下载速率与开机累计流量。
// 速率由两次采样的累计字节差除以时间间隔得出，首次采样只记录基线、返回 0。
func collectNetwork() NetworkStats {
	cur, interfaceName, ok := physicalNetworkCounters()
	if !ok {
		// 个别平台的接口名称无法与系统网卡对应时，退回 gopsutil 聚合值，
		// 保证监控不断流；Interface 明确标记为全部接口。
		counters, err := pnet.IOCounters(false)
		if err != nil || len(counters) == 0 {
			return NetworkStats{}
		}
		cur = counters[0]
		interfaceName = "全部接口"
	}
	now := time.Now()
	out := NetworkStats{BytesSent: cur.BytesSent, BytesRecv: cur.BytesRecv, Interface: interfaceName}
	if lastNet != nil && lastNetName == interfaceName {
		dt := now.Sub(lastNetT).Seconds()
		if dt > 0 && dt <= (3*sampleInterval).Seconds() {
			// 先比较再做 uint64 减法，避免计数器回绕或网卡重置时下溢为巨量流量。
			out.SentRate = counterRate(cur.BytesSent, lastNet.BytesSent, dt)
			out.RecvRate = counterRate(cur.BytesRecv, lastNet.BytesRecv, dt)
		}
	}
	// 记录本次采样，作为下一次速率计算的基线
	lastNet = &cur
	lastNetT = now
	lastNetName = interfaceName
	return out
}

// counterRate 根据累计计数器计算每秒速率。计数器回绕/重置或时间无效时返回 0。
func counterRate(current, previous uint64, seconds float64) float64 {
	if seconds <= 0 || current < previous {
		return 0
	}
	return float64(current-previous) / seconds
}

// collectListenPorts 采集各进程开放的监听端口（TCP LISTEN + UDP 本地端口），
// 按 PID 归组、去重并升序排序。每个采样周期只全量拉取一次连接表；
// 拉取失败返回空表，进程照常展示，仅端口列为空。
func collectListenPorts() map[int32][]int {
	out := make(map[int32][]int)
	seen := make(map[int32]map[int]struct{})
	add := func(pid int32, port uint32) {
		if pid <= 0 || port == 0 { // 内核归属(0)与未绑定端口(0)无意义
			return
		}
		set, ok := seen[pid]
		if !ok {
			set = make(map[int]struct{})
			seen[pid] = set
		}
		if _, dup := set[int(port)]; !dup {
			set[int(port)] = struct{}{}
			out[pid] = append(out[pid], int(port))
		}
	}
	if conns, err := pnet.Connections("tcp"); err == nil {
		for _, c := range conns {
			// Windows/Linux 状态字符串不同，两种写法都兼容
			if c.Status == "LISTEN" || c.Status == "LISTENING" {
				add(c.Pid, c.Laddr.Port)
			}
		}
	}
	if conns, err := pnet.Connections("udp"); err == nil {
		for _, c := range conns {
			add(c.Pid, c.Laddr.Port) // UDP 无连接状态，本地端口即监听端口
		}
	}
	for pid := range out {
		sort.Ints(out[pid])
	}
	return out
}

func portLoop(ctx context.Context) {
	for {
		listenPorts.Store(&listenPortSnapshot{ports: collectListenPorts()})
		select {
		case <-ctx.Done():
			return
		case <-time.After(portInterval):
		}
	}
}

// processCPUPercent 把相邻两帧的进程累计 CPU 时间换算为占整机 CPU 的百分比。
func processCPUPercent(previous, current, elapsed float64, cores int) float64 {
	if elapsed <= 0 || cores <= 0 || current < previous {
		return 0
	}
	percent := (current - previous) / elapsed * 100 / float64(cores)
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

// collectProcessStats 采集全部进程的占用情况（单进程粒度）。
// CPU 使用相邻快照的累计时间差计算，createTime 用于识别 PID 被新进程复用。
func collectProcessStats(totalMemory uint64) []ProcessStats {
	procs, err := process.Processes()
	if err != nil {
		return nil
	}
	now := time.Now()
	portMap := map[int32][]int(nil)
	if cached := listenPorts.Load(); cached != nil {
		portMap = cached.ports
	}
	nextSamples := make(map[int32]processCPUSample, len(procs))
	all := make([]ProcessStats, 0, len(procs))
	for _, p := range procs {
		name, err := p.Name()
		if err != nil || strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("PID %d", p.Pid)
		}

		cpuPct := 0.0
		createTime, createErr := p.CreateTime()
		cpuTimes, timesErr := p.Times()
		if createErr == nil && timesErr == nil && cpuTimes != nil {
			current := processCPUSample{createTime: createTime, total: cpuTimes.Total(), at: now}
			if previous, ok := procSamples[p.Pid]; ok && previous.createTime == createTime {
				cpuPct = processCPUPercent(previous.total, current.total, now.Sub(previous.at).Seconds(), logicalCPU)
			}
			nextSamples[p.Pid] = current
		}

		var rss uint64
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			rss = mi.RSS
		}
		memPct := float32(0)
		if totalMemory > 0 {
			memPct = float32(100 * float64(rss) / float64(totalMemory))
		}
		all = append(all, ProcessStats{
			PID:        p.Pid,
			Name:       name,
			CPU:        cpuPct,
			MemPercent: float32(memPct),
			MemRSS:     rss,
			Count:      1,
			Ports:      portMap[p.Pid],
		})
	}
	procSamples = nextSamples
	return all
}

// aggregateByName 把同名进程合并为一条（CPU/内存求和、端口归并去重），
// 对齐任务管理器默认的"按应用分组"视图，例如 31 个 msedge.exe 合计展示。
func aggregateByName(all []ProcessStats) []ProcessStats {
	groups := make(map[string]*ProcessStats, len(all)/2)
	groupPorts := make(map[string]map[int]struct{}) // 每组端口的去重集合
	for i := range all {
		s := &all[i]
		g, ok := groups[s.Name]
		if !ok {
			g = &ProcessStats{Name: s.Name, PID: s.PID}
			groups[s.Name] = g
			groupPorts[s.Name] = make(map[int]struct{})
		}
		g.CPU += s.CPU
		g.MemPercent += s.MemPercent
		g.MemRSS += s.MemRSS
		g.Count++
		for _, p := range s.Ports {
			if _, dup := groupPorts[s.Name][p]; !dup {
				groupPorts[s.Name][p] = struct{}{}
				g.Ports = append(g.Ports, p)
			}
		}
	}
	out := make([]ProcessStats, 0, len(groups))
	for name, g := range groups {
		g.CPU = round1(g.CPU)
		g.MemPercent = float32(round2(float64(g.MemPercent)))
		sort.Ints(g.Ports)
		_ = name
		out = append(out, *g)
	}
	return out
}

// topUnion 取 CPU 榜与内存榜前 N 名的并集（按 PID 或名称去重），
// 保证前端按任一列排序都不缺数据。key 函数决定去重维度。
func topUnion(list []ProcessStats, key func(ProcessStats) any) []ProcessStats {
	byCPU := make([]ProcessStats, len(list))
	copy(byCPU, list) // 复制一份再排序，避免污染另一份数据
	sort.Slice(byCPU, func(i, j int) bool {
		if byCPU[i].CPU != byCPU[j].CPU {
			return byCPU[i].CPU > byCPU[j].CPU
		}
		return byCPU[i].MemRSS > byCPU[j].MemRSS // CPU 相同则按内存兜底
	})
	byMem := make([]ProcessStats, len(list))
	copy(byMem, list)
	sort.Slice(byMem, func(i, j int) bool {
		if byMem[i].MemRSS != byMem[j].MemRSS {
			return byMem[i].MemRSS > byMem[j].MemRSS
		}
		return byMem[i].MemPercent > byMem[j].MemPercent
	})
	picked := make(map[any]struct{}, procLimit*2) // 已选条目集合，用于去重
	out := make([]ProcessStats, 0, procLimit*2)
	// take 从排序好的榜单里取前 N 个未重复的条目
	take := func(ranked []ProcessStats) {
		n := procLimit
		if len(ranked) < n {
			n = len(ranked)
		}
		for _, s := range ranked[:n] {
			k := key(s)
			if _, dup := picked[k]; dup {
				continue
			}
			picked[k] = struct{}{}
			out = append(out, s)
		}
	}
	take(byCPU) // 先取 CPU 榜
	take(byMem) // 再补内存榜（自动跳过已入选的）
	return out
}

// collectProcesses 返回两个视图：同名聚合排行与单进程排行。
func collectProcesses(totalMemory uint64) (grouped, single []ProcessStats) {
	all := collectProcessStats(totalMemory)
	if all == nil {
		return nil, nil
	}
	grouped = topUnion(aggregateByName(all), func(s ProcessStats) any { return s.Name })
	single = topUnion(all, func(s ProcessStats) any { return s.PID })
	// 单进程视图保留一位/两位小数展示
	for i := range single {
		single[i].CPU = round1(single[i].CPU)
		single[i].MemPercent = float32(round2(float64(single[i].MemPercent)))
	}
	return grouped, single
}

func processLoop(ctx context.Context) {
	for {
		var totalMemory uint64
		if vm, err := mem.VirtualMemory(); err == nil && vm != nil {
			totalMemory = vm.Total
		}
		grouped, single := collectProcesses(totalMemory)
		processes.Store(&processSnapshot{grouped: grouped, single: single})
		select {
		case <-ctx.Done():
			return
		case <-time.After(sampleInterval):
		}
	}
}

// ---------- 温度采集 ----------

// wmiSmartData 对应 WMI 类 MSStorageDriver_ATAPISmartData 的一行。
type wmiSmartData struct {
	InstanceName   string
	VendorSpecific []uint8
}

// parseSmartTemp 从 SMART 厂商数据中解析磁盘温度。
// 数据布局：前 2 字节为头部，之后每 12 字节一条属性记录
// （ID、标志 2 字节、归一化值、最差值、原始值 6 字节）。
// 优先取属性 194（Temperature Celsius），回退属性 190（Airflow Temperature），
// 温度取原始值的低字节（摄氏度）。
func parseSmartTemp(data []uint8) (float64, bool) {
	const recordLen = 12
	fallback := 0.0
	found := false
	for base := 2; base+recordLen <= len(data); base += recordLen {
		id := data[base]
		if id != 194 && id != 190 {
			continue
		}
		t := float64(data[base+5]) // raw 低字节即摄氏度
		if t <= 0 || t >= 100 {    // 过滤明显异常值
			continue
		}
		if id == 194 {
			return t, true // 标准温度属性，直接采信
		}
		fallback = t
		found = true
	}
	return fallback, found
}

// diskDisplayName 从 WMI 实例名提取可读的磁盘型号。
// 例："SCSI\Disk&Ven_NVMe&Prod_Micron_2500_MTFD\4&..." -> "Micron 2500 MTFD"。
func diskDisplayName(instance string) string {
	if i := strings.Index(instance, "Prod_"); i >= 0 {
		rest := instance[i+len("Prod_"):]
		if j := strings.IndexByte(rest, '\\'); j >= 0 {
			rest = rest[:j]
		}
		return strings.ReplaceAll(rest, "_", " ")
	}
	return instance
}

// collectDiskTemperatures 逐个磁盘读取 SMART 温度。
// WMI 类 MSStorageDriver_ATATemperature 在新版 Windows 上已移除，
// 因此从 MSStorageDriver_ATAPISmartData 的原始数据里自行解析温度属性。
func collectDiskTemperatures() []DiskTemperature {
	var rows []wmiSmartData
	err := wmi.QueryNamespace("SELECT * FROM MSStorageDriver_ATAPISmartData", &rows, "root/wmi")
	if err != nil {
		return nil
	}
	out := make([]DiskTemperature, 0, len(rows))
	for _, r := range rows {
		temp, ok := parseSmartTemp(r.VendorSpecific)
		out = append(out, DiskTemperature{
			Name:  diskDisplayName(r.InstanceName),
			Temp:  temp,
			Avail: ok,
		})
	}
	return out
}

// collectTemperatures 汇总 CPU/整机、GPU 与各磁盘的温度。
// WMI 与 nvidia-smi 调用较慢（秒级），本函数由后台低频循环调用并缓存结果。
func collectTemperatures() *TemperatureStats {
	out := &TemperatureStats{}

	// 1. CPU/整机温度：优先用 gopsutil 读取 WMI 热区传感器
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	if temps, err := sensors.TemperaturesWithContext(ctx); err == nil && len(temps) > 0 {
		for _, t := range temps {
			if t.Temperature > out.CPUTemp {
				out.CPUTemp = t.Temperature
			}
		}
		if out.CPUTemp > 0 {
			out.CPUSource = "ACPI thermal zone"
		}
	}
	cancel()

	// 如果 gopsutil 没读到，尝试直接调用 PowerShell 读取 WMI 热区
	if out.CPUTemp <= 0 {
		cmdCtx, cmdCancel := context.WithTimeout(context.Background(), 8*time.Second)
		cmd := exec.CommandContext(cmdCtx, "powershell", "-NoProfile", "-Command",
			"$t = Get-CimInstance -Namespace root/wmi -ClassName MSAcpi_ThermalZoneTemperature -ErrorAction SilentlyContinue | Measure-Object -Property CurrentTemperature -Maximum; if ($t.Maximum) { Write-Output $t.Maximum }")
		hideWindow(cmd)
		if data, err := cmd.Output(); err == nil {
			raw := strings.TrimSpace(string(data))
			if val, err := strconv.ParseFloat(raw, 64); err == nil && val > 0 {
				// WMI 返回的是 Kelvin*10，转换为摄氏度
				out.CPUTemp = round1((val / 10) - 273.15)
				out.CPUSource = "WMI ACPI thermal zone"
			}
		}
		cmdCancel()
	}
	if out.CPUTemp > 0 {
		out.CPUTempAvail = true
	}

	// 2. GPU 信息：调用 nvidia-smi，一次查询温度、型号、使用率、显存，取第一块卡
	if path, err := exec.LookPath("nvidia-smi"); err == nil {
		cmdCtx, cmdCancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(cmdCtx, path, "--query-gpu=temperature.gpu,name,utilization.gpu,memory.used,memory.total", "--format=csv,noheader,nounits")
		hideWindow(cmd)
		if data, err := cmd.Output(); err == nil {
			line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
			parts := strings.Split(line, ",")
			if len(parts) >= 5 {
				if v, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64); err == nil && v > 0 {
					out.GPUTemp = v
					out.GPUTempAvail = true
					out.GPUSource = "nvidia-smi"
				}
				out.GPUName = strings.TrimSpace(parts[1])
				if v, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64); err == nil {
					out.GPUUtil = v
					out.GPUUtilAvail = true
				}
				if v, err := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64); err == nil && v >= 0 {
					out.GPUMemUsed = uint64(v * 1024 * 1024) // nvidia-smi 返回 MiB
					out.GPUMemUsedAvail = true
				}
				if v, err := strconv.ParseFloat(strings.TrimSpace(parts[4]), 64); err == nil && v >= 0 {
					out.GPUMemTotal = uint64(v * 1024 * 1024)
				}
			}
		}
		cmdCancel()
	}

	// 3. 磁盘温度：逐个磁盘解析 SMART
	out.Disks = collectDiskTemperatures()
	hasValidDiskTemp := false
	for _, disk := range out.Disks {
		if disk.Avail {
			hasValidDiskTemp = true
			break
		}
	}
	if out.CPUTempAvail || out.GPUTempAvail || hasValidDiskTemp {
		out.UpdatedAt = float64(time.Now().UnixMilli()) / 1000
	}
	return out
}

// mergeTemperatures 在某个传感器暂时失败时保留上一份有效数据。
func mergeTemperatures(previous, current *TemperatureStats) *TemperatureStats {
	if previous == nil {
		return current
	}
	merged := *current
	if !merged.CPUTempAvail && previous.CPUTempAvail {
		merged.CPUTempAvail = true
		merged.CPUTemp = previous.CPUTemp
		merged.CPUSource = previous.CPUSource
	}
	if !merged.GPUTempAvail && previous.GPUTempAvail {
		merged.GPUTempAvail = true
		merged.GPUTemp = previous.GPUTemp
		merged.GPUName = previous.GPUName
		merged.GPUSource = previous.GPUSource
	}
	if !merged.GPUUtilAvail && previous.GPUUtilAvail {
		merged.GPUUtilAvail = true
		merged.GPUUtil = previous.GPUUtil
	}
	if !merged.GPUMemUsedAvail && previous.GPUMemUsedAvail {
		merged.GPUMemUsedAvail = true
		merged.GPUMemUsed = previous.GPUMemUsed
		merged.GPUMemTotal = previous.GPUMemTotal
	}
	if len(merged.Disks) == 0 && len(previous.Disks) > 0 {
		merged.Disks = append([]DiskTemperature(nil), previous.Disks...)
	} else if len(previous.Disks) > 0 {
		lastByName := make(map[string]DiskTemperature, len(previous.Disks))
		for _, disk := range previous.Disks {
			lastByName[disk.Name] = disk
		}
		for i := range merged.Disks {
			if !merged.Disks[i].Avail {
				if last, ok := lastByName[merged.Disks[i].Name]; ok && last.Avail {
					merged.Disks[i] = last
				}
			}
		}
	}
	if merged.UpdatedAt == 0 {
		merged.UpdatedAt = previous.UpdatedAt
	}
	return &merged
}

// temperatureLoop 后台低频刷新温度缓存，节奏由 tempInterval 控制。
func temperatureLoop(ctx context.Context) {
	for {
		temperature.Store(mergeTemperatures(temperature.Load(), collectTemperatures()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(tempInterval):
		}
	}
}

// collectStats 汇总一次完整的系统状态快照，供 SSE 推送与 /api/stats 返回。
func collectStats() *Stats {
	now := time.Now()
	cpuPct, _ := cpu.Percent(0, false) // 0：非阻塞，取距上次调用以来的均值
	perCore, _ := cpu.Percent(0, true) // true：按核心分别返回
	vm, _ := mem.VirtualMemory()
	bt, _ := host.BootTime()
	uptime, uptimeErr := host.Uptime()
	if uptimeErr != nil && bt > 0 {
		uptime = uint64(max(0, now.Sub(time.Unix(int64(bt), 0)).Seconds()))
	}
	pids, _ := process.Pids()

	totalCPU := 0.0
	cpuSource := "gopsutil processor time"
	if len(cpuPct) > 0 {
		totalCPU = cpuPct[0]
	}
	if utility, available := cpuUsagePercent(); available {
		totalCPU = utility
		cpuSource = "Windows PDH Processor Utility"
	}
	s := &Stats{
		Time: float64(now.UnixMilli()) / 1000,
		CPU: CPUStats{
			Percent: round1(totalCPU),
			PerCore: roundAll(perCore),
			Cores:   logicalCPU,
			Source:  cpuSource,
		},
		Disks:   collectDisks(),
		Network: collectNetwork(),
		System: SystemStats{
			BootTime:  bt,
			Uptime:    float64(uptime),
			ProcCount: len(pids),
		},
	}
	if ioStats := diskIO.Load(); ioStats != nil {
		s.DiskIO = *ioStats
	}
	if processStats := processes.Load(); processStats != nil {
		s.Processes = processStats.grouped
		s.ProcessesSingle = processStats.single
	}
	// 温度读取较慢，使用后台低频刷新的缓存值
	if temp := temperature.Load(); temp != nil {
		s.Temperature = *temp
	}
	if vm != nil {
		s.Memory = MemoryStats{
			Total:     vm.Total,
			Used:      vm.Used,
			Available: vm.Available,
			Percent:   vm.UsedPercent,
		}
	}
	return s
}

// samplerLoop 后台采样：周期采集并更新快照，采集慢也不会阻塞 HTTP 请求。
// 用 select 等待而非 time.Sleep，以便在上下文取消时及时退出。
func samplerLoop(ctx context.Context) {
	for {
		t0 := time.Now()
		snapshot.Store(collectStats())
		select {
		case <-ctx.Done():
			return
		// 扣除本次采集耗时，保证整体节奏稳定在 sampleInterval；下限 200ms 防忙转
		case <-time.After(max(200*time.Millisecond, sampleInterval-time.Since(t0))):
		}
	}
}

// primeSensors 预热：CPU 百分比首次调用返回 0，需先触发一次计时。
func primeSensors() {
	logicalCPU, _ = cpu.Counts(true)
	cpu.Percent(0, false)
	cpu.Percent(0, true)
	procSamples = make(map[int32]processCPUSample)
	time.Sleep(500 * time.Millisecond)
}

// ---------- HTTP 处理 ----------

// writeJSON 以 UTF-8 JSON 形式写出响应，并禁止客户端/中间层缓存。
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// ---------- 设备接入审批 ----------
//
// 局域网新设备首次访问数据接口时，在电脑右下角弹出审批窗口（允许/拒绝，
// 30 秒未操作自动拒绝）。同一 IP 允许过一次后，本次程序运行期间不再询问。
// 本机地址（回环与本机网卡 IP）免审批。

var (
	accessMu     sync.Mutex               // 保护 allowedIPs / deniedUntil
	accessPrompt sync.Mutex               // 同一时间只弹一个审批窗口，后来者排队
	allowedIPs   = map[string]bool{}      // 已允许的设备
	deniedUntil  = map[string]time.Time{} // 被拒绝的设备冷却期，防弹窗风暴
	localIPSet   = map[string]bool{}      // 本机所有地址，免审批
)

// initLocalIPs 收集本机回环与各网卡地址，这些地址发起的请求免审批。
func initLocalIPs() {
	localIPSet["127.0.0.1"] = true
	localIPSet["::1"] = true
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				localIPSet[n.IP.String()] = true
			}
		}
	}
}

// clientIP 取请求方的 IP（去掉端口部分）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// checkAccess 判断远端设备是否有权访问监控数据。
// 返回 false 时调用方应以 403 拒绝响应。
func checkAccess(r *http.Request) bool {
	ip := clientIP(r)
	if localIPSet[ip] {
		return true // 本机浏览器与自身地址无需审批
	}
	if net.ParseIP(ip) == nil {
		return false // 伪造/非法来源地址一律拒绝
	}

	accessMu.Lock()
	if allowedIPs[ip] {
		accessMu.Unlock()
		return true
	}
	if exp, ok := deniedUntil[ip]; ok && time.Now().Before(exp) {
		accessMu.Unlock()
		return false
	}
	accessMu.Unlock()

	// 弹窗串行化：多设备同时接入时排队逐个确认
	accessPrompt.Lock()
	defer accessPrompt.Unlock()
	accessMu.Lock() // 排队期间可能已被上一次弹窗放行
	if allowedIPs[ip] {
		accessMu.Unlock()
		return true
	}
	accessMu.Unlock()

	ok := requestApproval(ip)
	accessMu.Lock()
	if ok {
		allowedIPs[ip] = true
	} else {
		deniedUntil[ip] = time.Now().Add(time.Minute)
	}
	accessMu.Unlock()
	if ok {
		fmt.Printf("已允许设备 %s 接入监控\n", ip)
	} else {
		fmt.Printf("已拒绝设备 %s 的连接请求（60 秒内不再弹窗）\n", ip)
	}
	return ok
}

// handleStats 返回最新快照（调试用，前端走 SSE）。
func handleStats(w http.ResponseWriter, r *http.Request) {
	if !checkAccess(r) {
		http.Error(w, "access denied by host", http.StatusForbidden)
		return
	}
	if s := snapshot.Load(); s != nil {
		writeJSON(w, s)
		return
	}
	writeJSON(w, struct{}{})
}

// handleStream SSE 推送：连接建立后立即发一帧，之后每 2 秒推送最新快照。
// 客户端断开（r.Context().Done）或写入失败时结束本连接。
func handleStream(w http.ResponseWriter, r *http.Request) {
	// 新设备首次接入需经电脑端右下角弹窗确认
	if !checkAccess(r) {
		http.Error(w, "access denied by host", http.StatusForbidden)
		return
	}
	// SSE 依赖 Flusher 将数据逐帧刷给客户端，不支持则无法推送
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// SSE 协议要求的响应头；X-Accel-Buffering 用于关闭 nginx 类反向代理的缓冲
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// send 按 SSE 格式写出一帧并立即刷新，返回是否成功
	send := func(s *Stats) bool {
		data, err := json.Marshal(s)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// 连接建立时立即推送当前快照，避免前端等待首个 tick
	if s := snapshot.Load(); s != nil {
		if !send(s) {
			return
		}
	}
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done(): // 客户端关闭页面或断网
			return
		case <-ticker.C:
			if s := snapshot.Load(); s != nil && !send(s) {
				return // 写入失败说明连接已断，退出协程
			}
		}
	}
}

// ---------- 工具函数 ----------

// round1 四舍五入保留 1 位小数（用于 CPU/内存百分比展示）。
func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// round2 四舍五入保留 2 位小数（用于进程内存百分比展示）。
func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }

// roundAll 对一组百分比值逐个保留 1 位小数（用于每核 CPU 数据）。
func roundAll(vs []float64) []float64 {
	out := make([]float64, len(vs))
	for i, v := range vs {
		out[i] = round1(v)
	}
	return out
}

// virtualNicHint 是虚拟网卡名称里的关键字（统一小写比较），
// 用于把 VMware、WSL、Hyper-V、代理等虚拟适配器从"本机局域网地址"里排除掉。
// "*" 用于排除 Windows 网络共享/移动热点产生的"本地连接* 1"这类虚拟适配器。
var virtualNicHint = []string{
	"*", "vmware", "vmnet", "vethernet", "hyper-v", "veth", "wsl",
	"docker", "loopback", "tap", "tailscale", "zerotier",
	"mihomo", "clash", "sing-box", "npntunnel", "panggin",
	"virtual", "isatap", "teredo", "anyconnect", "wintun",
	"tunnel", "bluetooth",
}

// nicScore 给网卡打分，分越高越可能是设备实际接入局域网的那块网卡：
//
//	4 物理有线   3 WLAN   2 其他已识别网卡   0 虚拟网卡（不参与候选）
func nicScore(name string) int {
	n := strings.ToLower(name)
	for _, hint := range virtualNicHint {
		if strings.Contains(n, hint) {
			return 0
		}
	}
	switch {
	case strings.Contains(n, "wlan") || strings.Contains(n, "wi-fi") || strings.Contains(n, "wifi"):
		return 3
	case strings.Contains(n, "ethernet") || strings.Contains(n, "以太网") || strings.Contains(n, "本地连接"):
		return 4
	default:
		return 2
	}
}

// lanIP 获取本机在局域网中的 IPv4 地址，供手机等设备访问。
// 本机往往挂着多块网卡（VMware、WSL、代理、热点等虚拟网卡），
// 直接取"第一个非回环地址"很容易命中虚拟网卡，因此：
//  1. 优先用默认路由的出口地址——即本机当前真正上网的那块网卡，最可靠；
//     但若出口是代理/虚拟网卡（如 Mihomo TUN），该地址对局域网无意义，跳过；
//  2. 退化遍历网卡，跳过虚拟网卡与 169.254.x.x（DHCP 失败自分配的地址），
//     按物理有线 > WLAN > 其他的顺序挑最优。
//
// 全部失败返回空串，由调用方给出提示。
func lanIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	// 1. 首选默认路由出口地址（UDP 拨号只选路，不会真正发包）。
	//    反查该地址属于哪块网卡：若出口是代理/虚拟网卡（如 Mihomo TUN 的
	//    198.18.x.x），这个地址对局域网设备没有意义，不放行，落到方案 2。
	if conn, err := net.Dial("udp4", "8.8.8.8:53"); err == nil {
		local, ok := conn.LocalAddr().(*net.UDPAddr)
		conn.Close()
		if ok && !local.IP.IsLoopback() && !local.IP.IsLinkLocalUnicast() {
			for _, iface := range ifaces {
				addrs, err := iface.Addrs()
				if err != nil {
					continue
				}
				for _, addr := range addrs {
					if ipNet, isNet := addr.(*net.IPNet); isNet && ipNet.IP.Equal(local.IP) {
						if nicScore(iface.Name) > 0 {
							return local.IP.String()
						}
					}
				}
			}
		}
	}

	// 2. 退化方案：遍历网卡按打分取最优
	best := ""
	bestScore := 0
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		score := nicScore(iface.Name)
		if score == 0 || score <= bestScore {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			if ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
				continue
			}
			best, bestScore = ipNet.IP.String(), score
			break
		}
	}
	return best
}

// openBrowser 用系统默认浏览器打开 URL，按平台选择对应命令。
func openBrowser(url string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	} else if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", url)
	} else { // Linux 等
		cmd = exec.Command("xdg-open", url)
	}
	hideWindow(cmd)
	_ = cmd.Start() // 打开失败不致命，忽略错误
}

// startDiscovery 启动局域网发现服务（UDP 广播应答）。
// 客户端向本端口广播 "MONITOR-DISCOVER"，本服务回复
// "MONITOR-FOUND {主机名, 端口}"；客户端用应答的源 IP 拼出访问地址，
// 从而在多网卡环境下也能拿到正确的局域网地址。
func startDiscovery(port string) {
	conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", discoverPort))
	if err != nil {
		fmt.Printf("发现服务未启动：%v（不影响网页访问）\n", err)
		return
	}
	hostname, _ := os.Hostname()
	reply, _ := json.Marshal(map[string]string{
		"host": hostname,
		"port": port,
	})
	buf := make([]byte, 64)
	go func() {
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return // 端口关闭或出错，退出应答循环
			}
			// 只响应发现请求，忽略其它杂散报文
			if n <= 0 || !strings.HasPrefix(string(buf[:n]), "MONITOR-DISCOVER") {
				continue
			}
			_, _ = conn.WriteTo(append([]byte("MONITOR-FOUND "), reply...), addr)
		}
	}()
	fmt.Printf("发现服务已启动：UDP %d（手机可自动发现本机）\n", discoverPort)
}

// main 程序入口：预热传感器 → 启动后台采样 → 注册路由 → 监听端口 → 阻塞等待退出。
func main() {
	// 端口可由环境变量覆盖，默认 8765
	port := os.Getenv("MONITOR_PORT")
	if port == "" {
		port = "8765"
	}
	if err := initSettingsDB(); err != nil {
		log.Printf("设置数据库初始化失败：%v", err)
	}
	refreshSlideshowLibraryAsync(true)

	initLocalIPs()                           // 本机地址集合，设备准入判断用
	primeSensors()                           // 预热，保证首个快照的 CPU 数据非 0
	temperature.Store(collectTemperatures()) // 首次温度同步采集，保证首帧快照即有温度
	snapshot.Store(collectStats())           // 先产出首个快照，前端连上即可展示
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go samplerLoop(ctx)     // 后台周期采样，与 HTTP 服务并行
	go temperatureLoop(ctx) // 后台低频刷新温度缓存
	go diskIOLoop(ctx)      // 磁盘吞吐独立刷新，失败时保留上一帧
	go portLoop(ctx)        // 系统连接表较重，独立低频刷新
	go processLoop(ctx)     // 进程排行独立刷新，避免阻塞整体快照

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stats", handleStats)   // JSON 快照接口（调试用）
	mux.HandleFunc("/api/stream", handleStream) // SSE 实时推送接口
	mux.HandleFunc("/api/slideshow/manifest", handleSlideshowManifest)
	mux.HandleFunc("/api/slideshow/image", handleSlideshowImage)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 只允许访问首页路径，其余一律 404
		if r.URL.Path != "/" && r.URL.Path != "/index.html" && r.URL.Path != "/dashboard.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(dashboardHTML)
	})

	// 默认监听所有网卡，允许手机等局域网设备访问；
	// 显式设置 MONITOR_LAN=0 可回退为仅本机可访问。
	lanMode := os.Getenv("MONITOR_LAN") != "0"
	bindHost := "0.0.0.0"
	if !lanMode {
		bindHost = "127.0.0.1"
	}
	addr := bindHost + ":" + port

	// 服务放到协程里启动，主协程保持阻塞便于 Ctrl+C 退出
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("监听失败：%v", err)
		}
	}()

	fmt.Printf("监控服务已启动：http://127.0.0.1:%s/\n", port)
	if lanMode {
		if ip := lanIP(); ip != "" {
			fmt.Printf("手机/局域网设备访问：http://%s:%s/\n", ip, port)
		} else {
			fmt.Printf("未能获取局域网地址，请手动使用本机 IP 加端口 %s 访问。\n", port)
		}
		fmt.Println("提示：局域网内其他设备也能访问该页面。")
		startDiscovery(port) // 局域网模式才开发现服务，本机模式无意义
	}
	fmt.Println("按 Ctrl+C 停止。")

	// 延迟 1 秒再开浏览器，等服务真正监听就绪
	//if os.Getenv("MONITOR_NO_BROWSER") != "1" {
	//	go func() {
	//		time.Sleep(time.Second)
	//		openBrowser("http://127.0.0.1:" + port + "/")
	//	}()
	//}

	// 桌面窗口模式：gio 要求主协程运行 app.Main（窗口事件泵），
	// 审批弹窗、监控大屏都在窗口里。
	// Windows 上 app.Main() 内部是 select{} 永不返回，因此窗口的开/关/唤回
	// 由下面的协程循环管理：点关闭 → 缩到系统托盘继续运行（网页端/平板端不断线）；
	// 托盘右键「退出」→ 进程结束。其他平台无托盘支持，关窗即退出。
	if guiEnabled() {
		if traySupported {
			go startTray() // 托盘消息循环不能占用 gio 主线程
		}
		go func() {
			for {
				if runGUI() || !traySupported {
					os.Exit(0) // 托盘退出/窗口异常/无托盘平台关窗
				}
				select {
				case <-trayQuitCh:
					os.Exit(0)
				case <-trayShowCh:
					// 托盘「打开监控界面」：重建窗口，gio 事件泵仍在主协程
				}
			}
		}()
		appMain()
		return
	}

	// 无界面模式：阻塞直到中断（设备接入自动放行）
	select {}
}
