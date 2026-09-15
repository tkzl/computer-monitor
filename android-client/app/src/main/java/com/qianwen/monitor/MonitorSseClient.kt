package com.qianwen.monitor

import org.json.JSONObject
import java.io.BufferedReader
import java.io.InputStreamReader
import java.net.HttpURLConnection
import java.net.URL

/**
 * 可取消、可重连的 SSE 客户端，供原生大屏与手表界面共用。
 * 每次 start 都使用独立代次；旧线程即使稍后从阻塞读取中醒来，也不能继续投递数据。
 */
class MonitorSseClient(
    private val onData: (JSONObject) -> Unit,
    private val onStatus: (String) -> Unit
) {
    private val lock = Any()

    @Volatile
    private var generation = 0

    @Volatile
    private var running = false

    private var worker: Thread? = null
    private var activeConnection: HttpURLConnection? = null

    fun isRunning(): Boolean = running

    fun start(baseUrl: String) {
        stop()

        val runId: Int
        val thread: Thread
        synchronized(lock) {
            running = true
            runId = ++generation
            thread = Thread({ runLoop(baseUrl.trimEnd('/'), runId) }, "monitor-sse-$runId")
            worker = thread
        }
        onStatus("连接中...")
        thread.start()
    }

    fun stop() {
        val connection: HttpURLConnection?
        val thread: Thread?
        synchronized(lock) {
            running = false
            generation++
            connection = activeConnection
            activeConnection = null
            thread = worker
            worker = null
        }
        // disconnect 才能可靠唤醒阻塞中的 InputStream；interrupt 负责打断重连等待。
        connection?.disconnect()
        thread?.interrupt()
    }

    private fun isCurrent(runId: Int): Boolean = running && generation == runId

    private fun runLoop(baseUrl: String, runId: Int) {
        while (isCurrent(runId)) {
            var connection: HttpURLConnection? = null
            try {
                connection = URL("$baseUrl/api/stream").openConnection() as HttpURLConnection
                connection.requestMethod = "GET"
                connection.setRequestProperty("Accept", "text/event-stream")
                connection.connectTimeout = 10_000
                connection.readTimeout = 0

                synchronized(lock) {
                    if (!isCurrent(runId)) {
                        connection.disconnect()
                        return
                    }
                    activeConnection = connection
                }

                BufferedReader(InputStreamReader(connection.inputStream)).use { reader ->
                    while (isCurrent(runId)) {
                        val line = reader.readLine() ?: break
                        if (!line.startsWith("data:")) continue
                        try {
                            onData(JSONObject(line.substring(5).trim()))
                        } catch (_: Exception) {
                            // 单帧损坏不终止整个长连接，等待下一帧。
                        }
                    }
                }
                if (isCurrent(runId)) {
                    onStatus("连接已断开，正在重连...")
                }
            } catch (e: Exception) {
                if (isCurrent(runId)) {
                    onStatus("连接失败: ${e.message ?: "未知错误"}，正在重连...")
                }
            } finally {
                synchronized(lock) {
                    if (activeConnection === connection) activeConnection = null
                }
                connection?.disconnect()
            }

            if (!isCurrent(runId)) break
            try {
                Thread.sleep(RECONNECT_DELAY_MS)
            } catch (_: InterruptedException) {
                break
            }
        }

        synchronized(lock) {
            if (generation == runId) {
                running = false
                worker = null
            }
        }
    }

    companion object {
        private const val RECONNECT_DELAY_MS = 3_000L
    }
}
