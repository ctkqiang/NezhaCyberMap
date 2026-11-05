/*
MainActivity.kt —— Compose 装配层。

布局：
  全屏 Canvas（地球）→ 顶栏（标题 + 统计 + 暂停）→ 左下实时事件流 → 右下 feed 状态。

帧循环：withFrameMillis 驱动 renderer.frame()，与 Choreographer 对齐。
手势：detectDragGestures 直接喂给 renderer 的相机（含甩动惯性）。
*/
package com.nezha.cybermap

import android.os.Bundle
import android.os.SystemClock
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectDragGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameMillis
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.viewmodel.compose.viewModel

/** 默认连模拟器的宿主机回环；真机调试时改成局域网 IP。 */
private const val SERVER_URL = "ws://10.0.2.2:8148/ws"

private val DarkScheme = darkColorScheme(
    background = Color(0xFF050507),
    surface = Color(0xFF120D14),
    primary = Color(0xFFFF4A60),
    onBackground = Color(0xFFFFEEF2),
    onSurface = Color(0xFFFFEEF2),
)

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent {
            MaterialTheme(colorScheme = DarkScheme) {
                ThreatMapScreen(SERVER_URL)
            }
        }
    }
}

@Composable
fun ThreatMapScreen(serverUrl: String, vm: MainViewModel = viewModel()) {
    val stats by vm.stats.collectAsState()
    val feeds by vm.feeds.collectAsState()
    val connected by vm.connected.collectAsState()
    val log by vm.log.collectAsState()

    // 陆地点阵只构建一次；尺寸变化时只重建相机参数。
    val context = LocalContext.current
    val renderer = remember {
        GlobeRenderer(buildLandVectors(context.assets)).also { vm.attach(it) }
    }
    var frameTick by remember { mutableLongStateOf(0L) }

    LaunchedEffect(Unit) {
        vm.connect(serverUrl)
        while (true) {
            withFrameMillis { frameTick = it }
        }
    }

    Box(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.background)) {
        Canvas(
            Modifier
                .fillMaxSize()
                .pointerInput(renderer) {
                    detectDragGestures(
                        onDragStart = { renderer.dragStart() },
                        onDragEnd = { renderer.dragEnd(SystemClock.uptimeMillis()) },
                        onDragCancel = { renderer.dragEnd(SystemClock.uptimeMillis()) },
                    ) { change, dragAmount ->
                        change.consume()
                        renderer.dragBy(dragAmount.x, dragAmount.y, SystemClock.uptimeMillis())
                    }
                },
        ) {
            // frameTick 仅作"帧已到"信号：读取它会让该 Canvas 每帧重绘，
            // 实际时间戳由 renderer 内部以 SystemClock 为准。
            @Suppress("UNUSED_EXPRESSION") frameTick
            renderer.resize(size.width, size.height)
            with(renderer) { frame(SystemClock.uptimeMillis()) }
        }

        TopBar(stats, connected, onPause = vm::togglePause)
        EventLog(log, Modifier.align(Alignment.BottomStart))
        FeedStatus(feeds, Modifier.align(Alignment.BottomEnd))
    }
}

@Composable
private fun TopBar(stats: StatsSnapshot, connected: Boolean, onPause: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text("哪吒威胁地图", color = Palette.headGlow, fontSize = 15.sp, fontFamily = FontFamily.Monospace)
        Spacer(Modifier.width(18.dp))
        StatItem("事件总量", "${stats.total}")
        Spacer(Modifier.width(14.dp))
        StatItem("速率/分", "${stats.rate}")
        Spacer(Modifier.width(14.dp))
        StatItem("运行", formatUptime(stats.uptimeSec))
        Spacer(Modifier.weight(1f))
        Text(
            if (connected) "● 已连接" else "○ 未连接",
            color = if (connected) Color(0xFF3DD68C) else Color(0xFFFF4A60),
            fontSize = 11.sp,
            fontFamily = FontFamily.Monospace,
        )
        Spacer(Modifier.width(10.dp))
        TextButton(onClick = onPause) {
            Text(if (stats.paused) "继续" else "暂停", color = Palette.target, fontSize = 12.sp)
        }
    }
}

@Composable
private fun StatItem(label: String, value: String) {
    Column {
        Text(label, color = Color(0xFF8A6A72), fontSize = 9.sp, fontFamily = FontFamily.Monospace)
        Text(value, color = Palette.headGlow, fontSize = 16.sp, fontFamily = FontFamily.Monospace)
    }
}

private fun formatUptime(sec: Long): String {
    val h = sec / 3600
    val m = sec % 3600 / 60
    val s = sec % 60
    return "%02d:%02d:%02d".format(h, m, s)
}

@Composable
private fun EventLog(log: List<AttackEvent>, modifier: Modifier) {
    Column(modifier.padding(12.dp).width(300.dp)) {
        Text("实时事件流 · LIVE", color = Palette.target, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
        Spacer(Modifier.height(6.dp))
        for (ev in log) {
            Text(
                "${ev.raw.ifBlank { ev.src.ip }}  ${ev.src.name} → ${ev.dst.name}",
                color = Color(0xFFC8909A),
                fontSize = 10.sp,
                fontFamily = FontFamily.Monospace,
                maxLines = 1,
            )
        }
    }
}

@Composable
private fun FeedStatus(feeds: Map<String, FeedItem>, modifier: Modifier) {
    Column(modifier.padding(12.dp), horizontalAlignment = Alignment.End) {
        for ((key, f) in feeds.entries.take(4)) {
            val c = when (f.state) {
                1 -> Color(0xFF3DD68C)
                2 -> Color(0xFFFF4A60)
                else -> Color(0xFF8A6A72)
            }
            Text("${f.name.ifBlank { key}} · ${stateName(f.state)}", color = c, fontSize = 9.sp, fontFamily = FontFamily.Monospace)
        }
    }
}

private fun stateName(state: Int) = when (state) {
    1 -> "运行中"
    2 -> "异常"
    else -> "待命"
}
