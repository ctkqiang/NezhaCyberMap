/*
MainActivity.kt —— Compose 装配层。

布局：
  全屏 Canvas（地球）→ 顶栏（两行：身份与开关 / 统计读数）→ 左下实时事件流 → 右下 feed 状态。

帧循环：withFrameMillis 驱动 renderer.frame()，与 Choreographer 对齐。
手势：detectDragGestures 直接喂给 renderer 的相机（含甩动惯性、平面平移）。
*/
package com.nezha.cybermap

import android.os.Bundle
import android.os.SystemClock
import androidx.activity.ComponentActivity
import androidx.activity.SystemBarStyle
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectDragGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.asPaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameMillis
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.viewmodel.compose.viewModel
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** 默认连模拟器的宿主机回环；真机调试时改成局域网 IP。 */
private const val SERVER_URL = "ws://10.0.2.2:8148/ws"

// Mr. Robot 美学向的界面色：中性黑底 + 灰阶文字，红色只留给威胁数据与离线告警。
private val Ink = Color(0xFFDCDCDC)
private val Ink2 = Color(0xFF8A8A8A)
private val Ink3 = Color(0xFF4D4D4D)
private val OkGreen = Color(0xFF3DD68C)
private val BadRed = Color(0xFFD92F45)

private val DarkScheme = darkColorScheme(
    background = Color(0xFF080808),
    surface = Color(0xFF101010),
    primary = Color(0xFFFF2E4D),
    onBackground = Ink,
    onSurface = Ink,
)

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // 边到边：地球铺满整屏。系统栏必须显式指定"暗背景 + 浅色图标"，
        // 交给 auto 的话，浅色模式下会选深色图标，落在纯黑背景上直接看不见。
        enableEdgeToEdge(
            statusBarStyle = SystemBarStyle.dark(android.graphics.Color.TRANSPARENT),
            navigationBarStyle = SystemBarStyle.dark(android.graphics.Color.TRANSPARENT),
        )
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
    val demoMode by vm.demoMode.collectAsState()
    val log by vm.log.collectAsState()
    var showServerDialog by remember { mutableStateOf(false) }
    var flatMode by remember { mutableStateOf(false) }
    var topBarPx by remember { mutableStateOf(0f) }
    var frameTick by remember { mutableLongStateOf(0L) }

    val context = LocalContext.current
    val density = LocalDensity.current
    val navBarPx = with(density) {
        WindowInsets.navigationBars.asPaddingValues().calculateBottomPadding().toPx()
    }

    // 陆地点阵在后台线程生成：40KB 轮廓解析 + 扫描线填充要几百毫秒，
    // 塞在组合里同步跑会把首帧卡成白屏。生成完成前先只画天空和一行提示。
    val land by produceState<FloatArray?>(null) {
        value = withContext(Dispatchers.Default) {
            runCatching { buildLandVectors(context.assets) }.getOrNull()
        }
    }
    val renderer = remember(land) { land?.let { GlobeRenderer(it) } }

    // renderer 可能在 ViewModel 开始推事件之后才就绪（异步生成地形），
    // 所以必须等它出现再 attach，否则第一批事件会被丢掉。
    LaunchedEffect(renderer) {
        renderer?.let { vm.attach(it) }
    }

    LaunchedEffect(Unit) {
        // 先尝试真实后端；3 秒内连不上自动降级到演示模式，不让用户面对黑屏。
        // 连接异常（地址非法、OkHttp 已关闭等）绝不能让演示降级跟着失效。
        runCatching { vm.connect(serverUrl) }
        launch {
            delay(3000)
            if (!vm.connected.value && !vm.demoMode.value) vm.startDemo()
        }
    }

    // 帧驱动独立成一个 effect：它必须无条件活着。
    // 之前和网络连接写在同一个作用域里，connect() 只要抛一次异常，
    // 同作用域的 while 循环就会被连带取消 —— 地球永远停在第一帧，像死机一样。
    // 动画是渲染器自己的事，不该被网络成败绑架。
    LaunchedEffect(Unit) {
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
                        onDragStart = { renderer?.dragStart() },
                        onDragEnd = { renderer?.dragEnd(SystemClock.uptimeMillis()) },
                        onDragCancel = { renderer?.dragEnd(SystemClock.uptimeMillis()) },
                    ) { change, dragAmount ->
                        change.consume()
                        renderer?.dragBy(dragAmount.x, dragAmount.y, SystemClock.uptimeMillis())
                    }
                },
        ) {
            // frameTick 仅作"帧已到"信号：读取它会让该 Canvas 每帧重绘，
            // 实际时间戳由 renderer 内部以 SystemClock 为准。
            @Suppress("UNUSED_EXPRESSION") frameTick
            renderer?.let { r ->
                // 顶栏高度是实测的（随字号、行数、系统字体缩放变化），
                // 球心和地图必须让开这块区域，否则大字号下球顶会被压住。
                r.resize(size.width, size.height, topInset = topBarPx, bottomInset = navBarPx)
                with(r) { frame(SystemClock.uptimeMillis()) }
            }
        }

        if (renderer == null) {
            Text(
                "正在生成地形…",
                color = Ink2,
                fontSize = 12.sp,
                fontFamily = FontFamily.Monospace,
                modifier = Modifier.align(Alignment.Center),
            )
        }

        TopBar(
            stats = stats,
            connected = connected,
            demoMode = demoMode,
            flatMode = flatMode,
            onPause = vm::togglePause,
            onProjToggle = { flatMode = renderer?.toggleFlat() ?: false },
            onServerClick = { showServerDialog = true },
            onMeasured = { topBarPx = it },
        )
        // 底部两块 HUD 必须由同一个 Row 排布：
        // 旧版各自 align(BottomStart) / align(BottomEnd) 并写死宽度，
        // 在 360dp 窄屏上事件流会伸到约 306dp，被右下角 feed 面板盖住尾部几行。
        // 交给 Row 测量后，重叠从"可能发生"变成"布局上不可能"。
        Row(
            Modifier
                .align(Alignment.BottomCenter)
                .fillMaxWidth()
                .navigationBarsPadding()
                .padding(12.dp),
            verticalAlignment = Alignment.Bottom,
        ) {
            // fill = false：事件流为空时只占内容宽度，不会拉出一整条空底板。
            EventLog(log, Modifier.weight(1f, fill = false))
            Spacer(Modifier.width(10.dp))
            FeedStatus(feeds)
        }

        if (showServerDialog) {
            ServerDialog(
                current = vm.serverUrl.collectAsState().value.ifBlank { serverUrl },
                onDismiss = { showServerDialog = false },
                onConfirm = { url ->
                    showServerDialog = false
                    vm.connect(url)
                },
                onDemo = {
                    showServerDialog = false
                    vm.startDemo()
                },
            )
        }
    }
}

@Composable
private fun ServerDialog(
    current: String,
    onDismiss: () -> Unit,
    onConfirm: (String) -> Unit,
    onDemo: () -> Unit,
) {
    var text by remember { mutableStateOf(current) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("服务器地址", fontFamily = FontFamily.Monospace) },
        text = {
            Column {
                Text("真实数据需要 Go 后端（cybermap serve）。", fontSize = 12.sp)
                Spacer(Modifier.height(8.dp))
                OutlinedTextField(
                    value = text,
                    onValueChange = { text = it },
                    singleLine = true,
                    label = { Text("ws://主机:端口/ws") },
                )
                Spacer(Modifier.height(4.dp))
                // 演示入口放在这里而不是顶栏：顶栏在竖屏上已经挤不下第四个按钮了。
                TextButton(onClick = onDemo, contentPadding = PaddingValues(0.dp)) {
                    Text("不连服务器，改用本地演示数据", color = Ink2, fontSize = 12.sp)
                }
            }
        },
        confirmButton = { TextButton(onClick = { onConfirm(text.trim()) }) { Text("连接") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("取消") } },
    )
}

/**
 * 顶栏分两行：
 *   第一行 —— 品牌 + 连接状态 + 平面/暂停两个高频开关；
 *   第二行 —— 三个统计读数。
 * 单行方案在 360dp 竖屏上必然溢出（四个 TextButton 光最小宽度就 232dp），
 * 会被 Row 直接推出屏幕，"平面"按钮根本点不到。
 */
@Composable
private fun TopBar(
    stats: StatsSnapshot,
    connected: Boolean,
    demoMode: Boolean,
    flatMode: Boolean,
    onPause: () -> Unit,
    onProjToggle: () -> Unit,
    onServerClick: () -> Unit,
    onMeasured: (Float) -> Unit,
) {
    Column(
        Modifier
            .fillMaxWidth()
            .onSizeChanged { onMeasured(it.height.toFloat()) }
            // 渐变到透明：顶栏压在地球上，硬边会把球顶切成两半。
            .background(
                Brush.verticalGradient(
                    0f to Color(0xF2080808),
                    0.72f to Color(0xC0080808),
                    1f to Color(0x00080808),
                )
            )
            .windowInsetsPadding(WindowInsets.statusBars)
            .padding(horizontal = 14.dp)
            .padding(top = 8.dp, bottom = 12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("哪吒网络安全", color = Palette.headGlow, fontSize = 15.sp, fontFamily = FontFamily.Monospace)
            Spacer(Modifier.weight(1f))
            ConnBadge(connected, demoMode, onServerClick)
            Spacer(Modifier.width(8.dp))
            ActionChip(if (flatMode) "球体" else "平面", onProjToggle)
            Spacer(Modifier.width(8.dp))
            ActionChip(if (stats.paused) "继续" else "暂停", onPause)
        }
        Spacer(Modifier.height(10.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            StatItem("事件总量", "${stats.total}")
            Spacer(Modifier.width(22.dp))
            StatItem("速率/分", "${stats.rate}")
            Spacer(Modifier.width(22.dp))
            StatItem("运行", formatUptime(stats.uptimeSec))
        }
    }
}

/** 紧凑方形状态按钮：28dp 高、无圆角，跟 Web 端顶栏同一种语言。 */
@Composable
private fun ActionChip(label: String, onClick: () -> Unit) {
    Box(
        Modifier
            .height(28.dp)
            .background(Color(0x14FFFFFF))
            .border(1.dp, Color(0x33FFFFFF))
            .clickable(onClick = onClick)
            .padding(horizontal = 10.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(label, color = Ink, fontSize = 12.sp, fontFamily = FontFamily.Monospace)
    }
}

@Composable
private fun ConnBadge(connected: Boolean, demoMode: Boolean, onClick: () -> Unit) {
    val txt: String
    val col: Color
    when {
        demoMode -> { txt = "◐ 演示"; col = Ink2 }
        connected -> { txt = "● 在线"; col = OkGreen }
        else -> { txt = "○ 离线"; col = BadRed }
    }
    Box(
        Modifier
            .height(28.dp)
            .clickable(onClick = onClick)
            .padding(horizontal = 6.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(txt, color = col, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
    }
}

@Composable
private fun StatItem(label: String, value: String) {
    Column {
        Text(label, color = Ink2, fontSize = 9.sp, fontFamily = FontFamily.Monospace)
        Text(value, color = Palette.headGlow, fontSize = 16.sp, fontFamily = FontFamily.Monospace)
    }
}

private fun formatUptime(sec: Long): String {
    val h = sec / 3600
    val m = sec % 3600 / 60
    val s = sec % 60
    return "%02d:%02d:%02d".format(h, m, s)
}

/**
 * 实时事件流：必须有底板。
 * 灰色文字直接压在灰白点阵上时，对比度不到 1.5:1，屏幕稍微一反光就完全读不出来。
 *
 * 宽度由父级 Row 的 weight 分配，这里只封顶 300dp：
 * 自己再写 fillMaxWidth 会和兄弟面板抢同一块屏幕区域。
 */
@Composable
private fun EventLog(log: List<AttackEvent>, modifier: Modifier) {
    Column(
        modifier
            .widthIn(max = 300.dp)
            .background(Color(0xD9080808))
            .border(1.dp, Color(0x1FFFFFFF))
            .padding(horizontal = 10.dp, vertical = 8.dp),
    ) {
        Text("实时事件流", color = Ink2, fontSize = 10.sp, fontFamily = FontFamily.Monospace)
        Spacer(Modifier.height(6.dp))
        for (ev in log) {
            Text(
                "${ev.raw.ifBlank { ev.src.ip }}  ${ev.src.name} → ${ev.dst.name}",
                color = Ink2,
                fontSize = 10.sp,
                fontFamily = FontFamily.Monospace,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
    }
}

@Composable
private fun FeedStatus(feeds: Map<String, FeedItem>, modifier: Modifier = Modifier) {
    Column(
        modifier
            .background(Color(0xD9080808))
            .border(1.dp, Color(0x1FFFFFFF))
            .padding(horizontal = 10.dp, vertical = 6.dp),
        horizontalAlignment = Alignment.End,
    ) {
        for ((key, f) in feeds.entries.take(4)) {
            val c = when (f.state) {
                1 -> OkGreen
                2 -> BadRed
                else -> Ink3
            }
            Text(
                "${f.name.ifBlank { key }} · ${stateName(f.state)}",
                color = c,
                fontSize = 9.sp,
                fontFamily = FontFamily.Monospace,
                maxLines = 1,
            )
        }
    }
}

private fun stateName(state: Int) = when (state) {
    1 -> "运行中"
    2 -> "异常"
    else -> "待命"
}
