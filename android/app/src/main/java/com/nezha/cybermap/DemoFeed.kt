/*
DemoFeed.kt —— 内置演示事件源。

安卓端是独立进程，无法直接跑 Go 后端；没有可连的服务器时，
用确定性伪随机在本地生成攻击事件，让界面不至于空白。

坐标池取常见攻击源/蜜罐国家，严重度按指数分布（低危居多、严重稀少），
与 Web 端 Mock feed 的行为一致：700ms 一条。
*/
package com.nezha.cybermap

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlin.random.Random

class DemoFeed(
    private val scope: CoroutineScope,
    private val onEvent: (AttackEvent) -> Unit,
) {
    private var job: Job? = null

    private val origins = listOf(
        GeoPoint("45.148.10.9", "RU", "俄罗斯", 55.75, 37.61),
        GeoPoint("103.86.52.4", "CN", "中国", 39.90, 116.40),
        GeoPoint("185.220.101.7", "NL", "荷兰", 52.37, 4.90),
        GeoPoint("91.121.87.5", "FR", "法国", 48.86, 2.35),
        GeoPoint("193.32.66.21", "US", "美国", 40.71, -74.00),
        GeoPoint("177.54.144.3", "BR", "巴西", -23.55, -46.63),
        GeoPoint("103.21.244.8", "IN", "印度", 28.61, 77.21),
        GeoPoint("196.22.104.5", "ZA", "南非", -26.20, 28.05),
        GeoPoint("210.245.86.2", "VN", "越南", 21.03, 105.85),
        GeoPoint("14.186.77.3", "KR", "韩国", 37.57, 126.98),
    )

    private val targets = listOf(
        GeoPoint("10.0.0.1", "US", "美国蜜罐", 38.90, -77.04),
        GeoPoint("10.0.0.2", "SG", "新加坡蜜罐", 1.35, 103.82),
        GeoPoint("10.0.0.3", "DE", "德国蜜罐", 52.52, 13.40),
        GeoPoint("10.0.0.4", "JP", "日本蜜罐", 35.68, 139.69),
        GeoPoint("10.0.0.5", "AU", "澳大利亚蜜罐", -33.87, 151.21),
    )

    fun start() {
        if (job?.isActive == true) return
        job = scope.launch {
            while (isActive) {
                delay(700)
                onEvent(randomEvent())
            }
        }
    }

    fun stop() {
        job?.cancel()
        job = null
    }

    private fun randomEvent(): AttackEvent {
        val src = origins[Random.nextInt(origins.size)]
        val dst = targets[Random.nextInt(targets.size)]
        // 指数分布：p(0)≈0.50, p(1)≈0.25, p(2)≈0.15, p(3+)≈0.10
        val sev = (-Math.log(1 - Random.nextDouble()) * 1.6).toInt().coerceIn(0, 4)
        return AttackEvent(
            ts = System.currentTimeMillis(),
            src = src,
            dst = dst,
            sev = sev,
            feed = "演示",
            raw = "${src.ip}:1${Random.nextInt(1000, 9999)}",
        )
    }
}
