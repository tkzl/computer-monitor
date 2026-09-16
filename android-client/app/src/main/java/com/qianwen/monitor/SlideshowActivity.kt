package com.qianwen.monitor

import android.app.Activity
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Rect
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.MotionEvent
import android.view.View
import android.view.WindowManager
import android.widget.ImageView
import android.widget.TextView
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.Executors
import java.util.concurrent.ThreadPoolExecutor

/** 全屏幻灯片；底部列表可浏览选图，轻触大图退出。 */
class SlideshowActivity : Activity() {
    private data class Slide(val id: String, val url: String, val modified: Long, val name: String)

    private lateinit var imageView: ImageView
    private lateinit var statusView: TextView
    private lateinit var thumbnails: SlideThumbnailStrip
    private var clockScreensaver: ClockScreensaver? = null
    private var stripGesture = false
    private var browsingThumbnails = false
    private var imageRequest = 0
    private var loadingImage = false
    private var paused = true
    private val imageExecutor = Executors.newFixedThreadPool(1) as ThreadPoolExecutor
    private var imageJob: java.util.concurrent.Future<*>? = null
    private val advanceSlide = Runnable { showNext(generation) }
    private val handler = Handler(Looper.getMainLooper())
    private var slides = emptyList<Slide>()
    private var index = 0
    private var generation = 0
    private var currentBitmap: Bitmap? = null
    private lateinit var baseUrl: String

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        window.decorView.systemUiVisibility = IMMERSIVE_FLAGS
        setContentView(R.layout.activity_slideshow)
        imageView = findViewById(R.id.slideshow_image)
        statusView = findViewById(R.id.slideshow_status)
        thumbnails = findViewById(R.id.slideshow_thumbnails)
        baseUrl = intent.getStringExtra(EXTRA_SERVER_URL)?.trimEnd('/') ?: ""
        val config = SlideshowSettings.load(this)
        val clock = findViewById<View>(R.id.slideshow_clock)
        clock.visibility = if (config.showClock) View.VISIBLE else View.GONE
        thumbnails.visibility = if (config.showThumbnails) View.VISIBLE else View.GONE
        if (config.showClock && config.moveClock) clockScreensaver = ClockScreensaver(clock, thumbnails)
        if (!config.enabled) {
            finish()
            return
        }
        imageView.scaleType = if (config.displayMode == SlideshowSettings.MODE_FILL) {
            ImageView.ScaleType.CENTER_CROP
        } else {
            ImageView.ScaleType.FIT_CENTER
        }
        loadManifest(++generation)
    }

    override fun dispatchTouchEvent(event: MotionEvent): Boolean {
        if (event.actionMasked == MotionEvent.ACTION_DOWN) {
            val bounds = Rect()
            stripGesture = thumbnails.visibility == View.VISIBLE &&
                thumbnails.getGlobalVisibleRect(bounds) && bounds.contains(event.rawX.toInt(), event.rawY.toInt())
            if (stripGesture) {
                browsingThumbnails = true
                handler.removeCallbacks(advanceSlide)
            } else {
                finish()
                return true
            }
        }
        if (stripGesture) {
            super.dispatchTouchEvent(event)
            if (event.actionMasked == MotionEvent.ACTION_UP || event.actionMasked == MotionEvent.ACTION_CANCEL) {
                stripGesture = false
                scheduleNext()
            }
        }
        return true
    }

    private fun scheduleNext() {
        handler.removeCallbacks(advanceSlide)
        if (!stripGesture && !loadingImage && !paused && !isFinishing && slides.isNotEmpty()) {
            handler.postDelayed(advanceSlide, SlideshowSettings.load(this).slideSeconds * 1000L)
        }
    }

    override fun onResume() {
        super.onResume()
        paused = false
        clockScreensaver?.start()
        scheduleNext()
    }

    override fun onPause() {
        paused = true
        clockScreensaver?.stop()
        handler.removeCallbacks(advanceSlide)
        super.onPause()
    }

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (hasFocus) window.decorView.systemUiVisibility = IMMERSIVE_FLAGS
    }

    private fun loadManifest(runId: Int) {
        if (baseUrl.isEmpty()) {
            showStatus("尚未设置电脑服务地址")
            return
        }
        Thread({
            try {
                val json = readJson("$baseUrl/api/slideshow/manifest")
                val list = ArrayList<Slide>()
                val items = json.optJSONArray("images")
                if (items != null) {
                    for (i in 0 until items.length()) {
                        val item = items.optJSONObject(i) ?: continue
                        val id = item.optString("id")
                        val path = item.optString("url")
                        if (id.isNotEmpty() && path.isNotEmpty()) {
                            list.add(Slide(id, absoluteUrl(path), item.optLong("modified"), item.optString("name")))
                        }
                    }
                }
                handler.post {
                    if (runId != generation || isFinishing) return@post
                    if (list.isEmpty()) {
                        showStatus(if (json.optBoolean("scanning")) "正在扫描电脑中的相册…" else "幻灯片目录中没有图片")
                        handler.postDelayed({ loadManifest(runId) }, MANIFEST_RETRY_MS)
                    } else {
                        slides = list
                        val resumeId = SlideshowSettings.loadResumeSlideId(this)
                        index = list.indexOfFirst { it.id == resumeId }.takeIf { it >= 0 } ?: 0
                        cleanupOldCache(list)
                        if (thumbnails.visibility == View.VISIBLE) {
                            val density = resources.displayMetrics.density
                            thumbnails.submit(list.size, { list[it].name }, { position ->
                                loadBitmap(list[position], (120 * density).toInt(), (80 * density).toInt(), false)
                            }, { position ->
                                index = position
                                handler.removeCallbacks(advanceSlide)
                                showNext(generation)
                            })
                            thumbnails.select(index, true)
                        }
                        showNext(runId)
                    }
                }
            } catch (_: Exception) {
                handler.post {
                    if (runId == generation && !isFinishing) {
                        showStatus("读取幻灯片失败，正在重试…")
                        handler.postDelayed({ loadManifest(runId) }, MANIFEST_RETRY_MS)
                    }
                }
            }
        }, "slideshow-manifest").start()
    }

    private fun showNext(runId: Int) {
        if (runId != generation || slides.isEmpty() || isFinishing) return
        handler.removeCallbacks(advanceSlide)
        val requestId = ++imageRequest
        val position = index % slides.size
        val slide = slides[position]
        index = (index + 1) % slides.size
        loadingImage = true
        imageJob?.cancel(true)
        imageExecutor.purge()
        imageJob = imageExecutor.submit {
            val bitmap = try { loadBitmap(slide) } catch (_: Exception) { null }
            handler.post {
                if (runId != generation || requestId != imageRequest || isFinishing) {
                    bitmap?.recycle()
                    return@post
                }
                loadingImage = false
                if (bitmap != null) {
                    val old = currentBitmap
                    currentBitmap = bitmap
                    imageView.setImageBitmap(bitmap)
                    statusView.visibility = View.GONE
                    thumbnails.select(position, !browsingThumbnails)
                    old?.takeIf { it !== bitmap && !it.isRecycled }?.recycle()
                    // index already points at the following slide. Saving its stable ID
                    // resumes from the correct position even if the manifest is reordered.
                    SlideshowSettings.saveResumeSlideId(this, slides[index % slides.size].id)
                } else {
                    showStatus("图片加载失败，正在跳过…")
                }
                scheduleNext()
            }
        }
    }

    private fun loadBitmap(
        slide: Slide,
        targetWidth: Int = resources.displayMetrics.widthPixels,
        targetHeight: Int = resources.displayMetrics.heightPixels,
        allowCache: Boolean = true
    ): Bitmap? {
        var cacheEnabled = allowCache && SlideshowSettings.load(this).cacheEnabled
        if (cacheEnabled && SlideshowSettings.disableCacheIfLow(this)) cacheEnabled = false
        val directory = File(cacheDir, "slideshow").also { it.mkdirs() }
        val suffix = slide.url.substringBefore('?').substringAfterLast('.', "img")
            .lowercase().replace(Regex("[^a-z0-9]"), "").take(5).ifEmpty { "img" }
        val cached = File(directory, "${slide.id}_${slide.modified}.$suffix")
        val source = if (cacheEnabled && cached.isFile && cached.length() > 0) {
            cached
        } else {
            val target = if (cacheEnabled) File(directory, cached.name + ".part")
            else File.createTempFile("slide_", ".tmp", cacheDir)
            try {
                download(slide.url, target, cacheEnabled)
                if (cacheEnabled && SlideshowSettings.load(this).cacheEnabled) {
                    if (!target.renameTo(cached)) {
                        target.copyTo(cached, overwrite = true)
                        target.delete()
                    }
                    cached
                } else target
            } catch (e: Exception) {
                target.delete()
                throw e
            }
        }
        return try {
            decodeSampled(source, targetWidth, targetHeight)
        } finally {
            if (!cacheEnabled || source.name.endsWith(".part")) source.delete()
        }
    }

    private fun download(url: String, target: File, persistent: Boolean) {
        val connection = URL(url).openConnection() as HttpURLConnection
        try {
            connection.connectTimeout = 10_000
            connection.readTimeout = 30_000
            connection.inputStream.use { input ->
                FileOutputStream(target).use { output ->
                    val buffer = ByteArray(64 * 1024)
                    while (true) {
                        if (Thread.currentThread().isInterrupted) throw InterruptedException()
                        val count = input.read(buffer)
                        if (count < 0) break
                        output.write(buffer, 0, count)
                        if (persistent && SlideshowSettings.disableCacheIfLow(this)) {
                            throw IllegalStateException("cache disabled due to low storage")
                        }
                    }
                }
            }
        } finally {
            connection.disconnect()
        }
    }

    private fun decodeSampled(file: File, targetWidth: Int, targetHeight: Int): Bitmap? {
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeFile(file.absolutePath, bounds)
        if (bounds.outWidth <= 0 || bounds.outHeight <= 0) return null
        var sample = 1
        while (bounds.outWidth / (sample * 2) >= targetWidth &&
            bounds.outHeight / (sample * 2) >= targetHeight) sample *= 2
        // 极端全景图可能只有一边超过屏幕很多；再以像素总量设上限，避免老平板 OOM。
        val maxPixels = targetWidth.toLong() * targetHeight.toLong() * 4L
        while (bounds.outWidth.toLong() * bounds.outHeight.toLong() /
            (sample.toLong() * sample.toLong()) > maxPixels) {
            sample *= 2
        }
        return BitmapFactory.decodeFile(file.absolutePath, BitmapFactory.Options().apply {
            inSampleSize = sample
            inPreferredConfig = Bitmap.Config.ARGB_8888
        })
    }

    private fun cleanupOldCache(current: List<Slide>) {
        val keep = current.map { "${it.id}_${it.modified}." }.toSet()
        File(cacheDir, "slideshow").listFiles()?.forEach { file ->
            if (file.name.endsWith(".part") || keep.none { file.name.startsWith(it) }) file.delete()
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

    private fun showStatus(text: String) {
        statusView.text = text
        statusView.visibility = View.VISIBLE
    }

    override fun onDestroy() {
        clockScreensaver?.stop()
        generation++
        imageRequest++
        imageJob?.cancel(true)
        imageExecutor.shutdownNow()
        thumbnails.close()
        handler.removeCallbacksAndMessages(null)
        imageView.setImageDrawable(null)
        currentBitmap?.takeIf { !it.isRecycled }?.recycle()
        currentBitmap = null
        super.onDestroy()
    }

    companion object {
        const val EXTRA_SERVER_URL = "server_url"
        private const val MANIFEST_RETRY_MS = 3_000L
        @Suppress("DEPRECATION")
        private const val IMMERSIVE_FLAGS = View.SYSTEM_UI_FLAG_FULLSCREEN or
            View.SYSTEM_UI_FLAG_HIDE_NAVIGATION or View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY or
            View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN or View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION or
            View.SYSTEM_UI_FLAG_LAYOUT_STABLE
    }
}
