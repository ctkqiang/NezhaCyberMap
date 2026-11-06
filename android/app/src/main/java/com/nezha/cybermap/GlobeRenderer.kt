/*
GlobeRenderer.kt —— 正交投影地球渲染器（Jetpack Compose Canvas 版）。

与 Web 端 renderer.mjs 同一份视觉规范：
  1. 陆地只用点阵，扫描线填充驱动；
  2. 攻击轨迹走大圆等角采样，数学上不会飞出球面；
  3. 弹道是「彗星」：外层弥散烟 + 内芯 + 白热弹头，加性混合（BlendMode.Plus），
     亮度与形状解耦——形状用相对渐变，生命周期衰减只乘在外层 alpha 上；
  4. 自转叠 27.5s 周期的 ±9% 浮动，去掉"转盘感"；
  5. 陆地点阵带稳定抖动，临边/极区的同心条带被打散成颗粒。

时间基准：事件动画一律使用本地 bornAtMs，不与服务端墙钟 ts 混算。
*/
package com.nezha.cybermap

import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.clipPath
import kotlin.math.PI
import kotlin.math.acos
import kotlin.math.asin
import kotlin.math.atan2
import kotlin.math.cos
import kotlin.math.max
import kotlin.math.min
import kotlin.math.pow
import kotlin.math.sin
import kotlin.math.sqrt
import kotlin.random.Random

// ── 相机 ──
private const val PITCH_BASE = 0.42
private const val SPIN_RATE = 0.00019
private const val SPIN_RESUME_MS = 4200L
private const val SPIN_RAMP_MS = 1400L
private const val SPIN_WOBBLE = 0.09
private const val SPIN_WOBBLE_MS = 27500.0
private const val FLING_HALF_MS = 900.0
private const val FLING_MAX = 0.018
private const val PITCH_LIMIT = 1.1

// ── 弧线（彗星弹道）──
private const val ARC_STEPS = 30
private const val ARC_TAIL = 13.0
private const val ARC_SPEED = 0.085
private const val ARC_FLIGHT_MIN_MS = 480.0
private const val ARC_FLIGHT_MAX_MS = 1500.0
private const val ARC_RETRACT_MS = 1500.0
private const val ARC_HALF_MS = 2600.0
private const val ARC_MAX = 20
private const val FADE_FLOOR = 0.04

// ── 标记 ──
private const val LAND_MIN_DEPTH = 0.15
private const val LAND_BINS = 8
private const val LAND_JITTER = 0.55
private const val SEED_MAX = 12
private const val SEED_TARGET_MAX = 6
private const val SEED_LIFE_MS = 7000L
private const val RIPPLE_MS = 2400L
private const val REPLAY_STEP_MS = 150L
private const val MAX_EVENTS = 64

// ── 调色板（与 Web 端 PALETTE 同源）──
// Mr. Robot 美学：界面本身零彩色，只有威胁数据允许有颜色。
// 地球因此是灰白点阵，天空压到近纯黑——球体必须读得出"实体"的边界，
// 否则整片大陆看起来只是浮在虚空里的点。
object Palette {
    val skyCore = Color(0xFF060606)
    val skyEdge = Color(0xFF000000)
    val globe0 = Color(0xFF262626)   // 球体受光侧（中性灰）
    val globe1 = Color(0xFF121212)   // 球体背光侧（比天空亮，行星是实心体）
    val land = Color(0xFFD6D6D6)     // 陆地点阵（灰白）
    val source = Color(0xEBFFFFFF)   // 攻击源环
    val target = Color(0xF2FF2E4D)   // 目标（蜜罐）环：唯一的常驻红
    val label = Color(0xE6E2E2E2)
    val grid = Color(0x0EFFFFFF)     // 经纬网：白 5.5%，不带色相
    val rim = Color(0x24FFFFFF)      // 地平线发丝线
    val headCore = Color(0xFFFFFFFF)
    val headGlow = Color(0xFFF0F0F0)
    val sev = listOf(
        Color(0xFF5AB0FF), // 低
        Color(0xFF3DD68C), // 中
        Color(0xFFFFAA33), // 高
        Color(0xFFFF2242), // 严重
    )
}

private fun easeOutCubic(t: Double): Double {
    val u = 1 - t
    return 1 - u * u * u
}

private fun smoothstep(t: Double): Double = t * t * (3 - 2 * t)

private data class ArcGeo(val pts: List<Proj?>, val flightMs: Double)

/**
 * GlobeRenderer 持有相机/事件/缓存状态，frame() 在 Compose Canvas 的 DrawScope 上执行。
 * 尺寸变化时调用 resize()；输入手势走 drag* 系列方法。
 */
class GlobeRenderer(private val land: FloatArray) {

    private var yaw = -1.2
    private var pitch = PITCH_BASE
    private var flingVel = 0.0
    private var spinWeight = 1.0
    private var lastDragAt = Long.MIN_VALUE
    private var lastFrameAt = 0L
    private var lastMoveAt = 0L

    private var cx = 0f
    private var cy = 0f
    private var radius = 0f

    // ── 平面地图模式（类卡巴斯基平铺视图）──
    var flatMode = false
        private set
    private var panLon = 0.0        // 视图中心经度偏移（度）
    private var mapX = 0f
    private var mapY = 0f
    private var mapW = 0f
    private var mapH = 0f
    // 弹道描边的横向跳变阈值：球体模式正无穷（永不触发），平面模式为半图宽。
    private var jumpX = Float.POSITIVE_INFINITY

    /** 切换球体/平面视图；返回切换后的模式。 */
    fun toggleFlat(): Boolean {
        flatMode = !flatMode
        return flatMode
    }

    private val events = ArrayDeque<AttackEvent>()
    private val arcCache = HashMap<AttackEvent, ArcGeo>()

    // 每个陆地点三组稳定抖动（见文件头第 5 条）。
    private val dotCount = land.size / 3
    private val landSize = FloatArray(dotCount) { 0.78f + Random.nextFloat() * 0.44f }
    private val landJx = FloatArray(dotCount) { Random.nextFloat() * 2f - 1f }
    private val landJy = FloatArray(dotCount) { Random.nextFloat() * 2f - 1f }

    // 平面模式用的经纬度点阵：从单位向量反解一次并缓存，
    // 避免每帧对上万点做 asin/atan2。
    private val landLL = FloatArray(dotCount * 2)

    init {
        val deg = (PI / 180.0).toFloat()
        var i = 0
        while (i < dotCount) {
            landLL[i * 2] = (asin(land[i * 3 + 2].coerceIn(-1f, 1f))) / deg
            landLL[i * 2 + 1] = (atan2(land[i * 3 + 1], land[i * 3])) / deg
            i++
        }
    }

    fun resize(w: Float, h: Float) {
        cx = w / 2f
        cy = h * 0.46f
        radius = min(w * 0.46f, h * 0.38f)
        // 平面地图矩形：宽受视口宽约束，高受视口约束，保持 2:1。
        val fw = min(w * 0.96f, h * 1.44f)
        mapW = fw
        mapH = fw / 2f
        mapX = (w - fw) / 2f
        mapY = (h - mapH) * 0.46f
    }

    // ── 事件 ──

    fun addEvent(ev: AttackEvent, nowMs: Long) {
        events.addLast(ev.copy(bornAtMs = nowMs))
        while (events.size > MAX_EVENTS) events.removeFirst()
    }

    fun replaceEvents(evs: List<AttackEvent>, nowMs: Long) {
        events.clear()
        arcCache.clear()
        val tail = evs.takeLast(MAX_EVENTS)
        val last = tail.size - 1
        tail.forEachIndexed { i, ev ->
            events.addLast(ev.copy(bornAtMs = nowMs - (last - i) * REPLAY_STEP_MS))
        }
    }

    // ── 手势 ──

    fun dragStart() {
        lastMoveAt = 0L
        flingVel = 0.0
    }

    fun dragBy(dx: Float, dy: Float, nowMs: Long) {
        val gap = max(1L, nowMs - lastMoveAt)
        lastMoveAt = nowMs
        if (flatMode) {
            // 平面模式：横拖平移经度（右拖 → 地表右移），回绕到 [-180, 180)；
            // 纵向拖拽与甩动惯性在平铺图上无意义，直接忽略。
            panLon = ((panLon - dx * 360.0 / mapW) % 360.0 + 540.0) % 360.0 - 180.0
            lastDragAt = nowMs
            return
        }
        yaw -= dx * 0.005
        pitch = max(-PITCH_LIMIT, min(PITCH_LIMIT, pitch - dy * 0.005))
        lastDragAt = nowMs
        val v = -(dx * 0.005) / gap
        flingVel = flingVel * 0.4 + v * 0.6
    }

    fun dragEnd(nowMs: Long) {
        lastDragAt = nowMs
        lastMoveAt = 0L
        flingVel = max(-FLING_MAX, min(FLING_MAX, flingVel))
        spinWeight = 0.0
    }

    // ── 帧 ──

    /** 绘制一帧；nowMs 使用 SystemClock.uptimeMillis() 时基。 */
    fun DrawScope.frame(nowMs: Long) {
        if (radius <= 0f) return
        val dt = if (lastFrameAt == 0L) 16L else (nowMs - lastFrameAt).coerceIn(1L, 50L)
        lastFrameAt = nowMs
        stepCamera(dt.toDouble(), nowMs)

        paintSky()
        if (flatMode) {
            val proj = FlatProjector(mapX, mapY, mapW, mapH, panLon)
            jumpX = mapW / 2f
            paintMapBg()
            paintGraticuleFlat(proj)
            paintLandFlat(proj)
            // 战术覆盖层压在地形之上、弹道之下：仪器边框属于界面，弹道属于数据。
            paintTacticalFrame(proj)
            paintTrails(proj, nowMs)
            paintSeeds(proj, nowMs)
        } else {
            val proj = Projector(cx, cy, radius, yaw, pitch)
            jumpX = Float.POSITIVE_INFINITY
            paintSphere()
            paintGraticule(proj)
            paintLand(proj)
            paintLight()
            paintTrails(proj, nowMs)
            paintSeeds(proj, nowMs)
        }
    }

    private fun stepCamera(dt: Double, nowMs: Long) {
        // 平面模式没有自转与惯性：地图是铺开的仪表板，静止才是正确的"性格"。
        if (flatMode) return
        if (flingVel != 0.0) {
            yaw -= flingVel * dt
            flingVel *= 0.5.pow(dt / FLING_HALF_MS)
            if (kotlin.math.abs(flingVel) < 1e-5) flingVel = 0.0
        }
        spinWeight = if (nowMs - lastDragAt > SPIN_RESUME_MS) {
            min(1.0, spinWeight + dt / SPIN_RAMP_MS)
        } else 0.0
        val wobble = 1 + SPIN_WOBBLE * sin(nowMs / SPIN_WOBBLE_MS * PI * 2)
        yaw -= SPIN_RATE * smoothstep(spinWeight) * wobble * dt
    }

    private fun DrawScope.paintSky() {
        drawRect(Brush.radialGradient(listOf(Palette.skyCore, Palette.skyEdge), center = Offset(cx, cy), radius = max(size.width, size.height)))
    }

    // ── 平面模式绘制 ──

    /** 平面地图底板：比天空略亮，顶部一层极淡受光、底部收暗，外描一道白色细边。 */
    private fun DrawScope.paintMapBg() {
        drawRect(Palette.globe1, topLeft = Offset(mapX, mapY), size = androidx.compose.ui.geometry.Size(mapW, mapH))
        // 受光/收暗必须是中性白与纯黑：旧版用 0x0DFF8484（带红的高光），
        // 在灰白点阵上会烧出一层脏粉色雾，画面立刻从"仪表"退化成"滤镜"。
        drawRect(
            brush = Brush.verticalGradient(
                0f to Color(0x08FFFFFF), 0.5f to Color.Transparent, 1f to Color(0x42000000),
                startY = mapY, endY = mapY + mapH,
            ),
            topLeft = Offset(mapX, mapY),
            size = androidx.compose.ui.geometry.Size(mapW, mapH),
        )
        drawRect(Color(0x24FFFFFF), topLeft = Offset(mapX, mapY), size = androidx.compose.ui.geometry.Size(mapW, mapH), style = Stroke(1f))
    }

    /** 平面经纬网：经纬各 30° 的直线网格。 */
    private fun DrawScope.paintGraticuleFlat(proj: IProjector) {
        clipPath(Path().apply { addRect(androidx.compose.ui.geometry.Rect(mapX, mapY, mapX + mapW, mapY + mapH)) }) {
            for (lon in -150..150 step 30) {
                val x = proj.project(0.0, lon.toDouble())!!.x
                drawLine(Palette.grid, Offset(x, mapY), Offset(x, mapY + mapH), strokeWidth = 1f)
            }
            for (lat in -60..60 step 30) {
                val y = proj.project(lat.toDouble(), 0.0)!!.y
                drawLine(Palette.grid, Offset(mapX, y), Offset(mapX + mapW, y), strokeWidth = 1f)
            }
        }
    }

    /**
     * 平面陆地点阵：走预解算的经纬度数组，无深度衰减与临边剔除，
     * 所有点满亮满尺寸；只保留少量抖动打散经纬网格感。
     */
    private fun DrawScope.paintLandFlat(proj: IProjector) {
        if (landLL.isEmpty()) return
        // 0.62 而非 0.9：等距圆柱投影下点间距就是 base/0.9，
        // 点径占到间距的九成时相邻点会粘连，整个欧亚大陆糊成一块实心白。
        val base = max(1f, mapW / 360f * LAND_STEP_DEG.toFloat() * 0.62f)
        val jf = 0.35f * base
        clipPath(Path().apply { addRect(androidx.compose.ui.geometry.Rect(mapX, mapY, mapX + mapW, mapY + mapH)) }) {
            var idx = 0
            var i = 0
            while (i < landLL.size) {
                val p = proj.project(landLL[i].toDouble(), landLL[i + 1].toDouble())!!
                val s = base * landSize[idx]
                val x = p.x + landJx[idx] * jf
                val y = p.y + landJy[idx] * jf
                drawRect(
                    color = Palette.land.copy(alpha = 0.88f),
                    topLeft = Offset(x - s * 0.5f, y - s * 0.5f),
                    size = androidx.compose.ui.geometry.Size(s, s),
                )
                i += 2
                idx++
            }
        }

        // 极区淡出：等距圆柱投影下南极圈覆盖所有经度、北极圈陆地几乎首尾相接，
        // 这是投影的固有特征而非采样错误。按海图惯例上下各 13% 用底色渐隐，
        // 并把中点透明度推到 0.92，否则线性渐变在带中间只剩一半遮罩，
        // 那条横贯全图的白带依然抢眼。
        val fadeH = mapH * 0.13f
        drawRect(
            brush = Brush.verticalGradient(
                0f to Color(0xFF101010), 0.35f to Color(0xEB101010), 1f to Color(0x00101010),
                startY = mapY, endY = mapY + fadeH,
            ),
            topLeft = Offset(mapX, mapY),
            size = androidx.compose.ui.geometry.Size(mapW, fadeH),
        )
        drawRect(
            brush = Brush.verticalGradient(
                0f to Color(0x00101010), 0.65f to Color(0xEB101010), 1f to Color(0xFF101010),
                startY = mapY + mapH - fadeH, endY = mapY + mapH,
            ),
            topLeft = Offset(mapX, mapY + mapH - fadeH),
            size = androidx.compose.ui.geometry.Size(mapW, fadeH),
        )
    }

    /**
     * 平面地图的战术覆盖层：扫描线 + 四角 L 形括号 + 四边经纬刻度。
     *
     * 借三角洲/军工 HUD 的语言：
     *   - 扫描线给 CRT 质感，必须极轻（alpha ≈ 0.02），重了就成廉价科幻贴图；
     *   - 四角括号界定"仪表区域"，比整圈描边更像是被框选出来的；
     *   - 四边刻度是这一层里唯一有功能的东西：没有度数，
     *     平铺世界读起来只是一张带边框的壁纸，而不是一张可以定位的海图。
     */
    private fun DrawScope.paintTacticalFrame(proj: IProjector) {
        // 扫描线（压在大陆之上：CRT 的覆盖感来自"线在图像前面"）。
        val scan = Color(0x05FFFFFF)
        var sy = mapY + 2f
        while (sy < mapY + mapH) {
            drawLine(scan, Offset(mapX, sy), Offset(mapX + mapW, sy), strokeWidth = 1f)
            sy += 4f
        }

        // 四角 L 形括号。
        val arm = (mapW * 0.035f).coerceIn(14f, 26f)
        val bracket = Color(0x6BFFFFFF)
        val x0 = mapX
        val y0 = mapY
        val x1 = mapX + mapW
        val y1 = mapY + mapH
        val corners = arrayOf(
            floatArrayOf(x0, y0, 1f, 1f),
            floatArrayOf(x1, y0, -1f, 1f),
            floatArrayOf(x0, y1, 1f, -1f),
            floatArrayOf(x1, y1, -1f, -1f),
        )
        for (c in corners) {
            val path = Path()
            path.moveTo(c[0], c[1] + c[3] * arm)
            path.lineTo(c[0], c[1])
            path.lineTo(c[0] + c[2] * arm, c[1])
            drawPath(path, bracket, style = Stroke(width = 1.5f))
        }

        // 经纬刻度：每 15° 一短划、30° 一长划，画在边框之外，不压地形。
        val tick = Color(0x4DFFFFFF)
        val minor = 3f
        val major = 6f
        for (lon in -180..180 step 15) {
            val x = proj.project(0.0, lon.toDouble())!!.x
            if (x < mapX - 1f || x > mapX + mapW + 1f) continue
            val len = if (lon % 30 == 0) major else minor
            drawLine(tick, Offset(x, mapY - 3f), Offset(x, mapY - 3f - len), strokeWidth = 1f)
            drawLine(tick, Offset(x, mapY + mapH + 3f), Offset(x, mapY + mapH + 3f + len), strokeWidth = 1f)
        }
        for (lat in -75..75 step 15) {
            val y = proj.project(lat.toDouble(), 0.0)!!.y
            if (y < mapY - 1f || y > mapY + mapH + 1f) continue
            val len = if (lat % 30 == 0) major else minor
            drawLine(tick, Offset(mapX - 3f, y), Offset(mapX - 3f - len, y), strokeWidth = 1f)
            drawLine(tick, Offset(mapX + mapW + 3f, y), Offset(mapX + mapW + 3f + len, y), strokeWidth = 1f)
        }
    }

    private fun DrawScope.paintSphere() {
        drawCircle(
            brush = Brush.radialGradient(
                0f to Palette.globe0, 1f to Palette.globe1,
                center = Offset(cx - radius * 0.32f, cy - radius * 0.34f),
                radius = radius * 1.02f,
            ),
            radius = radius,
            center = Offset(cx, cy),
        )
    }

    private fun DrawScope.paintGraticule(proj: IProjector) {
        clipPath(Path().apply { addOval(androidx.compose.ui.geometry.Rect(Offset(cx - radius, cy - radius), radius * 2f)) }) {
            for (lon in -180 until 180 step 30) {
                strokeCurve(proj, -90.0, 90.0, 5.0) { lat -> proj.project(lat, lon.toDouble()) }
            }
            for (lat in -60..60 step 30) {
                strokeCurve(proj, -180.0, 180.0, 5.0) { lon -> proj.project(lat.toDouble(), lon) }
            }
        }
    }

    private inline fun DrawScope.strokeCurve(
        proj: IProjector, from: Double, to: Double, step: Double,
        crossinline sample: (Double) -> Proj?,
    ) {
        val path = Path()
        var pen = false
        var a = from
        while (a <= to) {
            val p = sample(a)
            if (p == null) {
                pen = false
            } else if (!pen) {
                path.moveTo(p.x, p.y); pen = true
            } else {
                path.lineTo(p.x, p.y)
            }
            a += step
        }
        drawPath(path, Palette.grid, style = Stroke(width = 1f))
    }

    private fun DrawScope.paintLand(proj: IProjector) {
        if (land.isEmpty()) return
        val base = max(1f, radius * (LAND_STEP_DEG * PI / 180).toFloat() * 0.78f)
        val span = 1.0 - LAND_MIN_DEPTH

        clipPath(Path().apply { addOval(androidx.compose.ui.geometry.Rect(Offset(cx - radius, cy - radius), radius * 2f)) }) {
            var idx = 0
            var i = 0
            while (i < land.size) {
                val p = proj.project(land[i].toDouble(), land[i + 1].toDouble(), land[i + 2].toDouble())
                i += 3
                if (p == null || p.depth < LAND_MIN_DEPTH) { idx++; continue }
                val z = p.depth
                val jf = ((1 - z) * LAND_JITTER * base).toFloat()
                val x = p.x + landJx[idx] * jf
                val y = p.y + landJy[idx] * jf
                val s = base * (0.55 + 0.45 * z).toFloat() * landSize[idx]
                idx++
                // 深度分桶：把 globalAlpha 状态切换从每点一次降到每档一次。
                val b = min(LAND_BINS - 1, (((z - LAND_MIN_DEPTH) / span) * LAND_BINS).toInt())
                val zMid = LAND_MIN_DEPTH + (b + 0.5) * span / LAND_BINS
                drawRect(
                    color = Palette.land.copy(alpha = (0.10 + 0.90 * zMid).toFloat().coerceIn(0f, 1f)),
                    topLeft = Offset(x - s * 0.5f, y - s * 0.5f),
                    size = androidx.compose.ui.geometry.Size(s, s),
                )
            }
        }
    }

    private fun DrawScope.paintLight() {
        // 受光侧在左上：右下叠一层径向阴影，临边再压暗一圈，构成球面纵深。
        drawCircle(
            brush = Brush.radialGradient(
                0f to Color.Transparent, 0.45f to Color(0x47000000), 1f to Color(0x99000000),
                center = Offset(cx + radius * 0.55f, cy + radius * 0.5f),
                radius = radius * 1.42f,
            ),
            radius = radius,
            center = Offset(cx, cy),
        )
        drawCircle(
            brush = Brush.radialGradient(
                0f to Color.Transparent, 1f to Color(0x52000000),
                center = Offset(cx, cy),
                radius = radius,
            ),
            radius = radius,
            center = Offset(cx, cy),
        )
        // 轮廓发丝线：一条技术制图的轮廓，不是辉光。
        // 点阵球没有实体填充的边缘，缺了这条线，行星轮廓就"咬"不住背景，
        // 整块大陆看起来像浮在虚空里的碎片。
        drawCircle(color = Palette.rim, radius = radius - 0.5f, center = Offset(cx, cy), style = Stroke(1f))
    }

    // ── 弹道 ──

    private fun arcGeo(proj: IProjector, ev: AttackEvent): ArcGeo? {
        arcCache[ev]?.let { return it }
        val a = toVec(ev.src.lat, ev.src.lon)
        val b = toVec(ev.dst.lat, ev.dst.lon)
        val dot = (a[0] * b[0] + a[1] * b[1] + a[2] * b[2]).coerceIn(-1.0, 1.0)
        val theta = acos(dot)
        val cosT = cos(theta)
        var ux = b[0] - a[0] * cosT
        var uy = b[1] - a[1] * cosT
        var uz = b[2] - a[2] * cosT
        var m = sqrt(ux * ux + uy * uy + uz * uz)
        if (m < 1e-9) {
            if (kotlin.math.abs(a[2]) < 0.9) { ux = a[1]; uy = -a[0]; uz = 0.0 }
            else { ux = 0.0; uy = a[2]; uz = -a[1] }
            m = sqrt(ux * ux + uy * uy + uz * uz).takeIf { it > 0 } ?: 1.0
        }
        ux /= m; uy /= m; uz /= m

        val pts = ArrayList<Proj?>(ARC_STEPS + 1)
        for (i in 0..ARC_STEPS) {
            val phi = theta * i / ARC_STEPS
            val c = cos(phi); val s = sin(phi)
            pts.add(proj.project(a[0] * c + ux * s, a[1] * c + uy * s, a[2] * c + uz * s))
        }
        val deg = theta * 180 / PI
        val flight = min(ARC_FLIGHT_MAX_MS, max(ARC_FLIGHT_MIN_MS, deg / ARC_SPEED))
        return ArcGeo(pts, flight).also { arcCache[ev] = it }
    }

    private fun lerpPt(pts: List<Proj?>, f: Double): Proj? {
        val last = pts.size - 1
        if (f <= 0) return pts[0]
        if (f >= last) return pts[last]
        val i = f.toInt()
        val k = f - i
        val a = pts[i] ?: return null
        val b = pts[i + 1] ?: return null
        return Proj(
            x = a.x + (b.x - a.x) * k.toFloat(),
            y = a.y + (b.y - a.y) * k.toFloat(),
            depth = a.depth + (b.depth - a.depth) * k,
        )
    }

    private fun DrawScope.paintTrails(proj: IProjector, nowMs: Long) {
        // 同一起终国家对只留最新一条，避免高重复源刷屏。
        val seen = HashSet<String>()
        val active = ArrayList<AttackEvent>()
        for (ev in events.reversed()) {
            val key = "${ev.src.code}->${ev.dst.code}"
            if (seen.add(key)) {
                active.add(ev)
                if (active.size >= ARC_MAX) break
            }
        }

        // 球体裁圆、平面裁矩形；弹道绝不能越出地图边界。
        val clipP = Path().apply {
            if (flatMode) addRect(androidx.compose.ui.geometry.Rect(mapX, mapY, mapX + mapW, mapY + mapH))
            else addOval(androidx.compose.ui.geometry.Rect(Offset(cx - radius, cy - radius), radius * 2f))
        }
        clipPath(clipP) {
            for (ev in active) {
                val age = (nowMs - ev.bornAtMs).toDouble()
                if (age < 0) continue
                val fade = 0.5.pow(age / ARC_HALF_MS)
                if (fade < FADE_FLOOR) continue
                val geo = arcGeo(proj, ev) ?: continue

                val travel = min(1.0, age / geo.flightMs)
                val fHead = travel * ARC_STEPS
                val retract = min(1.0, max(0.0, (age - geo.flightMs) / ARC_RETRACT_MS))
                val absorbed = smoothstep(retract)
                val fTail = min(fHead, fHead - ARC_TAIL * (1 - absorbed))

                val head = lerpPt(geo.pts, fHead) ?: continue
                val tail = lerpPt(geo.pts, fTail) ?: continue
                val color = Palette.sev.getOrElse(ev.sev) { Palette.sev[0] }
                val thick = ev.sev >= 3

                val launch = smoothstep(min(1.0, age / 110.0))
                val dk = 0.34 + 0.66 * head.depth
                val live = fade * (1 - absorbed) * dk * launch
                if (live > 0.02) {
                    drawComet(geo.pts, fTail, fHead, tail, head, color, thick, live.toFloat(), dk.toFloat())
                }
                if (travel >= 1 && age < geo.flightMs + RIPPLE_MS) {
                    val dst = proj.project(ev.dst.lat, ev.dst.lon) ?: head
                    ripple(dst.x, dst.y, ((age - geo.flightMs) / RIPPLE_MS).toFloat(), Palette.target, if (thick) 9f else 6f)
                }
            }
        }
    }

    /** 彗星三层：外层弥散烟 → 近弹头内芯 → 白热弹头；整层加性混合。 */
    private fun DrawScope.drawComet(
        pts: List<Proj?>, fTail: Double, fHead: Double,
        tail: Proj, head: Proj, color: Color, thick: Boolean,
        live: Float, dk: Float,
    ) {
        val span = fHead - fTail
        if (span > 0.6) {
            val widePath = tracePath(pts, fTail, fHead)
            drawPath(
                path = widePath,
                brush = Brush.linearGradient(
                    0f to color.copy(alpha = 0f),
                    0.45f to color.copy(alpha = 0.10f * live),
                    1f to color.copy(alpha = 0.30f * live),
                    start = Offset(tail.x, tail.y),
                    end = Offset(head.x, head.y),
                ),
                style = Stroke(width = (if (thick) 10f else 7f) * (0.70f + 0.30f * dk)),
                blendMode = BlendMode.Plus,
            )

            val coreFrom = fTail + span * 0.40
            val corePt = lerpPt(pts, coreFrom)
            if (corePt != null) {
                drawPath(
                    path = tracePath(pts, coreFrom, fHead),
                    brush = Brush.linearGradient(
                        0f to color.copy(alpha = 0f),
                        0.5f to color.copy(alpha = 0.30f * live),
                        1f to color.copy(alpha = 0.95f * live),
                        start = Offset(corePt.x, corePt.y),
                        end = Offset(head.x, head.y),
                    ),
                    style = Stroke(width = if (thick) 1.9f else 1.35f),
                    blendMode = BlendMode.Plus,
                )
            }
        }

        val glowR = (if (thick) 13f else 10f) * (0.62f + 0.38f * dk)
        drawCircle(
            brush = Brush.radialGradient(
                0f to Palette.headGlow.copy(alpha = 0.62f * live),
                0.22f to color.copy(alpha = 0.44f * live),
                0.55f to color.copy(alpha = 0.14f * live),
                1f to color.copy(alpha = 0f),
                center = Offset(head.x, head.y),
                radius = glowR,
            ),
            radius = glowR,
            center = Offset(head.x, head.y),
            blendMode = BlendMode.Plus,
        )
        drawCircle(
            color = Palette.headCore.copy(alpha = min(1f, live * 1.4f)),
            radius = if (thick) 2.0f else 1.5f,
            center = Offset(head.x, head.y),
            blendMode = BlendMode.Plus,
        )
    }

    /** 把 [from, to] 分数区间写入路径；遇背面点断笔。
     * 平面模式下相邻点横向跳变超过半图宽（跨日界线）同样断笔，
     * 否则一条弹道会拉成横穿整张地图的水平线。 */
    private fun tracePath(pts: List<Proj?>, from: Double, to: Double): Path {
        val path = Path()
        var pen = false
        var lastX = 0f
        fun push(p: Proj?) {
            if (p == null) { pen = false; return }
            if (pen && kotlin.math.abs(p.x - lastX) > jumpX) pen = false
            if (!pen) { path.moveTo(p.x, p.y); pen = true } else path.lineTo(p.x, p.y)
            lastX = p.x
        }
        push(lerpPt(pts, from))
        val last = min(pts.size - 1.0, kotlin.math.floor(to)).toInt()
        var i = kotlin.math.ceil(from).toInt()
        while (i <= last) {
            if (i > from) push(pts[i])
            i++
        }
        if (to > last) push(lerpPt(pts, to))
        return path
    }

    // ── 标记 ──

    private fun DrawScope.paintSeeds(proj: IProjector, nowMs: Long) {
        val recent = events.takeLast(SEED_MAX)
        val newestFrom = recent.size - SEED_TARGET_MAX
        for ((i, ev) in recent.withIndex()) {
            val age = (nowMs - ev.bornAtMs).coerceAtLeast(0L)
            val life = max(0.0, 1.0 - age.toDouble() / SEED_LIFE_MS)
            val birth = smoothstep(min(1.0, age.toDouble() / 240.0)).toFloat()

            proj.project(ev.src.lat, ev.src.lon)?.let { s ->
                val k = ((0.68 + 0.32 * s.depth) * (0.80 + 0.20 * birth)).toFloat()
                val a = ((0.35 + 0.65 * life) * k * birth).toFloat().coerceIn(0f, 1f)
                drawCircle(Palette.source.copy(alpha = 0.5f * a), radius = 3.4f * k, center = Offset(s.x, s.y), style = Stroke(1f))
                drawCircle(Palette.headCore.copy(alpha = a), radius = 1.5f * k, center = Offset(s.x, s.y))
                if (age < RIPPLE_MS) ripple(s.x, s.y, age.toFloat() / RIPPLE_MS, Palette.source, 5f)
            }
            proj.project(ev.dst.lat, ev.dst.lon)?.let { d ->
                val dbirth = smoothstep(min(1.0, age.toDouble() / 300.0)).toFloat()
                val k = (0.68 + 0.32 * d.depth).toFloat() * (0.78f + 0.22f * dbirth)
                drawCircle(Palette.target.copy(alpha = dbirth), radius = 2.6f * k, center = Offset(d.x, d.y), style = Stroke(1f))
                if (age < RIPPLE_MS && ev.sev >= 2) ripple(d.x, d.y, age.toFloat() / RIPPLE_MS, Palette.target, 6f)
            }
        }
    }

    private fun DrawScope.ripple(x: Float, y: Float, p: Float, color: Color, spread: Float) {
        if (p >= 1f) return
        val nd = if (radius > 0) kotlin.math.hypot(x - cx, y - cy) / radius else 0f
        // 平面模式没有地平线，恒为满亮。
        val limb = if (flatMode) 1f else min(1f, max(0f, (1.02f - nd) / 0.24f))
        if (limb <= 0.01f) return
        val r = 1.5f + easeOutCubic(p.toDouble()).toFloat() * spread
        val rise = smoothstep(min(1f, p / 0.18f).toDouble()).toFloat()
        val fade = (1 - p).toDouble().pow(2.0).toFloat() * limb * rise
        drawCircle(color.copy(alpha = 0.5f * fade), radius = r, center = Offset(x, y), style = Stroke(1f))
    }
}
