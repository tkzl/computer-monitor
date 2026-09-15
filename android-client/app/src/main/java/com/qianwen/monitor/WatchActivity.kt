package com.qianwen.monitor

import android.app.Activity
import android.app.AlertDialog
import android.graphics.Color
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.WindowManager
import android.widget.Button
import android.widget.EditText
import android.widget.ProgressBar
import android.widget.TextView
import java.text.SimpleDateFormat
import java.util.Calendar
import java.util.Date
import java.util.Locale
import org.json.JSONObject

/**
 * 电脑监控手表端：为 240x240 小屏幕优化的监控界面
 * 只显示最关键的 CPU、内存、温度、网络数据
 */
class WatchActivity : Activity() {

    private lateinit var tvClock: TextView
    private lateinit var tvCalLunar: TextView
    private lateinit var tvStatus: TextView
    private lateinit var tvCpuPercent: TextView
    private lateinit var progressCpu: ProgressBar
    private lateinit var tvMemPercent: TextView
    private lateinit var progressMem: ProgressBar
    private lateinit var tvMemDetail: TextView
    private lateinit var tvTempCpu: TextView
    private lateinit var tvTempGpu: TextView
    private lateinit var tvNetDown: TextView
    private lateinit var tvNetUp: TextView
    private lateinit var tvNetTotal: TextView
    private lateinit var btnSettings: Button

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
    private val dateFormat = SimpleDateFormat("M月d日 EEEE", Locale.CHINA)
    private var lastDateKey = ""

    private val clockTick = object : Runnable {
        override fun run() {
            val now = Date()
            tvClock.text = timeFormat.format(now)
            // 日期与农历一天才变一次，跨天时才重新计算
            val dayKey = dateFormat.format(now)
            if (dayKey != lastDateKey) {
                lastDateKey = dayKey
                val lunar = Calendar.getInstance().let { LunarCalendar.solarToLunar(it) }
                tvCalLunar.text = if (lunar != null) "${dayKey}  ${lunar.line}" else dayKey
            }
            handler.postDelayed(this, 1000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_watch)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)

        initViews()
        setupListeners()

        serverUrl = prefs.getString(KEY_URL, "") ?: ""
        if (serverUrl.isEmpty()) {
            showAddressDialog(true)
        } else {
            startSSE()
        }
    }

    private fun initViews() {
        tvClock = findViewById(R.id.tv_clock)
        tvCalLunar = findViewById(R.id.tv_cal_lunar)
        tvStatus = findViewById(R.id.tv_status)
        tvCpuPercent = findViewById(R.id.tv_cpu_percent)
        progressCpu = findViewById(R.id.progress_cpu)
        tvMemPercent = findViewById(R.id.tv_mem_percent)
        progressMem = findViewById(R.id.progress_mem)
        tvMemDetail = findViewById(R.id.tv_mem_detail)
        tvTempCpu = findViewById(R.id.tv_temp_cpu)
        tvTempGpu = findViewById(R.id.tv_temp_gpu)
        tvNetDown = findViewById(R.id.tv_net_down)
        tvNetUp = findViewById(R.id.tv_net_up)
        tvNetTotal = findViewById(R.id.tv_net_total)
        btnSettings = findViewById(R.id.btn_settings)
    }

    private fun setupListeners() {
        btnSettings.setOnClickListener { showAddressDialog(false) }
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
            tvCpuPercent.text = String.format("%.1f%%", cpuPercent)
            progressCpu.progress = cpuPercent.toInt()
        }

        // 内存
        val memory = json.optJSONObject("memory")
        if (memory != null) {
            val memPercent = memory.optDouble("percent", 0.0)
            val used = memory.optLong("used", 0)
            val total = memory.optLong("total", 0)
            tvMemPercent.text = String.format("%.1f%%", memPercent)
            progressMem.progress = memPercent.toInt()
            tvMemDetail.text = "${formatBytes(used)}/${formatBytes(total)}"
        }

        // 温度
        val temperature = json.optJSONObject("temperature")
        if (temperature != null) {
            val cpuTemp = temperature.optDouble("cpu_temp", 0.0)
            val gpuTemp = temperature.optDouble("gpu_temp", 0.0)
            tvTempCpu.text = if (cpuTemp > 0) String.format("CPU:%.0f°", cpuTemp) else "CPU:--"
            tvTempGpu.text = if (gpuTemp > 0) String.format("GPU:%.0f°", gpuTemp) else "GPU:--"
        }

        // 网络
        val network = json.optJSONObject("network")
        if (network != null) {
            val recvRate = network.optDouble("recv_rate", 0.0)
            val sentRate = network.optDouble("sent_rate", 0.0)
            val bytesRecv = network.optLong("bytes_recv", 0)
            val bytesSent = network.optLong("bytes_sent", 0)
            tvNetDown.text = "↓${formatBytes(recvRate.toLong())}/s"
            tvNetUp.text = "↑${formatBytes(sentRate.toLong())}/s"
            tvNetTotal.text = "累计 ↓${formatBytes(bytesRecv)}  ↑${formatBytes(bytesSent)}"
        }
    }

    private fun updateStatus(status: String) {
        tvStatus.text = status
    }

    private fun formatBytes(bytes: Long): String {
        if (bytes < 1024) return "${bytes}B"
        val kb = bytes / 1024.0
        if (kb < 1024) return String.format("%.0fK", kb)
        val mb = kb / 1024.0
        if (mb < 1024) return String.format("%.1fM", mb)
        val gb = mb / 1024.0
        return String.format("%.1fG", gb)
    }

    private fun showAddressDialog(firstTime: Boolean) {
        val input = EditText(this)
        input.setText(serverUrl.ifEmpty { "http://192.168." })
        input.setHint("http://192.168.1.100:8765")
        input.setTextColor(Color.WHITE)
        input.setHintTextColor(Color.GRAY)

        val dialog = AlertDialog.Builder(this)
            .setTitle("服务地址")
            .setMessage("电脑需以局域网模式启动：\n\$env:MONITOR_LAN=\"1\"; .\\monitor.exe")
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
                } else {
                    input.error = "格式错误"
                }
            }
        }
        dialog.show()
    }

    override fun onResume() {
        super.onResume()
        handler.post(clockTick) // 启动秒级时钟
        if (serverUrl.isNotEmpty() && !sseClient.isRunning()) {
            startSSE()
        }
    }

    override fun onPause() {
        super.onPause()
        handler.removeCallbacks(clockTick) // 离开界面停止时钟
        stopSSE()
    }

    override fun onDestroy() {
        super.onDestroy()
        stopSSE()
    }

    companion object {
        private const val KEY_URL = "server_url"
    }
}
