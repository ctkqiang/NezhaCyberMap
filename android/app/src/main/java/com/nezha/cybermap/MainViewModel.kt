/*
MainViewModel.kt —— UI 状态聚合。

事件流本身不进 StateFlow（高频，交给 GlobeRenderer 直接消费），
StateFlow 只承载 HUD 需要的低频状态：统计、feed、连接、事件流水。
*/
package com.nezha.cybermap

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

private const val FEED_KEEP = 8

class MainViewModel : ViewModel() {

    var renderer: GlobeRenderer? = null
        private set

    private val _stats = MutableStateFlow(StatsSnapshot.EMPTY)
    val stats: StateFlow<StatsSnapshot> = _stats

    private val _feeds = MutableStateFlow<Map<String, FeedItem>>(emptyMap())
    val feeds: StateFlow<Map<String, FeedItem>> = _feeds

    private val _connected = MutableStateFlow(false)
    val connected: StateFlow<Boolean> = _connected

    private val _log = MutableStateFlow<List<AttackEvent>>(emptyList())
    val log: StateFlow<List<AttackEvent>> = _log

    private var ws: RealtimeClient? = null
    private var demo: DemoFeed? = null

    private val _demoMode = MutableStateFlow(false)
    val demoMode: StateFlow<Boolean> = _demoMode

    private val _serverUrl = MutableStateFlow("")
    val serverUrl: StateFlow<String> = _serverUrl

    fun attach(renderer: GlobeRenderer) {
        this.renderer = renderer
    }

    fun connect(url: String) {
        stopDemo()
        _serverUrl.value = url
        ws?.close()
        ws = RealtimeClient(url, viewModelScope, ::onMsg) { on ->
            _connected.value = on
        }.also { it.connect() }
    }

    /** 演示模式：本地伪随机事件源，不需要任何后端。 */
    fun startDemo() {
        ws?.close()
        ws = null
        _connected.value = false
        _demoMode.value = true
        _stats.value = StatsSnapshot.EMPTY
        _feeds.value = mapOf("demo" to FeedItem(1, "演示", "本地生成", 0L))
        demo = DemoFeed(viewModelScope) { ev ->
            val now = android.os.SystemClock.uptimeMillis()
            renderer?.addEvent(ev, now)
            _log.value = (listOf(ev) + _log.value).take(FEED_KEEP)
            _stats.value = _stats.value.copy(total = _stats.value.total + 1)
        }.also { it.start() }
    }

    fun stopDemo() {
        demo?.stop()
        demo = null
        _demoMode.value = false
    }

    fun togglePause() {
        val next = !_stats.value.paused
        ws?.sendPause(next)
        _stats.value = _stats.value.copy(paused = next)
    }

    override fun onCleared() {
        ws?.close()
        demo?.stop()
    }

    private fun onMsg(msg: ServerMsg) {
        val r = renderer ?: return
        val now = android.os.SystemClock.uptimeMillis()
        when (msg) {
            is ServerMsg.Welcome -> {
                msg.stats?.let { _stats.value = it }
                _feeds.value = msg.feeds
                r.replaceEvents(msg.events, now)
                viewModelScope.launch { _log.value = msg.events.takeLast(FEED_KEEP).reversed() }
            }
            is ServerMsg.Event -> {
                r.addEvent(msg.event, now)
                _log.value = (listOf(msg.event) + _log.value).take(FEED_KEEP)
            }
            is ServerMsg.Stats -> _stats.value = msg.stats
            is ServerMsg.Feeds -> _feeds.value = msg.feeds
        }
    }
}
