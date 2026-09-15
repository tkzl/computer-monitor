package com.qianwen.monitor

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Color
import android.graphics.drawable.GradientDrawable
import android.util.AttributeSet
import android.util.LruCache
import android.view.Gravity
import android.widget.FrameLayout
import android.widget.HorizontalScrollView
import android.widget.ImageView
import android.widget.TextView
import java.util.concurrent.Future
import java.util.concurrent.Executors
import java.util.concurrent.ThreadPoolExecutor

/** Only visible cells (plus one on either side) exist, even for very large albums. */
class SlideThumbnailStrip @JvmOverloads constructor(
    context: Context, attrs: AttributeSet? = null
) : HorizontalScrollView(context, attrs) {
    private class Cell(val root: FrameLayout, val image: ImageView) {
        var request: Future<*>? = null
    }
    private val row = FrameLayout(context)
    private val cells = mutableMapOf<Int, Cell>()
    private val executor = Executors.newFixedThreadPool(2) as ThreadPoolExecutor
    private val cache = object : LruCache<Int, Bitmap>(4 * 1024 * 1024) {
        override fun sizeOf(key: Int, value: Bitmap) = value.byteCount
    }
    private val cellWidth get() = dp(124)
    private var count = 0
    private var selected = -1
    private var loader: ((Int) -> Bitmap?)? = null
    private var onSelect: ((Int) -> Unit)? = null
    private var nameAt: ((Int) -> String)? = null
    @Volatile private var closed = false
    private val refresh = Runnable { refreshCells() }

    init {
        isHorizontalScrollBarEnabled = false
        isFillViewport = true
        addView(row, LayoutParams(0, LayoutParams.MATCH_PARENT))
    }

    fun submit(count: Int, nameAt: (Int) -> String, loader: (Int) -> Bitmap?, onSelect: (Int) -> Unit) {
        this.count = count
        this.nameAt = nameAt
        this.loader = loader
        this.onSelect = onSelect
        row.layoutParams = LayoutParams(count * cellWidth, LayoutParams.MATCH_PARENT)
        post(refresh)
    }

    fun select(position: Int, reveal: Boolean) {
        selected = position
        cells.forEach { (index, cell) -> styleCell(cell, index == selected) }
        if (reveal) post {
            if (!closed) scrollTo((position * cellWidth - (width - cellWidth) / 2).coerceAtLeast(0), 0)
        }
    }

    override fun onScrollChanged(l: Int, t: Int, oldl: Int, oldt: Int) {
        super.onScrollChanged(l, t, oldl, oldt)
        removeCallbacks(refresh)
        postDelayed(refresh, 50)
    }

    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
        super.onSizeChanged(w, h, oldw, oldh)
        post(refresh)
    }

    private fun refreshCells() {
        if (closed || count == 0 || width == 0) return
        val first = (scrollX / cellWidth - 1).coerceAtLeast(0)
        val last = ((scrollX + width) / cellWidth + 1).coerceAtMost(count - 1)
        cells.keys.filter { it !in first..last }.forEach { index ->
            cells.remove(index)?.let {
                it.request?.cancel(true)
                it.image.setImageDrawable(null)
                row.removeView(it.root)
            }
        }
        executor.purge()
        for (index in first..last) {
            if (cells.containsKey(index)) continue
            val root = FrameLayout(context)
            val image = ImageView(context).apply {
                scaleType = ImageView.ScaleType.CENTER_CROP
                contentDescription = "第 ${index + 1} 张：${nameAt?.invoke(index).orEmpty()}"
            }
            root.addView(image, FrameLayout.LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.MATCH_PARENT))
            root.addView(TextView(context).apply {
                text = "${index + 1}"
                textSize = 11f
                setTextColor(Color.WHITE)
                setBackgroundColor(0x9904070F.toInt())
                setPadding(dp(4), 0, dp(4), 0)
            }, FrameLayout.LayoutParams(LayoutParams.WRAP_CONTENT, LayoutParams.WRAP_CONTENT, Gravity.BOTTOM or Gravity.END))
            row.addView(root, FrameLayout.LayoutParams(cellWidth - dp(8), LayoutParams.MATCH_PARENT).apply {
                leftMargin = index * cellWidth + dp(4)
                topMargin = dp(8)
                bottomMargin = dp(8)
            })
            val cell = Cell(root, image)
            cells[index] = cell
            styleCell(cell, index == selected)
            root.setOnClickListener { onSelect?.invoke(index) }
            val cached = cache.get(index)
            if (cached != null) {
                image.setImageBitmap(cached)
            } else {
                cell.request = executor.submit {
                    val bitmap = try { loader?.invoke(index) } catch (_: Exception) { null }
                    if (closed || Thread.currentThread().isInterrupted) {
                        bitmap?.recycle()
                    } else {
                        post {
                            if (closed) {
                                bitmap?.recycle()
                            } else if (bitmap != null) {
                                cache.put(index, bitmap)
                                if (cells[index] === cell) image.setImageBitmap(bitmap)
                            } else if (cells[index] === cell) {
                                image.contentDescription = "缩略图加载失败，点击查看第 ${index + 1} 张"
                            }
                        }
                    }
                }
            }
        }
    }

    private fun styleCell(cell: Cell, active: Boolean) {
        cell.root.background = GradientDrawable().apply {
            setColor(0xFF14202D.toInt())
            setStroke(dp(2), if (active) 0xFF00E5FF.toInt() else 0xFF314452.toInt())
        }
        cell.root.setPadding(dp(3), dp(3), dp(3), dp(3))
        cell.root.isSelected = active
    }

    fun close() {
        closed = true
        removeCallbacks(refresh)
        executor.shutdownNow()
        cells.values.forEach { it.image.setImageDrawable(null) }
        cells.clear()
        row.removeAllViews()
        cache.evictAll()
    }

    private fun dp(value: Int) = (value * resources.displayMetrics.density + 0.5f).toInt()
}
