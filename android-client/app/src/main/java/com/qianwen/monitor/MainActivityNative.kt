package com.qianwen.monitor

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.content.IntentFilter
import android.graphics.Color
import android.os.BatteryManager
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.view.MotionEvent
import android.view.View
import android.view.WindowManager
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ListView
import android.widget.ProgressBar
import android.widget.RadioButton
import android.widget.TextView
import java.io.File
import java.text.SimpleDateFormat
import java.util.Calendar
import java.util.Date
import java.util.Locale
import org.json.JSONArray
import org.json.JSONObject

/**
 * 电脑监控手机端：使用原生 UI 显示监控数据
 * 零第三方依赖，仅系统 API，兼容 Android 5.0（API 21）到最新版本
 */
class MainActivityNative : Activity() {

    private lateinit var btnApps: TextView
    private lateinit var btnScan: TextView
    private lateinit var tvClock: TextView
    private lateinit var tvCalDate: TextView
    private lateinit var tvCalLunar: TextView
    private lateinit var tvStatus: TextView
    private lateinit var tvTabletStats: TextView
    private lateinit var dotStatus: View
    private lateinit var tvCpuPercent: TextView
    private lateinit var progressCpu: ProgressBar
    private lateinit var tvCpuCores: TextView
    private lateinit var layoutCores: LinearLayout
    private lateinit var tvMemPercent: TextView
    private lateinit var progressMem: ProgressBar
    private lateinit var tvMemDetail: TextView
    private lateinit var tvNetDown: TextView
    private lateinit var tvNetUp: TextView
    private lateinit var tvNetTotal: TextView
    private lateinit var layoutDisks: LinearLayout
    private lateinit var tvTempCpu: TextView
    private lateinit var tvTempGpu: TextView
    private lateinit var layoutTempDisks: LinearLayout
    private lateinit var tvGpuName: TextView
    private lateinit var tvGpuUtil: TextView
    private lateinit var progressGpu: ProgressBar
    private lateinit var tvGpuMem: TextView
    private lateinit var layoutProcesses: LinearLayout
    private lateinit var thCpu: TextView
    private lateinit var thMem: TextView
    private lateinit var thRss: TextView
    private lateinit var btnViewGrouped: TextView
    private lateinit var btnViewSingle: TextView

    private var serverUrl = ""
    private val handler = Handler(Looper.getMainLooper())
    private val sseClient by lazy {
        MonitorSseClient(
            onData = { json -> handler.post { updateUI(json) } },
            onStatus = { status -> handler.post { updateStatus(status) } }
        )
    }

    private val prefs by lazy { getSharedPreferences("monitor", MODE_PRIVATE) }

    private val timeFormat = SimpleDateFormat("HH:mm:ss", Locale.getDefault())
    private val dateFormat = SimpleDateFormat("yyyy年M月d日 EEEE", Locale.CHINA)
    private var lastDateKey = ""
    private var clockFitted = false
    private var connectionStatus = "未连接"
    private var lastTabletCpuTotal = 0L
    private var lastTabletCpuIdle = 0L

    /** 时钟占可用宽度的比例，可在设置菜单调整并保存到本地 */
    private var clockRatio = CLOCK_RATIO_DEFAULT
        set(value) {
            val v = value.coerceIn(0.3f, 1.0f)
            if (field == v) return
            field = v
            prefs.edit().putFloat(KEY_CLOCK_RATIO, v).apply()
            applyClockRatio()
        }

    /** 按比例重设时钟字号；布局未就绪时由 clockTick 下个周期补做 */
    private fun applyClockRatio() {
        clockFitted = false
    }

    /** CPU 告警阈值（百分比），可在设置菜单调整并保存到本地 */
    private var cpuAlarmThreshold = CPU_ALARM_DEFAULT
        set(value) {
            val v = value.coerceIn(1, 100)
            if (field == v) return
            field = v
            prefs.edit().putInt(KEY_CPU_ALARM, v).apply()
            applyCpuAlarm(lastCpuPercent) // 改阈值后立即重判当前值
        }

    private lateinit var cpuAlarm: View
    private var lastCpuPercent = 0.0
    private var alarmBlinking = false
    private val alarmBlink = object : Runnable {
        override fun run() {
            // 红框在透明与可见间交替，形成闪烁；退出告警时停在透明
            val on = cpuAlarm.alpha < 0.5f
            cpuAlarm.alpha = if (on) 1f else 0f
            handler.postDelayed(this, 500)
        }
    }

    /** 按阈值切换 CPU 卡片红框闪烁状态 */
    private fun applyCpuAlarm(percent: Double) {
        lastCpuPercent = percent
        val shouldBlink = percent > cpuAlarmThreshold
        if (shouldBlink && !alarmBlinking) {
            alarmBlinking = true
            handler.post(alarmBlink)
        } else if (!shouldBlink && alarmBlinking) {
            alarmBlinking = false
            handler.removeCallbacks(alarmBlink)
            cpuAlarm.alpha = 0f
        }
    }

    /**
     * 把时钟字号缩放到占可用宽度的 clockRatio（居中显示，两侧留边）：
     * 时间恒为 8 字符等宽，用临时字号量出宽度后按比例换算目标字号。
     */
    private fun fitClockSize() {
        val avail = tvClock.width - tvClock.paddingLeft - tvClock.paddingRight
        if (avail <= 0) return // 布局尚未完成，下个 tick 再试
        val targetW = avail * clockRatio
        val probe = 100f // 临时基准字号（px），按比例换算与基准无关
        val paint = tvClock.paint
        val base = paint.textSize
        paint.textSize = probe
        // 等宽字体下用最长数字串量宽；再补上字距（letterSpacing 按 em 计）
        val textW = paint.measureText("88:88:88") + probe * 0.04f * 7
        paint.textSize = base
        val target = (probe * targetW / textW).coerceAtMost(probe * 2.4f)
        tvClock.setTextSize(android.util.TypedValue.COMPLEX_UNIT_PX, target)
        clockFitted = true
    }

    private val clockTick = object : Runnable {
        override fun run() {
            val now = Date()
            tvClock.text = timeFormat.format(now)
            renderConnectionStatus()
            updateTabletStats()
            if (!clockFitted) fitClockSize() // 布局就绪后按 60% 宽度定字号，只执行一次
            // 日期与农历一天才变一次，跨天时才重新计算
            val dayKey = dateFormat.format(now)
            if (dayKey != lastDateKey) {
                lastDateKey = dayKey
                tvCalDate.text = dayKey
                val lunar = Calendar.getInstance().let { LunarCalendar.solarToLunar(it) }
                tvCalLunar.text = lunar?.line ?: "农历数据超出范围"
            }
            handler.postDelayed(this, 1000)
        }
    }

    /** 缓存开启时持续检查剩余空间，跌破 200 MiB 立即关闭缓存。 */
    private val slideshowCacheSpaceGuard = object : Runnable {
        override fun run() {
            if (SlideshowSettings.disableCacheIfLow(this@MainActivityNative)) {
                updateStatus("存储空间少于 200MB，已自动关闭幻灯片缓存")
            }
            handler.postDelayed(this, CACHE_SPACE_CHECK_INTERVAL_MS)
        }
    }

    /** 没有触摸达到设定时间后进入幻灯片；每次触摸都从头计时。 */
    private val slideshowIdleTimer = Runnable {
        if (serverUrl.isNotEmpty() && !isFinishing && SlideshowSettings.load(this).enabled) {
            startActivity(Intent(this, SlideshowActivity::class.java).putExtra(
                SlideshowActivity.EXTRA_SERVER_URL,
                serverUrl
            ))
        }
    }

    private fun resetSlideshowIdleTimer() {
        handler.removeCallbacks(slideshowIdleTimer)
        val config = SlideshowSettings.load(this)
        if (serverUrl.isNotEmpty() && config.enabled) {
            handler.postDelayed(
                slideshowIdleTimer,
                config.idleSeconds * 1000L
            )
        }
    }

    override fun dispatchTouchEvent(event: MotionEvent): Boolean {
        resetSlideshowIdleTimer()
        return super.dispatchTouchEvent(event)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main_native)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)

        initViews()
        setupListeners()

        clockRatio = prefs.getFloat(KEY_CLOCK_RATIO, CLOCK_RATIO_DEFAULT).coerceIn(0.3f, 1.0f)
        cpuAlarmThreshold = prefs.getInt(KEY_CPU_ALARM, CPU_ALARM_DEFAULT)

        serverUrl = prefs.getString(KEY_URL, "") ?: ""
        if (serverUrl.isEmpty()) {
            showAddressDialog(true)
        } else {
            startSSE()
        }
    }

    private fun initViews() {
        btnApps = findViewById(R.id.btn_apps)
        btnScan = findViewById(R.id.btn_scan)
        tvClock = findViewById(R.id.tv_clock)
        tvCalDate = findViewById(R.id.tv_cal_date)
        tvCalLunar = findViewById(R.id.tv_cal_lunar)
        tvStatus = findViewById(R.id.tv_status)
        tvTabletStats = findViewById(R.id.tv_tablet_stats)
        dotStatus = findViewById(R.id.dot_status)
        tvCpuPercent = findViewById(R.id.tv_cpu_percent)
        progressCpu = findViewById(R.id.progress_cpu)
        tvCpuCores = findViewById(R.id.tv_cpu_cores)
        layoutCores = findViewById(R.id.layout_cores)
        tvMemPercent = findViewById(R.id.tv_mem_percent)
        progressMem = findViewById(R.id.progress_mem)
        tvMemDetail = findViewById(R.id.tv_mem_detail)
        tvNetDown = findViewById(R.id.tv_net_down)
        tvNetUp = findViewById(R.id.tv_net_up)
        tvNetTotal = findViewById(R.id.tv_net_total)
        layoutDisks = findViewById(R.id.layout_disks)
        tvTempCpu = findViewById(R.id.tv_temp_cpu)
        tvTempGpu = findViewById(R.id.tv_temp_gpu)
        layoutTempDisks = findViewById(R.id.layout_temp_disks)
        tvGpuName = findViewById(R.id.tv_gpu_name)
        tvGpuUtil = findViewById(R.id.tv_gpu_util)
        progressGpu = findViewById(R.id.progress_gpu)
        tvGpuMem = findViewById(R.id.tv_gpu_mem)
        layoutProcesses = findViewById(R.id.layout_processes)
        thCpu = findViewById(R.id.th_cpu)
        thMem = findViewById(R.id.th_mem)
        thRss = findViewById(R.id.th_rss)
        btnViewGrouped = findViewById(R.id.btn_view_grouped)
        btnViewSingle = findViewById(R.id.btn_view_single)
        cpuAlarm = findViewById(R.id.cpu_alarm)
    }

    private fun setupListeners() {
        findViewById<View>(R.id.btn_settings).setOnClickListener { showSettingsMenu() }
        btnScan.setOnClickListener { startScan() }
        btnApps.setOnClickListener { showAppsDialog() }

        // 表头点击排序（与网页端一致：点击切换排序列，降序）
        thCpu.setOnClickListener { sortKey = 0; renderProcs() }
        thMem.setOnClickListener { sortKey = 1; renderProcs() }
        thRss.setOnClickListener { sortKey = 2; renderProcs() }

        // 视图切换：合并视图 / 单进程视图
        btnViewGrouped.setOnClickListener { setProcView("grouped") }
        btnViewSingle.setOnClickListener { setProcView("single") }
    }

    private fun setProcView(v: String) {
        if (procView == v) return
        procView = v
        btnViewGrouped.setBackgroundResource(if (v == "grouped") R.drawable.btn_outline else R.drawable.btn_round)
        btnViewGrouped.setTextColor(resources.getColor(if (v == "grouped") R.color.accent else R.color.muted))
        btnViewSingle.setBackgroundResource(if (v == "single") R.drawable.btn_outline else R.drawable.btn_round)
        btnViewSingle.setTextColor(resources.getColor(if (v == "single") R.color.accent else R.color.muted))
        renderProcs() // 立即用缓存数据重排，无需等新帧
    }

    private var isScanning = false
    private val foundResults = ArrayList<Discovery.DiscoveryResult>()

    /** 局域网自动发现：UDP 广播探测监控服务，找到后弹框一键连接 */
    private fun startScan() {
        if (isScanning) return
        isScanning = true
        foundResults.clear()
        btnScan.text = "扫描中..."
        btnScan.isEnabled = false
        updateStatus("正在扫描局域网…")

        Discovery(
            onFound = { result ->
                runOnUiThread {
                    foundResults.add(result)
                    if (foundResults.size == 1) showConnectDialog() // 首台立即询问，后续可再次点扫描查看
                }
            },
            onComplete = {
                runOnUiThread {
                    isScanning = false
                    btnScan.isEnabled = true
                    btnScan.text = "扫描"
                    if (foundResults.isEmpty()) {
                        updateStatus("未发现监控服务，请确认电脑已启动 monitor 且与本机在同一 Wi-Fi")
                    } else if (foundResults.size > 1) {
                        showConnectDialog()
                    }
                }
            }
        ).scan(applicationContext)
    }

    private fun showConnectDialog() {
        val items = foundResults.map { "${it.host}  http://${it.ip}:${it.port}" }.toTypedArray()
        AlertDialog.Builder(this)
            .setTitle("发现 ${foundResults.size} 台监控服务")
            .setItems(items) { _, which ->
                serverUrl = "http://${foundResults[which].ip}:${foundResults[which].port}"
                prefs.edit().putString(KEY_URL, serverUrl).apply()
                stopSSE()
                startSSE()
            }
            .setNegativeButton("取消", null)
            .show()
    }

    private fun startSSE() {
        sseClient.start(serverUrl)
    }

    private fun stopSSE() {
        sseClient.stop()
    }

    private fun updateUI(json: JSONObject) {
        updateStatus("已连接")

        // CPU
        val cpu = json.optJSONObject("cpu")
        if (cpu != null) {
            val cpuPercent = cpu.optDouble("percent", 0.0)
            val cores = cpu.optInt("cores", 0)
            tvCpuPercent.text = String.format("%.1f%%", cpuPercent)
            progressCpu.progress = cpuPercent.toInt()
            tvCpuCores.text = "$cores 核心"
            renderCoreBars(cpu.optJSONArray("per_core"))
            applyCpuAlarm(cpuPercent)
        }

        // 内存
        val memory = json.optJSONObject("memory")
        if (memory != null) {
            val memPercent = memory.optDouble("percent", 0.0)
            val used = memory.optLong("used", 0)
            val total = memory.optLong("total", 0)
            tvMemPercent.text = String.format("%.1f%%", memPercent)
            progressMem.progress = memPercent.toInt()
            tvMemDetail.text = "${formatBytes(used)} / ${formatBytes(total)}"
        }

        // 网络
        val network = json.optJSONObject("network")
        if (network != null) {
            val recvRate = network.optDouble("recv_rate", 0.0)
            val sentRate = network.optDouble("sent_rate", 0.0)
            val bytesRecv = network.optLong("bytes_recv", 0)
            val bytesSent = network.optLong("bytes_sent", 0)
            tvNetDown.text = "${formatBytes(recvRate.toLong())}/s"
            tvNetUp.text = "${formatBytes(sentRate.toLong())}/s"
            tvNetTotal.text = "本次开机累计 ↓${formatBytes(bytesRecv)}  ↑${formatBytes(bytesSent)}"
        }

        // 磁盘
        val disks = json.optJSONArray("disks")
        layoutDisks.removeAllViews()
        val diskIO = json.optJSONObject("disk_io")
        if (diskIO != null) {
            val busy = if (diskIO.optBoolean("busy_avail", false)) {
                String.format("%.1f%%", diskIO.optDouble("busy_percent", 0.0))
            } else "N/A"
            val ioSummary = TextView(this).apply {
                text = "实时 读 ${formatBytes(diskIO.optLong("read_rate", 0))}/s  " +
                    "写 ${formatBytes(diskIO.optLong("write_rate", 0))}/s  " +
                    "忙碌 $busy"
                setTextColor(Color.parseColor("#8FB7CC"))
                textSize = 11f
                setPadding(0, 0, 0, 6)
            }
            layoutDisks.addView(ioSummary)
        }
        if (disks != null && disks.length() > 0) {
            for (i in 0 until disks.length()) {
                val disk = disks.optJSONObject(i)
                val mount = disk.optString("mount", "")
                val percent = disk.optDouble("percent", 0.0)
                val used = disk.optLong("used", 0)
                val total = disk.optLong("total", 0)

                val diskView = LinearLayout(this).apply {
                    orientation = LinearLayout.VERTICAL
                    setPadding(0, 8, 0, 8)
                }

                val header = TextView(this).apply {
                    text = "$mount  ${String.format("%.1f%%", percent)}"
                    setTextColor(Color.parseColor("#8FB7CC"))
                    textSize = 12f
                }

                val progress = ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal).apply {
                    max = 100
                    progress = percent.toInt()
                    progressDrawable = resources.getDrawable(R.drawable.progress_bar)
                    layoutParams = LinearLayout.LayoutParams(
                        LinearLayout.LayoutParams.MATCH_PARENT,
                        8
                    ).apply { topMargin = 4 }
                }

                val detail = TextView(this).apply {
                    text = "${formatBytes(used)} / ${formatBytes(total)}"
                    setTextColor(Color.parseColor("#8FB7CC"))
                    textSize = 11f
                }

                diskView.addView(header)
                diskView.addView(progress)
                diskView.addView(detail)
                layoutDisks.addView(diskView)
            }
        }

        // 温度
        val temperature = json.optJSONObject("temperature")
        if (temperature != null) {
            val cpuTemp = temperature.optDouble("cpu_temp", 0.0)
            val gpuTemp = temperature.optDouble("gpu_temp", 0.0)
            tvTempCpu.text = if (cpuTemp > 0) String.format("%.1f°C", cpuTemp) else "N/A"
            tvTempGpu.text = if (gpuTemp > 0) String.format("%.1f°C", gpuTemp) else "N/A"
            tvTempCpu.setTextColor(tempColor(cpuTemp))
            tvTempGpu.setTextColor(tempColor(gpuTemp))

            // 磁盘温度：SMART 枚举需要管理员权限，读不到时给出提示
            layoutTempDisks.removeAllViews()
            val disksArr = temperature.optJSONArray("disks")
            if (disksArr != null && disksArr.length() > 0) {
                for (i in 0 until disksArr.length()) {
                    val d = disksArr.optJSONObject(i) ?: continue
                    val avail = d.optBoolean("avail", false)
                    val valStr = if (avail) String.format("%.1f°C", d.optDouble("temp", 0.0)) else "N/A"
                    val row = TextView(this).apply {
                        textSize = 11f
                        setPadding(0, 4, 0, 4)
                        text = "${d.optString("name", "")}  $valStr"
                        setTextColor(if (avail) Color.parseColor("#E6EBF5") else Color.parseColor("#8FB7CC"))
                    }
                    layoutTempDisks.addView(row)
                }
            } else {
                val tip = TextView(this).apply {
                    textSize = 10f
                    setPadding(0, 4, 0, 0)
                    text = "磁盘温度需以管理员身份运行"
                    setTextColor(Color.parseColor("#8FB7CC"))
                }
                layoutTempDisks.addView(tip)
            }
        }

        // GPU 数据（使用率、显存、温度、型号）
        if (temperature != null) {
            val gpuName = temperature.optString("gpu_name", "")
            tvGpuName.text = if (gpuName.isNotEmpty()) gpuName else "N/A"

            val gpuUtilAvail = temperature.optBoolean("gpu_util_avail", false)
            val gpuUtil = temperature.optDouble("gpu_util", 0.0)
            tvGpuUtil.text = if (gpuUtilAvail) String.format("%.0f%%", gpuUtil) else "N/A"
            progressGpu.progress = if (gpuUtilAvail) gpuUtil.toInt().coerceIn(0, 100) else 0

            val gpuMemUsedAvail = temperature.optBoolean("gpu_mem_used_avail", false)
            val gpuMemUsed = temperature.optLong("gpu_mem_used", 0L)
            val gpuMemTotal = temperature.optLong("gpu_mem_total", 0L)
            tvGpuMem.text = if (gpuMemUsedAvail && gpuMemTotal > 0) {
                "${formatBytes(gpuMemUsed)} / ${formatBytes(gpuMemTotal)}"
            } else {
                "N/A"
            }
        } else {
            tvGpuName.text = "N/A"
            tvGpuUtil.text = "N/A"
            progressGpu.progress = 0
            tvGpuMem.text = "N/A"
        }

        // 进程：缓存两视图数据，按当前视图与排序渲染（对齐网页端交互）
        json.optJSONArray("processes")?.let { procGrouped = parseProcs(it) }
        json.optJSONArray("processes_single")?.let { procSingle = parseProcs(it) }
        renderProcs()
    }

    /** 一条进程记录（与网页端字段一致） */
    private data class ProcRow(
        val pid: Int,
        val name: String,
        val cpu: Double,
        val memPct: Double,
        val rss: Long,
        val count: Int,
        val ports: String // 已格式化的端口文本，无端口为空串
    )

    private fun parseProcs(arr: JSONArray): List<ProcRow> {
        val list = ArrayList<ProcRow>(arr.length())
        for (i in 0 until arr.length()) {
            val proc = arr.optJSONObject(i) ?: continue
            val ports = proc.optJSONArray("ports")
            var portText = ""
            if (ports != null && ports.length() > 0) {
                val shown = minOf(ports.length(), 3)
                val sb = StringBuilder()
                for (j in 0 until shown) {
                    if (j > 0) sb.append(",")
                    sb.append(ports.optInt(j))
                }
                if (ports.length() > shown) sb.append(" +").append(ports.length() - shown)
                portText = sb.toString()
            }
            list.add(
                ProcRow(
                    pid = proc.optInt("pid", 0),
                    name = proc.optString("name", ""),
                    cpu = proc.optDouble("cpu", 0.0),
                    memPct = proc.optDouble("mem_percent", 0.0),
                    rss = proc.optLong("mem_rss", 0),
                    count = proc.optInt("count", 1),
                    ports = portText
                )
            )
        }
        return list
    }

    // 当前视图与排序状态（sortKey：0=CPU 1=内存% 2=内存占用）
    private var procGrouped: List<ProcRow> = emptyList()
    private var procSingle: List<ProcRow> = emptyList()
    private var procView = "grouped"
    private var sortKey = 0
    private val procLimit = 15 // 与网页端排行榜条数一致

    private fun renderProcs() {
        val src = if (procView == "grouped") procGrouped else procSingle
        val sorted = src.sortedByDescending { p ->
            when (sortKey) {
                1 -> p.memPct
                2 -> p.rss.toDouble()
                else -> p.cpu
            }
        }.take(procLimit)
        layoutProcesses.removeAllViews()
        for (p in sorted) {
            layoutProcesses.addView(buildProcRow(p))
        }
        updateSortHeaders()
    }

    /** 单行进程：六列与表头同权重，保证列对齐 */
    private fun buildProcRow(p: ProcRow): View {
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(0, dp(5), 0, dp(5))
        }
        row.addView(cellTextView(p.pid.toString(), R.color.muted, 1.0f, android.view.Gravity.START))
        val nameView = cellTextView(
            if (p.count > 1) "${p.name} ×${p.count}" else p.name,
            R.color.text, 2.4f, android.view.Gravity.START
        )
        nameView.setSingleLine(true)
        row.addView(nameView)
        // CPU 数值按档位着色，对齐网页端 pillColor（≥50 红、≥20 黄、其余绿）
        val cpuColor = when {
            p.cpu >= 50 -> R.color.hud_red
            p.cpu >= 20 -> R.color.hud_yellow
            else -> R.color.hud_green
        }
        row.addView(cellTextView(String.format("%.1f", p.cpu), cpuColor, 1.2f, android.view.Gravity.END))
        row.addView(cellTextView(String.format("%.2f", p.memPct), R.color.muted, 1.1f, android.view.Gravity.END))
        row.addView(cellTextView(formatBytes(p.rss), R.color.muted, 1.4f, android.view.Gravity.END))
        row.addView(cellTextView(p.ports, R.color.hud_blue, 1.9f, android.view.Gravity.END))
        return row
    }

    private fun cellTextView(text: String, colorRes: Int, weight: Float, gravity: Int): TextView {
        return TextView(this).apply {
            this.text = text
            setTextColor(resources.getColor(colorRes))
            textSize = 11f
            typeface = android.graphics.Typeface.MONOSPACE
            maxLines = 1
            // 文字对齐作用在 TextView 自身，与表头列的 gravity 保持一致
            this.gravity = gravity
            layoutParams = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, weight)
        }
    }

    /** 表头排序箭头：当前排序列显示 ▼，其余清除 */
    private fun updateSortHeaders() {
        val heads = listOf(thCpu, thMem, thRss)
        val bases = listOf("CPU %", "内存 %", "内存占用")
        for (i in heads.indices) {
            val base = bases[i]
            heads[i].text = if (i == sortKey) "$base▼" else base
            heads[i].setTextColor(resources.getColor(if (i == sortKey) R.color.accent else R.color.muted))
        }
    }

    /** 每核竖条取色，与网页端 barColor 阈值一致：≥90 红、≥70 黄橙、其余青蓝绿 */
    private fun coreColors(p: Double): IntArray = when {
        p >= 90 -> intArrayOf(Color.parseColor("#FF4D5E"), Color.parseColor("#FF2D95"))
        p >= 70 -> intArrayOf(Color.parseColor("#FFC857"), Color.parseColor("#FF8C00"))
        else -> intArrayOf(Color.parseColor("#00E5FF"), Color.parseColor("#37FFB4"))
    }

    /**
     * 渲染每核使用率竖条（样式对齐网页端 .core）：
     * 外层 FrameLayout 作底槽（淡色描边），内层 View 靠底部对齐，
     * 高度按该核占用百分比起到，颜色随占用档位变化；
     * 核心数不变时只更新高度与颜色，不重建视图。
     */
    private fun renderCoreBars(perCore: JSONArray?) {
        val n = perCore?.length() ?: 0
        if (n == 0) {
            layoutCores.removeAllViews()
            return
        }
        if (layoutCores.childCount != n) {
            layoutCores.removeAllViews()
            val gap = dp(3)
            for (i in 0 until n) {
                val track = android.widget.FrameLayout(this).apply {
                    setBackgroundResource(R.drawable.core_track)
                }
                val fill = View(this).apply {
                    setBackgroundColor(Color.parseColor("#00E5FF"))
                }
                // 填充条贴底，高度由占用比例控制
                val fillLp = android.widget.FrameLayout.LayoutParams(
                    android.widget.FrameLayout.LayoutParams.MATCH_PARENT, 0
                ).apply { gravity = android.view.Gravity.BOTTOM }
                track.addView(fill, fillLp)

                val lp = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.MATCH_PARENT, 1f)
                if (i > 0) lp.marginStart = gap
                layoutCores.addView(track, lp)
            }
        }
        val trackH = dp(28) - dp(2) // 扣除底槽内边距
        for (i in 0 until n) {
            val track = layoutCores.getChildAt(i) as android.widget.FrameLayout
            val fill = track.getChildAt(0)
            val p = perCore!!.optDouble(i, 0.0)
            fill.setBackgroundColor(coreColors(p)[0])
            val h = (trackH * minOf(maxOf(p, 0.0), 100.0) / 100.0).toInt()
                .coerceAtLeast(if (p > 0.5) dp(2) else 0)
            fill.layoutParams.height = h
            fill.requestLayout()
            fill.contentDescription = "核心 ${i + 1}：${p.toInt()}%"
        }
    }

    private fun dp(v: Int): Int = (v * resources.displayMetrics.density).toInt()

    /** 温度告警取色：<55 青色正常，55-75 黄色警戒，>75 红色告警 */
    private fun tempColor(t: Double): Int = when {
        t <= 0 -> Color.parseColor("#8FB7CC")
        t < 55 -> Color.parseColor("#00E5FF")
        t < 75 -> Color.parseColor("#FFC857")
        else -> Color.parseColor("#FF4D5E")
    }

    /** 可启动应用条目：显示名 + 启动组件 + 桌面图标 */
    private data class LaunchableApp(
        val label: String,
        val pkg: String,
        val cls: String,
        val icon: android.graphics.drawable.Drawable?
    )

    private var launchableApps: List<LaunchableApp> = emptyList()

    /**
     * 弹出设备已安装应用列表（自定义 HUD 面板，靠左弹出）。
     * 想改为靠右显示，把 APPS_GRAVITY 换成 android.view.Gravity.END 即可。
     */
    private fun showAppsDialog() {
        launchableApps = loadLaunchableApps() // 每次打开重新枚举，保证新装/卸载即时生效
        if (launchableApps.isEmpty()) {
            updateStatus("未找到可启动的应用")
            return
        }
        val dialog = android.app.Dialog(this, R.style.HudDialog)
        dialog.setContentView(R.layout.dialog_apps)

        // 面板宽度取屏幕的 45%，靠左居中弹出
        dialog.findViewById<TextView>(R.id.tv_apps_title).text = "应用（${launchableApps.size}）"
        dialog.findViewById<View>(R.id.btn_apps_close).setOnClickListener { dialog.dismiss() }
        val list = dialog.findViewById<ListView>(R.id.lv_apps)
        list.adapter = object : android.widget.BaseAdapter() {
            override fun getCount() = launchableApps.size
            override fun getItem(position: Int) = launchableApps[position]
            override fun getItemId(position: Int) = position.toLong()
            override fun getView(position: Int, convertView: View?, parent: android.view.ViewGroup?): View {
                val row = convertView ?: android.view.LayoutInflater.from(this@MainActivityNative)
                    .inflate(R.layout.item_app, parent, false)
                val app = launchableApps[position]
                val iv = row.findViewById<android.widget.ImageView>(R.id.iv_app_icon)
                if (app.icon != null) iv.setImageDrawable(app.icon) else iv.setImageResource(R.drawable.ic_launcher)
                row.findViewById<TextView>(R.id.tv_app_name).text = app.label
                row.findViewById<TextView>(R.id.tv_app_pkg).text = app.pkg
                return row
            }
        }
        list.setOnItemClickListener { _, _, position, _ ->
            dialog.dismiss()
            launchApp(launchableApps[position])
        }
        dialog.window?.apply {
            setLayout(
                (resources.displayMetrics.widthPixels * 0.45f).toInt(),
                android.view.ViewGroup.LayoutParams.WRAP_CONTENT
            )
            setGravity(android.view.Gravity.START or android.view.Gravity.CENTER_VERTICAL)
            // 靠左弹出并留一点边距；改 Gravity.END 即可靠右
            val params = attributes
            params.x = dp(8)
            attributes = params
        }
        dialog.show()
    }

    /** 通过 LAUNCHER 意图枚举桌面可启动应用（含图标），按名称排序 */
    private fun loadLaunchableApps(): List<LaunchableApp> {
        val pm = packageManager
        val intent = android.content.Intent(android.content.Intent.ACTION_MAIN)
            .addCategory(android.content.Intent.CATEGORY_LAUNCHER)
        val resolved = pm.queryIntentActivities(intent, 0)
        return resolved.mapNotNull { info ->
            val name = info.loadLabel(pm)?.toString() ?: return@mapNotNull null
            LaunchableApp(
                label = name,
                pkg = info.activityInfo.packageName,
                cls = info.activityInfo.name,
                icon = try { info.loadIcon(pm) } catch (e: Exception) { null }
            )
        }.distinctBy { it.pkg to it.cls }
            .sortedBy { it.label.lowercase() }
    }

    /** 启动指定应用；失败时给出提示 */
    private fun launchApp(app: LaunchableApp) {
        try {
            val intent = android.content.Intent(android.content.Intent.ACTION_MAIN)
                .addCategory(android.content.Intent.CATEGORY_LAUNCHER)
                .setClassName(app.pkg, app.cls)
                .addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK)
            startActivity(intent)
        } catch (e: Exception) {
            updateStatus("无法启动 ${app.label}: ${e.message}")
        }
    }

    /** ⚙ 设置菜单：显示、告警、幻灯片与服务地址 */
    private fun showSettingsMenu() {
        val slideshow = SlideshowSettings.load(this)
        val items = arrayOf(
            "时钟宽度（当前 ${Math.round(clockRatio * 100)}%）",
            "CPU 告警阈值（当前 $cpuAlarmThreshold%）",
            "幻灯片设置（${if (slideshow.enabled) "已开启" else "已关闭"}）",
            "幻灯片列表",
            "服务地址"
        )
        AlertDialog.Builder(this)
            .setTitle("设置")
            .setItems(items) { _, which ->
                when (which) {
                    0 -> showClockRatioDialog()
                    1 -> showCpuAlarmDialog()
                    2 -> showSlideshowSettingsDialog()
                    3 -> showSlideshowList()
                    4 -> showAddressDialog(false)
                }
            }
            .setNegativeButton("取消", null)
            .show()
    }

    private fun showSlideshowList() {
        if (serverUrl.isEmpty()) {
            updateStatus("请先设置电脑服务地址")
            return
        }
        startActivity(Intent(this, SlideshowListActivity::class.java).putExtra(
            SlideshowListActivity.EXTRA_SERVER_URL,
            serverUrl
        ))
    }

    private fun showSlideshowSettingsDialog() {
        val config = SlideshowSettings.load(this)
        val content = layoutInflater.inflate(R.layout.dialog_slideshow_settings, null)
        content.isFocusableInTouchMode = true
        content.requestFocus() // 打开时不自动弹出数字键盘，点输入框后再显示
        val enabledCheck = content.findViewById<CheckBox>(R.id.check_slideshow_enabled)
        val idleInput = content.findViewById<EditText>(R.id.input_slideshow_idle_seconds)
        val slideInput = content.findViewById<EditText>(R.id.input_slide_seconds)
        val fitRadio = content.findViewById<RadioButton>(R.id.radio_slideshow_fit)
        val fillRadio = content.findViewById<RadioButton>(R.id.radio_slideshow_fill)
        val cacheCheck = content.findViewById<CheckBox>(R.id.check_slideshow_cache)
        val clockCheck = content.findViewById<CheckBox>(R.id.check_slideshow_clock)
        val moveClockCheck = content.findViewById<CheckBox>(R.id.check_slideshow_move_clock)
        val thumbnailsCheck = content.findViewById<CheckBox>(R.id.check_slideshow_thumbnails)
        val storageText = content.findViewById<TextView>(R.id.text_slideshow_storage)

        enabledCheck.isChecked = config.enabled
        idleInput.setText(config.idleSeconds.toString())
        slideInput.setText(config.slideSeconds.toString())
        if (config.displayMode == SlideshowSettings.MODE_FILL) fillRadio.isChecked = true
        else fitRadio.isChecked = true
        cacheCheck.isChecked = config.cacheEnabled
        clockCheck.isChecked = config.showClock
        moveClockCheck.isChecked = config.moveClock
        moveClockCheck.isEnabled = config.showClock
        clockCheck.setOnCheckedChangeListener { _, checked -> moveClockCheck.isEnabled = checked }
        thumbnailsCheck.isChecked = config.showThumbnails

        fun refreshStorageMessage(error: Boolean = false) {
            val free = SlideshowSettings.availableCacheBytes(this)
            storageText.text = "缓存分区可用 ${formatBytes(free)}；少于 200MB 时自动关闭缓存。"
            storageText.setTextColor(Color.parseColor(if (error) "#FF4D5E" else "#8FB7CC"))
        }
        refreshStorageMessage()
        cacheCheck.setOnCheckedChangeListener { _, checked ->
            if (checked && SlideshowSettings.availableCacheBytes(this) < SlideshowSettings.MIN_CACHE_FREE_BYTES) {
                cacheCheck.isChecked = false
                refreshStorageMessage(true)
            } else {
                refreshStorageMessage()
            }
        }

        val dialog = AlertDialog.Builder(this)
            .setTitle("幻灯片设置")
            .setView(content)
            .setPositiveButton("保存", null)
            .setNegativeButton("取消", null)
            .create()
        dialog.setOnShowListener {
            dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener {
                val idleSeconds = idleInput.text.toString().toIntOrNull()
                val slideSeconds = slideInput.text.toString().toIntOrNull()
                when {
                    idleSeconds == null || idleSeconds !in 1..86400 -> {
                        idleInput.error = "请输入 1–86400 秒"
                    }
                    slideSeconds == null || slideSeconds !in 1..3600 -> {
                        slideInput.error = "请输入 1–3600 秒"
                    }
                    cacheCheck.isChecked &&
                        SlideshowSettings.availableCacheBytes(this) < SlideshowSettings.MIN_CACHE_FREE_BYTES -> {
                        cacheCheck.isChecked = false
                        refreshStorageMessage(true)
                    }
                    else -> {
                        SlideshowSettings.save(
                            this,
                            SlideshowConfig(
                                enabled = enabledCheck.isChecked,
                                idleSeconds = idleSeconds,
                                slideSeconds = slideSeconds,
                                displayMode = if (fillRadio.isChecked) {
                                    SlideshowSettings.MODE_FILL
                                } else {
                                    SlideshowSettings.MODE_FIT
                                },
                                cacheEnabled = cacheCheck.isChecked,
                                showClock = clockCheck.isChecked,
                                moveClock = moveClockCheck.isChecked,
                                showThumbnails = thumbnailsCheck.isChecked
                            )
                        )
                        dialog.dismiss()
                        updateStatus("幻灯片设置已保存")
                        resetSlideshowIdleTimer()
                    }
                }
            }
        }
        dialog.show()
    }

    /** 输入 CPU 告警阈值（百分比），保存到本地并立即重判闪烁状态 */
    private fun showCpuAlarmDialog() {
        val input = EditText(this).apply {
            setInputType(android.text.InputType.TYPE_CLASS_NUMBER)
            setText(cpuAlarmThreshold.toString())
            setTextColor(Color.WHITE)
            setHintTextColor(Color.GRAY)
        }
        AlertDialog.Builder(this)
            .setTitle("CPU 告警阈值（1–100）")
            .setMessage("CPU 使用率超过该百分比时，CPU 卡片红框闪烁提醒。默认 20。")
            .setView(input)
            .setPositiveButton("保存") { _, _ ->
                val v = input.text.toString().toIntOrNull()
                if (v != null && v in 1..100) {
                    cpuAlarmThreshold = v
                } else {
                    updateStatus("告警阈值需为 1–100 之间的整数")
                }
            }
            .setNegativeButton("取消", null)
            .show()
    }

    /** 输入时钟占屏幕宽度的百分比，保存到本地并立即生效 */
    private fun showClockRatioDialog() {
        val input = EditText(this).apply {
            setInputType(android.text.InputType.TYPE_CLASS_NUMBER)
            setText(Math.round(clockRatio * 100).toString())
            setTextColor(Color.WHITE)
            setHintTextColor(Color.GRAY)
        }
        AlertDialog.Builder(this)
            .setTitle("时钟宽度（30–100）")
            .setMessage("时间大字占屏幕宽度的百分比，数值越大字越大。")
            .setView(input)
            .setPositiveButton("保存") { _, _ ->
                val pct = input.text.toString().toIntOrNull()
                if (pct != null && pct in 30..100) {
                    clockRatio = pct / 100f
                } else {
                    updateStatus("时钟宽度需为 30–100 之间的整数")
                }
            }
            .setNegativeButton("取消", null)
            .show()
    }

    private fun updateStatus(status: String) {
        connectionStatus = status
        renderConnectionStatus()
        // 指示灯：已连接绿色 / 进行中黄色 / 失败红色
        val dotColor = when {
            status.startsWith("已连接") -> Color.parseColor("#37FFB4")
            status.startsWith("连接失败") || status.startsWith("未发现") -> Color.parseColor("#FF4D5E")
            else -> Color.parseColor("#FFC857")
        }
        dotStatus.background?.mutate()?.setColorFilter(dotColor, android.graphics.PorterDuff.Mode.SRC_IN)
    }

    /** 已连接时附带平板自本次开机以来的运行时长（elapsedRealtime 包含休眠时间）。 */
    private fun renderConnectionStatus() {
        tvStatus.text = if (connectionStatus.startsWith("已连接")) {
            "$connectionStatus · 开机 ${formatDeviceUptime(SystemClock.elapsedRealtime())}"
        } else {
            connectionStatus
        }
    }

    private fun formatDeviceUptime(elapsedMillis: Long): String {
        val totalSeconds = elapsedMillis / 1000L
        val days = totalSeconds / 86400L
        val hours = totalSeconds % 86400L / 3600L
        val minutes = totalSeconds % 3600L / 60L
        val seconds = totalSeconds % 60L
        val clock = String.format(Locale.ROOT, "%02d:%02d:%02d", hours, minutes, seconds)
        return if (days > 0L) "${days}天 $clock" else clock
    }

    /** 读取整台平板的 CPU 利用率与电池状态，保持在连接状态行右侧。 */
    private fun updateTabletStats() {
        val cpu = readTabletCpuPercent()?.let { "$it%" } ?: "--"
        val battery = registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val level = battery?.getIntExtra(BatteryManager.EXTRA_LEVEL, -1) ?: -1
        val scale = battery?.getIntExtra(BatteryManager.EXTRA_SCALE, 100) ?: 100
        val percent = if (level >= 0 && scale > 0) level * 100 / scale else -1
        val status = battery?.getIntExtra(BatteryManager.EXTRA_STATUS, -1) ?: -1
        val charging = status == BatteryManager.BATTERY_STATUS_CHARGING ||
            status == BatteryManager.BATTERY_STATUS_FULL
        val batteryText = if (percent >= 0) "$percent%${if (charging) " 充电" else ""}" else "--"
        tvTabletStats.text = "平板 CPU $cpu · 电量 $batteryText"
    }

    private fun readTabletCpuPercent(): Int? {
        return try {
            val line = File("/proc/stat").bufferedReader().use { it.readLine() } ?: return null
            val values = line.trim().split(Regex("\\s+")).drop(1).mapNotNull { it.toLongOrNull() }
            if (values.size < 4) return null
            val total = values.sum()
            val idle = values[3] + values.getOrElse(4) { 0L }
            val deltaTotal = total - lastTabletCpuTotal
            val deltaIdle = idle - lastTabletCpuIdle
            lastTabletCpuTotal = total
            lastTabletCpuIdle = idle
            if (deltaTotal <= 0L || deltaIdle < 0L) null
            else (((deltaTotal - deltaIdle) * 100L + deltaTotal / 2L) / deltaTotal)
                .toInt().coerceIn(0, 100)
        } catch (_: Exception) {
            null
        }
    }

    private fun formatBytes(bytes: Long): String {
        if (bytes < 1024) return "$bytes B"
        val kb = bytes / 1024.0
        if (kb < 1024) return String.format("%.1f KB", kb)
        val mb = kb / 1024.0
        if (mb < 1024) return String.format("%.1f MB", mb)
        val gb = mb / 1024.0
        return String.format("%.1f GB", gb)
    }

    private fun showAddressDialog(firstTime: Boolean) {
        val input = EditText(this)
        input.setText(serverUrl.ifEmpty { "http://192.168." })
        input.setHint("http://192.168.1.100:8765")
        input.setTextColor(Color.WHITE)
        input.setHintTextColor(Color.GRAY)

        val dialog = AlertDialog.Builder(this)
            .setTitle("监控服务地址")
            .setMessage("请在电脑上以「局域网模式」启动监控程序：\n\$env:MONITOR_LAN=\"1\"; .\\monitor.exe\n启动后终端会显示「手机访问地址」，手机与电脑需连接同一 Wi-Fi。")
            .setView(input)
            .setCancelable(!firstTime)
            .setPositiveButton("连接", null)
            .setNegativeButton("取消", null)
            .create()

        dialog.setOnShowListener {
            dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener {
                val raw = input.text.toString().trim().trimEnd('/')
                if (raw.matches(Regex("^https?://[A-Za-z0-9.-]+(:\\d{1,5})?(/.*)?$"))) {
                    serverUrl = raw
                    prefs.edit().putString(KEY_URL, raw).apply()
                    dialog.dismiss()
                    stopSSE()
                    startSSE()
                    resetSlideshowIdleTimer()
                } else {
                    input.error = "格式应为 http://地址:端口"
                }
            }
        }
        dialog.show()
    }

    override fun onResume() {
        super.onResume()
        lastTabletCpuTotal = 0L
        lastTabletCpuIdle = 0L
        handler.post(clockTick) // 启动秒级时钟
        handler.post(slideshowCacheSpaceGuard)
        resetSlideshowIdleTimer()
        if (serverUrl.isNotEmpty() && !sseClient.isRunning()) {
            startSSE()
        }
    }

    override fun onPause() {
        super.onPause()
        handler.removeCallbacks(clockTick) // 离开界面停止时钟
        handler.removeCallbacks(slideshowCacheSpaceGuard)
        handler.removeCallbacks(slideshowIdleTimer)
        stopSSE()
    }

    // 本应用注册了 HOME（桌面）角色：按系统返回键若正常 finish 会导致无桌面可回、屏幕变黑。
    // 与各家桌面 launcher 一致：直接吞掉返回事件。弹窗（可取消对话框）会自行消费返回键关闭自己，
    // 不会走到这里，因此无需额外判断。
    @Suppress("DEPRECATION")
    override fun onBackPressed() {
        // 故意不调用 super：桌面主界面不因返回键退出
    }

    override fun onDestroy() {
        super.onDestroy()
        stopSSE()
    }

    companion object {
        private const val KEY_URL = "server_url"
        private const val KEY_CLOCK_RATIO = "clock_ratio"
        private const val CLOCK_RATIO_DEFAULT = 0.6f // 时钟默认占可用宽度 60%
        private const val KEY_CPU_ALARM = "cpu_alarm_threshold"
        private const val CPU_ALARM_DEFAULT = 20      // CPU 告警阈值默认 20%
        private const val CACHE_SPACE_CHECK_INTERVAL_MS = 10_000L
    }
}
