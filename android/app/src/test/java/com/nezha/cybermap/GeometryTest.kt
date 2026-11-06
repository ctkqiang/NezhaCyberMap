/*
GeometryTest.kt —— 渲染几何、投影、协议的 JVM 断言测试。

为什么要有这个文件：
  这台机器上安卓模拟器起不来（Hypervisor.framework 被系统禁用），
  所以"跑起来看看"这条验证路径被封死。但布局是否把球顶压到顶栏底下、
  平面图在竖屏是否真的铺开、投影是否把东画在右边——这些全是纯数值问题，
  可以在 JVM 上直接断言。

  这些断言不是在复述实现，它们锁定的是"用户可见的正确性"：
  任何一次 resize 改动把球体推出安全区，这里会红。
*/
package com.nezha.cybermap

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class GeometryTest {

    // ── resize：球体安全区 ──

    /**
     * 竖屏（1080×2340，顶栏 321px，导航条 48px）。
     *
     * 旧版用整屏高度算半径，横屏时球顶会钻进顶栏；竖屏虽然侥幸没撞上，
     * 但安全区的边界必须被显式守住，不能靠巧合。
     */
    @Test
    fun portraitSphereClearsTopBarAndNavBar() {
        val r = GlobeRenderer(FloatArray(0))
        r.resize(w = 1080f, h = 2340f, topInset = 321f, bottomInset = 48f)

        assertEquals(540f, r.cx, 0.01f)
        // cy = 321 + (2340-321-48)*0.46 = 1227.66
        assertEquals(1227.66f, r.cy, 0.05f)
        // radius = min(1080*0.46, 1971*0.36) = 496.8
        assertEquals(496.8f, r.radius, 0.05f)

        val top = r.cy - r.radius
        val bottom = r.cy + r.radius
        assertTrue("球顶 $top 压进了顶栏下沿 321", top >= 321f)
        assertTrue("球底 $bottom 压进了导航条上沿 2292", bottom <= 2340f - 48f)
        // 球横向几乎占满：直径至少是屏宽的 90%，否则手机上看着像个点。
        assertTrue("球体直径 ${r.radius * 2} 小于屏宽 90%", r.radius * 2f >= 1080f * 0.9f)
    }

    /**
     * 横屏（2340×1080，顶栏 160px，导航条 48px）—— 这是旧版真正翻车的场景。
     * 顶部可用高度只有 872px，半径必须按 872 收缩，而不是按整屏 1080。
     */
    @Test
    fun landscapeSphereClearsTopBarAndNavBar() {
        val r = GlobeRenderer(FloatArray(0))
        r.resize(w = 2340f, h = 1080f, topInset = 160f, bottomInset = 48f)

        assertEquals(1170f, r.cx, 0.01f)
        assertEquals(596f, r.cy, 0.05f)
        // radius = min(2340*0.46, 872*0.42) = 366.24
        assertEquals(366.24f, r.radius, 0.05f)

        assertTrue("横屏球顶 ${r.cy - r.radius} 钻进顶栏 160", r.cy - r.radius >= 160f)
        assertTrue("横屏球底 ${r.cy + r.radius} 越过 1032", r.cy + r.radius <= 1080f - 48f)
    }

    // ── resize：平面地图 ──

    /**
     * 竖屏平面图必须横向溢出屏幕（真正的"铺开的海图"），
     * 而不是缩成一条 2:1 的邮票。
     */
    @Test
    fun portraitFlatMapSpillsHorizontallyButKeepsVerticalSafeArea() {
        val r = GlobeRenderer(FloatArray(0))
        r.resize(w = 1080f, h = 2340f, topInset = 321f, bottomInset = 48f)
        r.toggleFlat()

        // mapH = min(1971*0.56, 1080*0.92) = 993.6；mapW = 2*mapH = 1987.2
        assertEquals(993.6f, r.mapH, 0.05f)
        assertEquals(1987.2f, r.mapW, 0.05f)

        assertTrue("平面图宽 ${r.mapW} 没有溢出屏宽 1080，竖屏会变成邮票", r.mapW > 1080f)
        assertTrue("平面图高 ${r.mapH} 不足屏宽一半", r.mapH >= 1080f * 0.5f)
        assertTrue("地图顶 ${r.mapY} 越过顶栏 321", r.mapY >= 321f)
        assertTrue("地图底 ${r.mapY + r.mapH} 越过 2292", r.mapY + r.mapH <= 2340f - 48f)
        // 居中对齐：左右溢出的量必须对称，否则平移手感是歪的。
        assertEquals(1080f / 2f, r.mapX + r.mapW / 2f, 0.05f)
    }

    /** 横屏平面图应完整落在屏幕内 —— 一眼看完，不需要平移。 */
    @Test
    fun landscapeFlatMapStaysInsideScreen() {
        val r = GlobeRenderer(FloatArray(0))
        r.resize(w = 2340f, h = 1080f, topInset = 160f, bottomInset = 48f)
        r.toggleFlat()

        assertTrue("地图左 ${r.mapX} 越界", r.mapX >= 0f)
        assertTrue("地图右 ${r.mapX + r.mapW} 越界", r.mapX + r.mapW <= 2340f)
        assertTrue("地图顶 ${r.mapY} 越过顶栏 160", r.mapY >= 160f)
        assertTrue("地图底 ${r.mapY + r.mapH} 越过 1032", r.mapY + r.mapH <= 1080f - 48f)
        // 横屏时高度也要够看：至少屏高的 55%。
        assertTrue("地图高 ${r.mapH} 不足屏高 55%", r.mapH >= 1080f * 0.55f)
    }

    /**
     * 等距圆柱投影是刚性的 2:1。一旦有人在 resize 里单独改宽或高，
     * 经纬网与陆地点阵会沿一个方向被拉伸，整张图直接变形。
     */
    @Test
    fun flatMapKeepsTwoToOneAspectInBothOrientations() {
        val portrait = GlobeRenderer(FloatArray(0)).apply {
            resize(1080f, 2340f, 321f, 48f)
        }
        val landscape = GlobeRenderer(FloatArray(0)).apply {
            resize(2340f, 1080f, 160f, 48f)
        }
        assertEquals(2f, portrait.mapW / portrait.mapH, 0.0005f)
        assertEquals(2f, landscape.mapW / landscape.mapH, 0.0005f)
    }

    // ── FlatProjector ──

    /**
     * 全球覆盖：[-180,180] × [-90,90] 必须正好铺满整个投影矩形。
     *
     * 注意接缝语义：±180° 是同一条经线，统一归到左边缘 x=0（于是海图左右可无缝衔接）；
     * 矩形右边缘由紧贴接缝西侧的经度逼近，而不是由 +180° 自己顶到 360。
     */
    @Test
    fun flatProjectorSpansTheWholeWorld() {
        val p = FlatProjector(0f, 0f, 360f, 180f, panDeg = 0.0)

        assertEquals(0f, p.project(0.0, -180.0).x, 0.01f)
        assertEquals(360f, p.project(0.0, 179.999999).x, 0.02f)
        assertEquals(180f, p.project(0.0, 0.0).x, 0.01f)
        assertEquals(270f, p.project(0.0, 90.0).x, 0.01f)
        assertEquals(0f, p.project(90.0, 0.0).y, 0.01f)
        assertEquals(180f, p.project(-90.0, 0.0).y, 0.01f)
        assertEquals(90f, p.project(0.0, 0.0).y, 0.01f)
    }

    /** 日界线两侧是同一条经线，必须投到同一个 x，否则海图会撕开一道缝。 */
    @Test
    fun flatProjectorWrapsAtAntimeridian() {
        val p = FlatProjector(0f, 0f, 360f, 180f, panDeg = 0.0)
        assertEquals(p.project(0.0, -180.0).x, p.project(0.0, 180.0).x, 0.01f)
    }

    /**
     * 平移移动的是"接缝"而不是地图内容：
     * pan=90 时东经 90° 恰好落在矩形正中，而原来的本初子午线移到了左侧。
     *
     * 这条断言同时解释了 renderer 为什么必须每帧 arcCache.clear()：
     * 同一组经纬度在不同 pan 下投出来的 x 不同，缓存的弹道点会立刻过期。
     */
    @Test
    fun flatPanMovesTheSeamNotTheMap() {
        val base = FlatProjector(0f, 0f, 360f, 180f, panDeg = 0.0)
        val panned = FlatProjector(0f, 0f, 360f, 180f, panDeg = 90.0)

        assertEquals(180f, panned.project(0.0, 90.0).x, 0.01f)
        assertEquals(90f, panned.project(0.0, 0.0).x, 0.01f)
        assertTrue(
            "平移后本初子午线的 x 没有变化，说明 pan 没生效",
            kotlin.math.abs(base.project(0.0, 0.0).x - panned.project(0.0, 0.0).x) > 1f,
        )
    }

    // ── Projector（球体）──

    /** 正对观察者的点可见，背面的点必须被剔除（否则会画到球前面）。 */
    @Test
    fun sphereProjectorCullsBackHemisphere() {
        val p = Projector(cx = 0f, cy = 0f, r = 100f, yaw = 0.0, pitch = 0.0)
        assertNotNull("正面的本初子午线不应被剔除", p.project(0.0, 0.0))
        assertNull("背面的对跖点必须被剔除", p.project(0.0, 180.0))
        assertNull("北极（pitch=0 时贴地平线以下）应被剔除", p.project(-90.0, 0.0))
    }

    /** 屏幕朝向约定：东在右，北在上。反了整张地图就是镜像的。 */
    @Test
    fun sphereProjectorKeepsEastOnTheRightAndNorthUp() {
        val p = Projector(cx = 0f, cy = 0f, r = 100f, yaw = 0.0, pitch = 0.0)
        val east = p.project(0.0, 30.0)!!
        val north = p.project(60.0, 0.0)!!
        assertTrue("东经 30° 被画到了左边（x=${east.x}）", east.x > 0f)
        assertTrue("北纬 60° 被画到了下方（y=${north.y}）", north.y < 0f)
    }

    // ── 手势：平面平移回绕 ──

    /** 平移后的 panLon 必须始终落在 [-180, 180)，否则经度公式会漂向无穷。 */
    @Test
    fun flatDragWrapsPanLongitudeIntoHalfTurn() {
        val r = GlobeRenderer(FloatArray(0))
        r.resize(1080f, 2340f, 321f, 48f)
        r.toggleFlat()

        // 往右拖满半张图 = 经度平移 180°。
        r.dragStart()
        r.dragBy(dx = r.mapW / 2f, dy = 0f, nowMs = 1L)
        assertTrue("panLon=${r.panLon} 越出 [-180,180)", r.panLon in -180.0..180.0)

        // 连续猛拖十次，仍然不许跑出区间。
        repeat(10) { r.dragBy(dx = r.mapW * 3f, dy = 0f, nowMs = 2L + it) }
        assertTrue("连续平移后 panLon=${r.panLon} 越出 [-180,180)", r.panLon in -180.0..180.0)
    }

    // ── 协议解析 ──

    /** 缺 lat/lon 的坐标必须是 0.0 而不是 NaN —— NaN 进 asin() 会让整帧黑屏。 */
    @Test
    fun missingCoordinatesNeverProduceNaN() {
        val msg = Protocol.parse(
            """{"type":"event","event":{"ts":1,"src":{"ip":"1.1.1.1"},"dst":{},"sev":0,"feed":"x","raw":"y"}}""",
            nowMs = 1234L,
        )
        val ev = (msg as ServerMsg.Event).event
        assertTrue("src.lat=${ev.src.lat} 不是有限数", ev.src.lat.isFinite())
        assertTrue("src.lon=${ev.src.lon} 不是有限数", ev.src.lon.isFinite())
        assertTrue("dst.lat=${ev.dst.lat} 不是有限数", ev.dst.lat.isFinite())
        assertEquals(0.0, ev.src.lat, 0.0)
        assertEquals(0.0, ev.src.lon, 0.0)
        assertEquals(1234L, ev.bornAtMs)
    }

    /** 严重度越界会被夹到 0..3；否则调色板兜底成低危蓝，严重攻击看起来像无害扫描。 */
    @Test
    fun severityIsClampedIntoPaletteRange() {
        fun sevOf(raw: Int): Int {
            val msg = Protocol.parse(
                """{"type":"event","event":{"ts":1,"src":{},"dst":{},"sev":$raw,"feed":"","raw":""}}""",
                nowMs = 1L,
            )
            return (msg as ServerMsg.Event).event.sev
        }
        assertEquals(3, sevOf(9))
        assertEquals(0, sevOf(-5))
        assertEquals(2, sevOf(2))
    }

    /** 统计里的定长数组：后端是 [4]int / [30]int，客户端数组长度必须一致。 */
    @Test
    fun statsArraysStayFixedLength() {
        val msg = Protocol.parse(
            """{"type":"stats","stats":{"total":7,"rate":3,"uptime":99,"paused":true,
               "severity":[1,2],"spark":[1,2,3]}}""",
            nowMs = 1L,
        )
        val s = (msg as ServerMsg.Stats).stats
        assertEquals(4, s.severity.size)
        assertEquals(30, s.spark.size)
        assertEquals(1, s.severity[0])
        assertEquals(0, s.severity[3])
        assertTrue(s.paused)
        assertEquals(99L, s.uptimeSec)
    }

    /** 暂停指令的 JSON 必须与后端 hub.go 的字段名对齐。 */
    @Test
    fun pauseCommandMatchesServerContract() {
        val json = org.json.JSONObject(Protocol.pauseCommand(true))
        assertEquals("pause", json.getString("type"))
        assertTrue(json.getBoolean("paused"))
    }

    // ── 演示数据 ──

    /**
     * 演示 feed 的严重度必须覆盖整个 [0,1) 区间落在 0..3。
     * 尾部最重要：指数分布偶尔会给出 4，而 4 会被渲染侧兜底成最低档蓝色。
     */
    @Test
    fun demoSeverityNeverEscapesPalette() {
        var i = 0
        while (i < 100_000) {
            val sev = demoSeverity(i / 100_000.0)
            assertTrue("demoSeverity(${i / 100_000.0}) = $sev 越出 0..3", sev in 0..3)
            i++
        }
        assertEquals("u 趋近 1 时必须收在最高档", 3, demoSeverity(0.9999999999))
        assertEquals("u=0 必须是最低档", 0, demoSeverity(0.0))
    }

    /** 调色板只有四档严重度，与后端的 0..3 一一对应。 */
    @Test
    fun paletteHasExactlyFourSeverities() {
        assertEquals(4, Palette.sev.size)
    }
}
