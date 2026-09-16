package com.qianwen.monitor

import android.view.Choreographer
import android.view.View
import kotlin.math.abs

/** Slow diagonal movement, confined to the picture area above the thumbnail strip. */
internal class ClockScreensaver(private val clock: View, private val bottomStrip: View) : Choreographer.FrameCallback {
    private val choreographer = Choreographer.getInstance()
    private val density = clock.resources.displayMetrics.density
    private val horizontal = BouncingAxis(-32f * density)
    private val vertical = BouncingAxis(23f * density)
    private var running = false
    private var initialized = false
    private var lastFrame = 0L

    fun start() {
        if (running) return
        running = true
        lastFrame = 0L
        choreographer.postFrameCallback(this)
    }

    fun stop() {
        running = false
        lastFrame = 0L
        choreographer.removeFrameCallback(this)
    }

    override fun doFrame(frameTimeNanos: Long) {
        if (!running) return
        val parent = clock.parent as? View
        if (parent != null && parent.width > 0 && parent.height > 0 && clock.width > 0 && clock.height > 0) {
            val inset = 12f * density
            // Recompute after rotation, layout or clock-size changes.
            val bottom = if (bottomStrip.visibility == View.VISIBLE && bottomStrip.height > 0) {
                bottomStrip.top.toFloat()
            } else parent.height.toFloat()
            val left = inset.coerceAtMost((parent.width - clock.width).coerceAtLeast(0) / 2f)
            val top = inset.coerceAtMost((bottom - clock.height).coerceAtLeast(0f) / 2f)
            val width = (parent.width - clock.width - 2f * left).coerceAtLeast(0f)
            val height = (bottom - clock.height - 2f * top).coerceAtLeast(0f)
            if (!initialized) {
                horizontal.position = (clock.x - left).coerceIn(0f, width)
                vertical.position = (clock.y - top).coerceIn(0f, height)
                initialized = true
            }
            // Ignore time spent paused and cap stalls to prevent sudden jumps.
            val seconds = if (lastFrame == 0L) 0f else
                ((frameTimeNanos - lastFrame) / 1_000_000_000f).coerceIn(0f, 0.1f)
            horizontal.advance(width, seconds)
            vertical.advance(height, seconds)
            clock.x = left + horizontal.position
            clock.y = top + vertical.position
        }
        lastFrame = frameTimeNanos
        choreographer.postFrameCallbackDelayed(this, 32L)
    }
}

/** Reflect excess travel at either boundary, including tiny or resized bounds. */
internal class BouncingAxis(var velocity: Float) {
    var position = 0f

    fun advance(limit: Float, seconds: Float) {
        if (limit <= 0f) {
            position = 0f
            return
        }
        val next = position.coerceIn(0f, limit) + velocity * seconds
        val period = 2f * limit
        val folded = ((next % period) + period) % period
        position = if (folded <= limit) folded else period - folded
        velocity = when {
            folded == 0f -> abs(velocity)
            folded == limit -> -abs(velocity)
            folded > limit -> -velocity
            else -> velocity
        }
    }
}
