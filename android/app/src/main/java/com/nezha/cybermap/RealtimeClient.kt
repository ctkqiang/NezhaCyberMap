/*
RealtimeClient.kt —— WebSocket 实时客户端（OkHttp，指数退避重连）。

与 Web 端 ws.mjs 同协议：welcome / event / stats / feeds，
以及客户端上行 {"type":"pause","paused":bool}。

回调全部切回主线程（通过传入的 scope），UI 层可直接更新 StateFlow。
*/
package com.nezha.cybermap

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.util.concurrent.TimeUnit

class RealtimeClient(
    private val url: String,
    private val scope: CoroutineScope,
    private val onMsg: (ServerMsg) -> Unit,
    private val onConn: (Boolean) -> Unit,
) {
    private val client = OkHttpClient.Builder()
        .pingInterval(20, TimeUnit.SECONDS)
        .build()

    private var sock: WebSocket? = null
    private var retryJob: Job? = null
    private var retryDelayMs = 1000L
    private var closedByUser = false

    fun connect() {
        closedByUser = false
        retryJob?.cancel()
        open()
    }

    fun close() {
        closedByUser = true
        retryJob?.cancel()
        sock?.close(1000, "bye")
        sock = null
    }

    fun sendPause(paused: Boolean) {
        sock?.send(Protocol.pauseCommand(paused))
    }

    private fun open() {
        val req = Request.Builder().url(url).build()
        sock = client.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                retryDelayMs = 1000L
                scope.launch(Dispatchers.Main) { onConn(true) }
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                val msg = Protocol.parse(text, System.currentTimeMillis()) ?: return
                scope.launch(Dispatchers.Main) { onMsg(msg) }
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(code, reason)
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                notifyClosed()
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                notifyClosed()
            }
        })
    }

    private fun notifyClosed() {
        scope.launch(Dispatchers.Main) { onConn(false) }
        if (closedByUser) return
        retryJob?.cancel()
        retryJob = scope.launch {
            delay(retryDelayMs)
            retryDelayMs = (retryDelayMs * 2).coerceAtMost(10_000L)
            open()
        }
    }
}
