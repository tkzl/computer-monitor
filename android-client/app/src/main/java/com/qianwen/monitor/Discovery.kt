package com.qianwen.monitor

import android.content.Context
import android.net.wifi.WifiManager
import android.os.Build
import android.util.Log
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.NetworkInterface
import java.net.SocketTimeoutException
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/**
 * 局域网发现工具：向当前子网的广播地址发送 UDP 探测包，
 * 收集监控服务的应答（主机名 + 端口），供用户一键选择连接。
 *
 * 协议约定：
 *   请求："MONITOR-DISCOVER"（任意字节即可，监控服务按前缀匹配）
 *   应答："MONITOR-FOUND {\"host\":\"...\",\"port\":\"...\"}"
 *
 * 兼容 Android 5.0（API 21）到最新版，零第三方依赖。
 */
class Discovery(
    private val onFound: (DiscoveryResult) -> Unit,
    private val onComplete: () -> Unit
) {

    data class DiscoveryResult(val host: String, val port: String, val ip: String)

    companion object {
        private const val TAG = "Discovery"
        private const val DISCOVER_PORT = 8766       // 与 monitor.exe 中的 discoverPort 保持一致
        private const val DISCOVER_MSG = "MONITOR-DISCOVER"
        private const val RESPONSE_PREFIX = "MONITOR-FOUND "
        private const val TIMEOUT_MS = 1500          // 单次广播等待应答的超时
        private const val BROADCAST_ROUNDS = 2       // 广播轮数（多轮提高发现率）
    }

    /**
     * 在后台线程池中执行扫描，每收到一条应答通过 [onFound] 回调（主线程）。
     * 扫描结束后通过 [onComplete] 回调（主线程）。
     */
    fun scan(context: Context? = null) {
        val executor = Executors.newSingleThreadExecutor()
        executor.execute {
            try {
                val broadcast = if (context != null) getBroadcastAddress(context) else getBroadcastAddressFallback()
                if (broadcast == null) {
                    Log.w(TAG, "无法获取子网广播地址，跳过自动扫描")
                    return@execute
                }
                Log.i(TAG, "开始扫描，广播地址：$broadcast")

                // 去重：同一 (ip, port) 只回调一次
                val seen = ConcurrentHashMap<String, Boolean>()

                repeat(BROADCAST_ROUNDS) { round ->
                    try {
                        DatagramSocket().use { socket ->
                            socket.broadcast = true
                            socket.soTimeout = TIMEOUT_MS
                            val req = DISCOVER_MSG.toByteArray()
                            val packet = DatagramPacket(req, req.size, broadcast, DISCOVER_PORT)
                            socket.send(packet)

                            val buf = ByteArray(512)
                            while (true) {
                                val recv = DatagramPacket(buf, buf.size)
                                try {
                                    socket.receive(recv)
                                } catch (_: SocketTimeoutException) {
                                    break // 本轮超时，进入下一轮或结束
                                }
                                val text = String(recv.data, 0, recv.length)
                                if (!text.startsWith(RESPONSE_PREFIX)) continue
                                val json = text.substring(RESPONSE_PREFIX.length)
                                val host = readJsonField(json, "host") ?: continue
                                val port = readJsonField(json, "port") ?: continue
                                val ip = recv.address?.hostAddress ?: continue
                                val key = "$ip:$port"
                                if (seen.putIfAbsent(key, true) == null) {
                                    Log.i(TAG, "发现监控服务：$host @ $ip:$port")
                                    onFound(DiscoveryResult(host, port, ip))
                                }
                            }
                        }
                    } catch (e: Exception) {
                        Log.w(TAG, "第 ${round + 1} 轮扫描异常：${e.message}")
                    }
                }
            } finally {
                onComplete()
                executor.shutdown()
            }
        }
    }

    /**
     * 获取当前 Wi-Fi 子网的广播地址。
     * 优先用 WifiManager（最稳），失败回退到遍历网卡找非回环 IPv4。
     */
    private fun getBroadcastAddress(context: Context): InetAddress? {
        // 1. 尝试通过 WifiManager 获取
        try {
            @Suppress("DEPRECATION")
            val wm = context.applicationContext.getSystemService(Context.WIFI_SERVICE) as? WifiManager
            if (wm != null) {
                val dhcp = wm.connectionInfo?.let { wm.dhcpInfo }
                if (dhcp != null) {
                    val ip = dhcp.ipAddress
                    val mask = dhcp.netmask
                    if (ip != 0 && mask != 0) {
                        val buf = ByteBuffer.allocate(4).order(ByteOrder.LITTLE_ENDIAN)
                        buf.putInt(ip)
                        val addr = buf.array()
                        val maskBuf = ByteBuffer.allocate(4).order(ByteOrder.LITTLE_ENDIAN)
                        maskBuf.putInt(mask)
                        val maskBytes = maskBuf.array()
                        val broadcast = ByteArray(4)
                        for (i in 0 until 4) {
                            broadcast[i] = (addr[i].toInt() or maskBytes[i].toInt().inv()).toByte()
                        }
                        return InetAddress.getByAddress(broadcast)
                    }
                }
            }
        } catch (e: Exception) {
            Log.w(TAG, "WifiManager 获取广播地址失败：${e.message}")
        }

        // 2. 回退：遍历网卡，找非回环 IPv4 的广播地址
        return getBroadcastAddressFallback()
    }

    /**
     * 回退方案：遍历网卡，找非回环 IPv4 的广播地址。
     */
    private fun getBroadcastAddressFallback(): InetAddress? {
        try {
            val interfaces = NetworkInterface.getNetworkInterfaces() ?: return null
            for (iface in interfaces.toList()) {
                if (iface.isLoopback || !iface.isUp) continue
                for (addr in iface.interfaceAddresses) {
                    val b = addr.broadcast ?: continue
                    if (b.address.size == 4) return b
                }
            }
        } catch (e: Exception) {
            Log.w(TAG, "遍历网卡获取广播地址失败：${e.message}")
        }
        return null
    }

    /**
     * 极简 JSON 字段读取：仅处理 "{\"host\":\"...\",\"port\":\"...\"}" 这种扁平字符串对象，
     * 避免引入 Gson/Moshi 等第三方库。
     */
    private fun readJsonField(json: String, key: String): String? {
        val needle = "\"$key\":\""
        val start = json.indexOf(needle)
        if (start < 0) return null
        val valueStart = start + needle.length
        val valueEnd = json.indexOf('"', valueStart)
        if (valueEnd < 0) return null
        return json.substring(valueStart, valueEnd)
    }
}
