package com.qianwen.monitor

import android.app.Activity
import android.app.Dialog
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Color
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.LruCache
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.BaseAdapter
import android.widget.FrameLayout
import android.widget.GridView
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream
import java.lang.ref.WeakReference
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.Executors

/** 设置中的幻灯片缩略图列表；只加载可见项，并可轻触查看单张大图。 */
class SlideshowListActivity : Activity() {
    private data class Slide(val id: String, val name: String, val url: String, val modified: Long)
    private data class Holder(val image: ImageView, val name: TextView)

    private lateinit var grid: GridView
    private lateinit var statusView: TextView
    private lateinit var baseUrl: String
    private var adapter: SlideAdapter? = null
    private var generation = 0
    private val handler = Handler(Looper.getMainLooper())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_slideshow_list)
        baseUrl = intent.getStringExtra(EXTRA_SERVER_URL)?.trimEnd('/') ?: ""
        grid = findViewById(R.id.grid_slideshow_list)
        statusView = findViewById(R.id.text_slideshow_list_status)
        findViewById<TextView>(R.id.btn_slideshow_list_back).setOnClickListener { finish() }
        loadManifest(++generation)
    }

    private fun loadManifest(runId: Int) {
        if (baseUrl.isEmpty()) {
            statusView.text = "尚未设置电脑服务地址"
            return
        }
        Thread({
            try {
                val json = readJson("$baseUrl/api/slideshow/manifest")
                val slides = ArrayList<Slide>()
                val items = json.optJSONArray("images")
                if (items != null) {
                    for (i in 0 until items.length()) {
                        val item = items.optJSONObject(i) ?: continue
                        val id = item.optString("id")
                        val path = item.optString("url")
                        if (id.isNotEmpty() && path.isNotEmpty()) {
                            slides.add(Slide(
                                id = id,
                                name = item.optString("name", "未命名图片"),
                                url = absoluteUrl(path),
                                modified = item.optLong("modified")
                            ))
                        }
                    }
                }
                handler.post {
                    if (runId != generation || isFinishing) return@post
                    if (slides.isEmpty()) {
                        statusView.text = if (json.optBoolean("scanning")) {
                            "电脑正在扫描幻灯片目录…"
                        } else {
                            json.optString("error").takeIf { it.isNotEmpty() }
                                ?: "幻灯片目录中没有图片"
                        }
                        if (json.optBoolean("scanning")) {
                            handler.postDelayed({ loadManifest(runId) }, RETRY_MS)
                        }
                        return@post
                    }
                    adapter?.close()
                    adapter = SlideAdapter(slides).also { slideAdapter ->
                        grid.adapter = slideAdapter
                        grid.setOnItemClickListener { _, _, position, _ ->
                            showLargePreview(slideAdapter.getSlide(position), slideAdapter)
                        }
                    }
                    val updating = if (json.optBoolean("scanning")) " · 目录更新中" else ""
                    val truncated = if (json.optBoolean("truncated")) " · 列表已达上限" else ""
                    statusView.text = "共 ${slides.size} 张 · 轻触小图可放大$updating$truncated"
                }
            } catch (_: Exception) {
                handler.post {
                    if (runId == generation && !isFinishing) {
                        statusView.text = "读取失败，正在重试…"
                        handler.postDelayed({ loadManifest(runId) }, RETRY_MS)
                    }
                }
            }
        }, "slideshow-list-manifest").start()
    }

    private fun showLargePreview(slide: Slide, slideAdapter: SlideAdapter) {
        val dialog = Dialog(this, android.R.style.Theme_Black_NoTitleBar_Fullscreen)
        val root = FrameLayout(this).apply {
            setBackgroundColor(Color.BLACK)
            setOnClickListener { dialog.dismiss() }
        }
        val image = ImageView(this).apply {
            scaleType = ImageView.ScaleType.FIT_CENTER
            setBackgroundColor(Color.BLACK)
            contentDescription = slide.name
        }
        root.addView(image, FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT,
            ViewGroup.LayoutParams.MATCH_PARENT
        ))
        val caption = TextView(this).apply {
            text = "${slide.name}  ·  轻触返回"
            setTextColor(Color.WHITE)
            setBackgroundColor(Color.argb(150, 4, 7, 15))
            setPadding(dp(16), dp(10), dp(16), dp(10))
            gravity = Gravity.CENTER
            textSize = 13f
        }
        root.addView(caption, FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT,
            ViewGroup.LayoutParams.WRAP_CONTENT,
            Gravity.BOTTOM
        ))
        dialog.setContentView(root)
        dialog.show()
        val metrics = resources.displayMetrics
        slideAdapter.loadInto(slide, image, metrics.widthPixels, metrics.heightPixels)
    }

    private inner class SlideAdapter(private val slides: List<Slide>) : BaseAdapter() {
        private val executor = Executors.newFixedThreadPool(3)
        private val pending = HashMap<String, MutableList<WeakReference<ImageView>>>()
        @Volatile private var closed = false
        private val cache = object : LruCache<String, Bitmap>(24 * 1024) {
            override fun sizeOf(key: String, value: Bitmap): Int = value.byteCount / 1024
        }

        override fun getCount(): Int = slides.size
        override fun getItem(position: Int): Any = slides[position]
        override fun getItemId(position: Int): Long = position.toLong()
        fun getSlide(position: Int): Slide = slides[position]

        override fun getView(position: Int, convertView: View?, parent: ViewGroup): View {
            val view: View
            val holder: Holder
            if (convertView == null) {
                view = layoutInflater.inflate(R.layout.item_slideshow_preview, parent, false)
                holder = Holder(
                    view.findViewById(R.id.image_slideshow_preview),
                    view.findViewById(R.id.text_slideshow_preview_name)
                )
                view.tag = holder
            } else {
                view = convertView
                holder = view.tag as Holder
            }
            val slide = slides[position]
            holder.name.text = slide.name
            loadInto(slide, holder.image, dp(190), dp(150))
            return view
        }

        fun loadInto(slide: Slide, image: ImageView, targetWidth: Int, targetHeight: Int) {
            val key = "${slide.id}_${slide.modified}_${targetWidth}x$targetHeight"
            image.tag = key
            val cached = cache.get(key)
            if (cached != null && !cached.isRecycled) {
                image.alpha = 1f
                image.setImageBitmap(cached)
                return
            }
            image.setImageDrawable(null)
            image.alpha = 0.35f
            var shouldStart = false
            synchronized(pending) {
                val listeners = pending[key]
                if (listeners == null) {
                    pending[key] = arrayListOf(WeakReference(image))
                    shouldStart = true
                } else {
                    listeners.add(WeakReference(image))
                }
            }
            if (!shouldStart || closed) return
            executor.execute {
                val bitmap = try {
                    downloadAndDecode(slide.url, targetWidth, targetHeight)
                } catch (_: Exception) {
                    null
                }
                handler.post {
                    val targets = synchronized(pending) { pending.remove(key).orEmpty() }
                    if (closed) return@post
                    if (bitmap != null) cache.put(key, bitmap)
                    targets.forEach { reference ->
                        reference.get()?.takeIf { it.tag == key }?.let { target ->
                            target.alpha = if (bitmap == null) 0.35f else 1f
                            target.setImageBitmap(bitmap)
                            if (bitmap == null) target.contentDescription = "加载失败：${slide.name}"
                        }
                    }
                }
            }
        }

        private fun downloadAndDecode(url: String, targetWidth: Int, targetHeight: Int): Bitmap? {
            val temp = File.createTempFile("slide_preview_", ".tmp", cacheDir)
            try {
                val connection = URL(url).openConnection() as HttpURLConnection
                try {
                    connection.connectTimeout = 10_000
                    connection.readTimeout = 30_000
                    connection.inputStream.use { input ->
                        FileOutputStream(temp).use { output -> input.copyTo(output, 64 * 1024) }
                    }
                } finally {
                    connection.disconnect()
                }
                val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
                BitmapFactory.decodeFile(temp.absolutePath, bounds)
                if (bounds.outWidth <= 0 || bounds.outHeight <= 0) return null
                var sample = 1
                while (bounds.outWidth / (sample * 2) >= targetWidth &&
                    bounds.outHeight / (sample * 2) >= targetHeight) sample *= 2
                val maxPixels = targetWidth.toLong() * targetHeight.toLong() * 4L
                while (bounds.outWidth.toLong() * bounds.outHeight.toLong() /
                    (sample.toLong() * sample.toLong()) > maxPixels) sample *= 2
                return BitmapFactory.decodeFile(temp.absolutePath, BitmapFactory.Options().apply {
                    inSampleSize = sample
                    inPreferredConfig = Bitmap.Config.ARGB_8888
                })
            } finally {
                temp.delete()
            }
        }

        fun close() {
            closed = true
            executor.shutdownNow()
            synchronized(pending) { pending.clear() }
            cache.evictAll()
        }
    }

    private fun readJson(url: String): JSONObject {
        val connection = URL(url).openConnection() as HttpURLConnection
        try {
            connection.connectTimeout = 10_000
            connection.readTimeout = 15_000
            return connection.inputStream.bufferedReader(Charsets.UTF_8).use { JSONObject(it.readText()) }
        } finally {
            connection.disconnect()
        }
    }

    private fun absoluteUrl(path: String): String =
        if (path.startsWith("http://") || path.startsWith("https://")) path else "$baseUrl$path"

    private fun dp(value: Int): Int = (value * resources.displayMetrics.density + 0.5f).toInt()

    override fun onDestroy() {
        generation++
        handler.removeCallbacksAndMessages(null)
        adapter?.close()
        adapter = null
        super.onDestroy()
    }

    companion object {
        const val EXTRA_SERVER_URL = "server_url"
        private const val RETRY_MS = 3_000L
    }
}
