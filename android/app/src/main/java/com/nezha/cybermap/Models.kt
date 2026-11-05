/*
Models.kt —— WebSocket 协议模型与 JSON 解析。

协议（服务端 → 客户端）：
  welcome {type, stats, events, feeds}   连接建立时的全量快照
  event   {type, event}                  单条攻击事件
  stats   {type, stats}                  每秒统计快照
  feeds   {type, feeds}                  feed 状态变更

客户端 → 服务端：
  {"type":"pause","paused":true|false}

与 Web 端 web/static/js/ws.mjs 保持同一协议，字段名以后端
internal/web/hub.go 的 JSON tag 为准。
*/
package com.nezha.cybermap

import org.json.JSONObject

/** 攻击源/目标点。code 为国家码（如 "CN"），name 为展示名。 */
data class GeoPoint(
    val ip: String,
    val code: String,
    val name: String,
    val lat: Double,
    val lon: Double,
)

/** 单条攻击事件。bornAtMs 是本地入场时间戳，用于动画时基（与墙钟 ts 解耦）。 */
data class AttackEvent(
    val ts: Long,
    val src: GeoPoint,
    val dst: GeoPoint,
    val sev: Int,
    val feed: String,
    val raw: String,
    val bornAtMs: Long = 0L,
)

/** 攻击源国家排行项。 */
data class CountryItem(val code: String, val name: String, val count: Int)

/** Feed 运行状态。state: 0 待命 / 1 运行 / 2 错误。 */
data class FeedItem(val state: Int, val name: String, val msg: String, val at: Long)

/** 每秒统计快照。severity/spark 为定长数组，与后端 [4]int / [30]int 对应。 */
data class StatsSnapshot(
    val total: Int,
    val rate: Int,
    val uptimeSec: Long,
    val paused: Boolean,
    val countries: List<CountryItem>,
    val severity: IntArray,
    val spark: IntArray,
) {
    companion object {
        val EMPTY = StatsSnapshot(0, 0, 0, false, emptyList(), IntArray(4), IntArray(30))
    }
}

/** 服务端消息的解析出口：一次 parse 分发到四类载荷。 */
sealed interface ServerMsg {
    data class Welcome(val stats: StatsSnapshot?, val events: List<AttackEvent>, val feeds: Map<String, FeedItem>) : ServerMsg
    data class Event(val event: AttackEvent) : ServerMsg
    data class Stats(val stats: StatsSnapshot) : ServerMsg
    data class Feeds(val feeds: Map<String, FeedItem>) : ServerMsg
}

object Protocol {

    fun parse(text: String, nowMs: Long): ServerMsg? = runCatching {
        val o = JSONObject(text)
        when (o.optString("type")) {
            "welcome" -> ServerMsg.Welcome(
                stats = o.optJSONObject("stats")?.let(::parseStats),
                events = o.optJSONArray("events")?.let { arr ->
                    List(arr.length()) { i -> parseEvent(arr.getJSONObject(i), 0L) }
                } ?: emptyList(),
                feeds = parseFeeds(o.optJSONObject("feeds")),
            )
            "event" -> o.optJSONObject("event")?.let { ServerMsg.Event(parseEvent(it, nowMs)) }
            "stats" -> o.optJSONObject("stats")?.let { ServerMsg.Stats(parseStats(it)) }
            "feeds" -> ServerMsg.Feeds(parseFeeds(o.optJSONObject("feeds")))
            else -> null
        }
    }.getOrNull()

    fun pauseCommand(paused: Boolean): String =
        JSONObject().put("type", "pause").put("paused", paused).toString()

    private fun parseEvent(o: JSONObject, nowMs: Long) = AttackEvent(
        ts = o.optLong("ts"),
        src = parsePoint(o.getJSONObject("src")),
        dst = parsePoint(o.getJSONObject("dst")),
        sev = o.optInt("sev"),
        feed = o.optString("feed"),
        raw = o.optString("raw"),
        // welcome 回放由调用方按序重新分配 bornAt；实时事件用本地时钟。
        bornAtMs = if (nowMs > 0L) nowMs else 0L,
    )

    private fun parsePoint(o: JSONObject) = GeoPoint(
        ip = o.optString("ip"),
        code = o.optString("code"),
        name = o.optString("name"),
        lat = o.optDouble("lat"),
        lon = o.optDouble("lon"),
    )

    private fun parseStats(o: JSONObject): StatsSnapshot {
        val countries = o.optJSONArray("countries")?.let { arr ->
            List(arr.length()) { i ->
                val c = arr.getJSONObject(i)
                CountryItem(c.optString("code"), c.optString("name"), c.optInt("count"))
            }
        } ?: emptyList()
        return StatsSnapshot(
            total = o.optInt("total"),
            rate = o.optInt("rate"),
            uptimeSec = o.optLong("uptime"),
            paused = o.optBoolean("paused"),
            countries = countries,
            severity = o.optJSONArray("severity")?.let { arr ->
                IntArray(4) { i -> arr.optInt(i) }
            } ?: IntArray(4),
            spark = o.optJSONArray("spark")?.let { arr ->
                IntArray(30) { i -> arr.optInt(i) }
            } ?: IntArray(30),
        )
    }

    private fun parseFeeds(o: JSONObject?): Map<String, FeedItem> {
        if (o == null) return emptyMap()
        return o.keys().asSequence().associateWith { k ->
            val f = o.getJSONObject(k)
            FeedItem(f.optInt("state"), f.optString("name"), f.optString("msg"), f.optLong("at"))
        }
    }
}
