package com.qianwen.monitor

import android.annotation.SuppressLint
import android.app.Activity
import android.app.AlertDialog
import android.graphics.Color
import android.os.Build
import android.os.Bundle
import android.view.KeyEvent
import android.view.View
import android.view.WindowManager
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView

/**
 * 电脑监控手机端：用 WebView 加载电脑上 monitor.exe 提供的科幻仪表盘。
 * 零第三方依赖，仅系统 API，兼容 Android 5.0（API 21）到最新版本。
 */
class MainActivity : Activity() {

    private lateinit var web: WebView
    private lateinit var overlay: View
    private lateinit var overlayText: TextView
    private lateinit var btnScan: Button
    private lateinit var btnManual: Button
    private lateinit var scanResults: LinearLayout
    private lateinit var scanStatus: TextView
    private var serverUrl = ""
    private var loadFailed = false // 本次加载是否已报错，防止 onPageFinished 误隐藏错误页
    private var isScanning = false

    private val prefs by lazy { getSharedPreferences("monitor", MODE_PRIVATE) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        // 监控看板常亮显示，方便把手机当副屏盯数据
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)

        web = findViewById(R.id.web)
        overlay = findViewById(R.id.overlay)
        overlayText = findViewById(R.id.overlay_text)
        btnScan = findViewById(R.id.btn_scan)
        btnManual = findViewById(R.id.btn_manual)
        scanResults = findViewById(R.id.scan_results)
        scanStatus = findViewById(R.id.scan_status)

        setupWebView()
        setupScanButton()

        findViewById<View>(R.id.btn_settings).setOnClickListener { showAddressDialog(false) }
        findViewById<Button>(R.id.btn_retry).setOnClickListener { load() }
        findViewById<Button>(R.id.btn_change).setOnClickListener { showAddressDialog(false) }

        serverUrl = prefs.getString(KEY_URL, "") ?: ""
        if (serverUrl.isEmpty()) {
            showAddressDialog(true) // 首次启动必须先填地址
        } else {
            load()
        }
    }

    /** 初始化扫描按钮 */
    private fun setupScanButton() {
        btnScan.setOnClickListener { startScan() }
        btnManual.setOnClickListener { showAddressDialog(false) }
    }

    /** 启动局域网扫描 */
    private fun startScan() {
        if (isScanning) return
        isScanning = true
        btnScan.isEnabled = false
        btnScan.text = "扫描中..."
        scanResults.removeAllViews()
        scanStatus.visibility = View.VISIBLE
        scanStatus.text = "正在扫描局域网..."

        val scanner = Discovery(
            onFound = { result ->
                runOnUiThread {
                    addScanResult(result)
                }
            },
            onComplete = {
                runOnUiThread {
                    isScanning = false
                    btnScan.isEnabled = true
                    btnScan.text = "扫描"
                    if (scanResults.childCount == 0) {
                        scanStatus.text = "未发现监控服务"
                    } else {
                        scanStatus.visibility = View.GONE
                    }
                }
            }
        )
        scanner.scan(applicationContext)
    }

    /** 添加扫描结果到列表 */
    private fun addScanResult(result: Discovery.DiscoveryResult) {
        val itemView = layoutInflater.inflate(R.layout.item_scan_result, scanResults, false)
        val tvHost = itemView.findViewById<TextView>(R.id.tv_host)
        val tvIp = itemView.findViewById<TextView>(R.id.tv_ip)
        
        tvHost.text = result.host
        tvIp.text = "${result.ip}:${result.port}"
        
        itemView.setOnClickListener {
            serverUrl = "http://${result.ip}:${result.port}"
            prefs.edit().putString(KEY_URL, serverUrl).apply()
            overlay.visibility = View.GONE
            load()
        }
        
        scanResults.addView(itemView)
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun setupWebView() {
        web.settings.javaScriptEnabled = true   // 仪表盘依赖 JS 渲染
        web.settings.domStorageEnabled = true
        web.settings.loadWithOverviewMode = true
        web.setBackgroundColor(Color.parseColor("#04070f")) // 与仪表盘底色一致，加载过程不闪白

        web.webViewClient = object : WebViewClient() {
            override fun onPageFinished(view: WebView?, url: String?) {
                super.onPageFinished(view, url)
                if (!loadFailed) overlay.visibility = View.GONE
            }

            // API 23+：主文档加载失败（连接被拒、无法解析地址等）
            override fun onReceivedError(
                view: WebView?,
                request: WebResourceRequest?,
                error: WebResourceError?
            ) {
                super.onReceivedError(view, request, error)
                if (Build.VERSION.SDK_INT >= 23 && request != null && request.isForMainFrame) {
                    markFailed(error?.description?.toString() ?: getString(R.string.load_failed))
                }
            }

            // API 21-22 走旧版回调，保证 Android 5.x 也能显示错误页
            @Deprecated("旧版错误回调，仅用于 Android 5.x/6.x 兼容")
            override fun onReceivedError(
                view: WebView?,
                errorCode: Int,
                description: String?,
                failingUrl: String?
            ) {
                super.onReceivedError(view, errorCode, description, failingUrl)
                if (Build.VERSION.SDK_INT < 23) {
                    markFailed(description ?: getString(R.string.load_failed))
                }
            }
        }
    }

    /** 标记加载失败并展示错误提示页 */
    private fun markFailed(detail: String) {
        loadFailed = true
        overlayText.text = getString(R.string.connect_failed, serverUrl, detail)
        overlay.visibility = View.VISIBLE
    }

    /** 加载当前配置的监控服务地址 */
    private fun load() {
        if (serverUrl.isEmpty()) {
            showAddressDialog(true)
            return
        }
        loadFailed = false
        overlayText.text = getString(R.string.connecting, serverUrl)
        overlay.visibility = View.VISIBLE
        web.loadUrl(serverUrl)
    }

    /** 地址配置对话框；firstTime 为首次启动，不可取消 */
    private fun showAddressDialog(firstTime: Boolean) {
        val input = EditText(this)
        input.setText(serverUrl.ifEmpty { "http://192.168." })
        input.setHint(R.string.address_hint)
        input.setTextColor(Color.WHITE)
        input.setHintTextColor(Color.GRAY)

        val dialog = AlertDialog.Builder(this)
            .setTitle(R.string.address_title)
            .setMessage(R.string.address_help)
            .setView(input)
            .setCancelable(!firstTime)
            .setPositiveButton(R.string.connect, null) // 点击行为在下方接管，便于校验
            .setNegativeButton(R.string.cancel, null)
            .create()

        dialog.setOnShowListener {
            dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener {
                val raw = input.text.toString().trim().trimEnd('/')
                if (raw.matches(ADDRESS_REGEX)) {
                    serverUrl = raw
                    prefs.edit().putString(KEY_URL, raw).apply()
                    dialog.dismiss()
                    load()
                } else {
                    input.error = getString(R.string.address_invalid)
                }
            }
        }
        dialog.show()
    }

    /** 返回键优先退出网页历史，其次退出应用 */
    override fun onKeyDown(keyCode: Int, event: KeyEvent?): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK && web.canGoBack()) {
            web.goBack()
            return true
        }
        return super.onKeyDown(keyCode, event)
    }

    override fun onResume() {
        super.onResume()
        web.onResume()
    }

    override fun onPause() {
        web.onPause()
        super.onPause()
    }

    override fun onDestroy() {
        web.destroy()
        super.onDestroy()
    }

    companion object {
        private const val KEY_URL = "server_url"
        // 形如 http://192.168.1.100:8765（允许域名、端口与路径）
        private val ADDRESS_REGEX = Regex("^https?://[A-Za-z0-9.-]+(:\\d{1,5})?(/.*)?$")
    }
}
