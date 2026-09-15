package com.qianwen.monitor

import android.content.Context
import android.os.StatFs

data class SlideshowConfig(
    val enabled: Boolean,
    val idleSeconds: Int,
    val slideSeconds: Int,
    val displayMode: String,
    val cacheEnabled: Boolean
)

/** Shared slideshow preferences and the low-storage cache safety guard. */
object SlideshowSettings {
    const val MODE_FIT = "fit"
    const val MODE_FILL = "fill"
    const val MIN_CACHE_FREE_BYTES = 200L * 1024L * 1024L

    const val DEFAULT_IDLE_SECONDS = 60
    const val DEFAULT_SLIDE_SECONDS = 10

    private const val PREFS_NAME = "monitor"
    private const val KEY_ENABLED = "slideshow_enabled"
    private const val KEY_IDLE_SECONDS = "slideshow_idle_seconds"
    private const val KEY_SLIDE_SECONDS = "slideshow_slide_seconds"
    private const val KEY_DISPLAY_MODE = "slideshow_display_mode"
    private const val KEY_CACHE_ENABLED = "slideshow_cache_enabled"
    private const val KEY_RESUME_SLIDE_ID = "slideshow_resume_slide_id"

    fun load(context: Context): SlideshowConfig {
        val prefs = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        val mode = prefs.getString(KEY_DISPLAY_MODE, MODE_FIT)
            ?.takeIf { it == MODE_FIT || it == MODE_FILL }
            ?: MODE_FIT
        return SlideshowConfig(
            enabled = prefs.getBoolean(KEY_ENABLED, false),
            idleSeconds = prefs.getInt(KEY_IDLE_SECONDS, DEFAULT_IDLE_SECONDS).coerceIn(1, 86400),
            slideSeconds = prefs.getInt(KEY_SLIDE_SECONDS, DEFAULT_SLIDE_SECONDS).coerceIn(1, 3600),
            displayMode = mode,
            cacheEnabled = prefs.getBoolean(KEY_CACHE_ENABLED, false)
        )
    }

    fun save(context: Context, config: SlideshowConfig) {
        context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            .edit()
            .putBoolean(KEY_ENABLED, config.enabled)
            .putInt(KEY_IDLE_SECONDS, config.idleSeconds.coerceIn(1, 86400))
            .putInt(KEY_SLIDE_SECONDS, config.slideSeconds.coerceIn(1, 3600))
            .putString(
                KEY_DISPLAY_MODE,
                if (config.displayMode == MODE_FILL) MODE_FILL else MODE_FIT
            )
            .putBoolean(KEY_CACHE_ENABLED, config.cacheEnabled)
            .apply()
    }

    /** Stable image ID that should be shown first when slideshow playback resumes. */
    fun loadResumeSlideId(context: Context): String =
        context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            .getString(KEY_RESUME_SLIDE_ID, "")
            .orEmpty()

    /** Persist the next image rather than a numeric index, so list reordering is safe. */
    fun saveResumeSlideId(context: Context, slideId: String) {
        if (slideId.isEmpty()) return
        context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            .edit()
            .putString(KEY_RESUME_SLIDE_ID, slideId)
            .apply()
    }

    /** Available bytes on the same filesystem that will hold slideshow cache files. */
    fun availableCacheBytes(context: Context): Long {
        return try {
            StatFs(context.cacheDir.absolutePath).availableBytes
        } catch (_: Exception) {
            0L
        }
    }

    /**
     * Disable caching when remaining space falls below 200 MiB.
     * Returns true only when an enabled cache was automatically switched off.
     * Future download/copy loops should call this before every cached file too.
     */
    fun disableCacheIfLow(context: Context): Boolean {
        val prefs = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        if (!prefs.getBoolean(KEY_CACHE_ENABLED, false)) return false
        if (availableCacheBytes(context) >= MIN_CACHE_FREE_BYTES) return false
        prefs.edit().putBoolean(KEY_CACHE_ENABLED, false).apply()
        return true
    }
}
