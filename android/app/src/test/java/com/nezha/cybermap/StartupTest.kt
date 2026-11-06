/*
StartupTest.kt —— 在 JVM 上真实启动 MainActivity。

为什么需要它：
  用户报的是"启动即崩"，而这类崩溃（主题解析、ViewModel 工厂、系统栏 API、
  Compose 首帧）全部发生在 onCreate 里，静态读代码看不出来。
  这台机器又没有可用设备（adb 列表为空、模拟器被 HVF 拒绝），
  所以只能让 Robolectric 在 JVM 上把 Activity 真正创建一遍：
  一旦 onCreate 抛异常，这个测试会直接红，堆栈就是真凶。

它验证的是"能活到 RESUMED"，不是"画面好看"——
  后者靠 GeometryTest 里的几何断言守。
*/
package com.nezha.cybermap

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(AndroidJUnit4::class)
// 在 minSdk 与当前主流版本上各启动一次。
// Robolectric 默认只跑一个 API 级别，用 34 通过并不代表 API 26 的机器也能起来：
// enableEdgeToEdge、BlendMode、WindowInsets 在低版本走的是完全不同的分支。
//
// NATIVE 图形模式：LEGACY 下 Bitmap/Canvas 全是空壳，绘制断言会退化成"什么都没画也通过"。
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [26, 33, 34])
class StartupTest {

    /** onCreate → onStart → onResume 全走完，且窗口内容真的被装上了。 */
    @Test
    fun activityReachesResumedAndAttachesContentView() {
        val controller = Robolectric.buildActivity(MainActivity::class.java).setup()
        val activity = controller.get()

        assertNotNull("Activity 没有被创建", activity)
        assertTrue("Activity 没有进入 RESUMED，启动过程被中断", !activity.isFinishing)

        // 只断言 decorView 非空是不够的：Activity 就算什么都不画也有 decorView。
        // content 下必须真的挂上 ComposeView，才能证明 setContent 的整条链路跑通了。
        val content = activity.findViewById<android.view.ViewGroup>(android.R.id.content)
        assertNotNull("content 容器不存在", content)
        assertTrue(
            "content 下没有子视图，setContent 没有生效（Compose 组合从未执行）",
            content.childCount > 0,
        )
        val root = content.getChildAt(0)
        assertEquals(
            "content 的根视图不是 ComposeView，实际是 ${root.javaClass.name}",
            "androidx.compose.ui.platform.ComposeView",
            root.javaClass.name,
        )
    }

    /** Application 必须能被实例化并跑完 onCreate——它现在承载崩溃取证。 */
    @Test
    fun applicationStartsAndWritesStartMarker() {
        val app = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.app.Application>()
        assertTrue("Manifest 的 android:name 没有指向 NezhaApp", app is NezhaApp)

        val marker = java.io.File(app.filesDir, "last-start.txt")
        assertTrue("Application.onCreate 没有留下启动标记，NezhaApp 可能没被实例化", marker.exists())
        val text = marker.readText()
        assertTrue("启动标记里没有 sdk 信息：$text", text.contains("sdk="))
    }

    /** 主题必须能解析：@style/Theme.NezhaCyberMap 的父主题一旦写错，这里立刻红。 */
    @Test
    fun applicationThemeResolves() {
        val controller = Robolectric.buildActivity(MainActivity::class.java).setup()
        val theme = controller.get().theme
        assertNotNull("Activity 主题为空，AndroidManifest 的 android:theme 没有生效", theme)
    }

    /** 地形资源必须能从 APK 的 assets 里真的读出来，而不是只在源码里存在。 */
    @Test
    fun landAssetIsReadableFromPackagedAssets() {
        val context = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>()
        val names = context.assets.list("")?.toList().orEmpty()
        assertTrue("assets 里没有 world-land.json，打包流程漏掉了地形资源", names.contains("world-land.json"))

        val vectors = buildLandVectors(context.assets)
        assertTrue("陆地点阵为空，扫描线填充没有产出任何点", vectors.isNotEmpty())
        assertTrue("陆地点阵长度 ${vectors.size} 不是 3 的倍数", vectors.size % 3 == 0)
    }

    // ── 真实绘制 ──

    /**
     * 把 frame() 真的画到一张 Bitmap 上。
     *
     * 这一段是之前全部测试的盲区：Activity 能启动，不代表绘制管线能跑。
     * BlendMode.Plus、nativeCanvas.drawText、clipPath、Brush.radialGradient
     * 在真机上直接落到 Skia，参数稍有不对就是硬崩溃。
     * Robolectric 4.14 默认 NATIVE 图形模式，这里走的是同一套 Skia 代码路径。
     *
     * startAt 刻意选在事件出生 200ms 后：此时彗星尾巴还没完全长出来
     * （fTail 为负），正是上一版数组越界崩溃的精确时序。
     */
    @Test
    fun sphereFramesActuallyRender() {
        val land = floatArrayOf(
            1f, 0f, 0f, 0.707f, 0.707f, 0f, 0f, 1f, 0f,
            -0.707f, 0.707f, 0f, 0f, 0f, 1f, 0f, 0f, -1f,
        )
        val r = GlobeRenderer(land)
        r.resize(1080f, 2340f, 321f, 48f)
        r.addEvent(event(sev = 3), 1000L)
        r.addEvent(event(sev = 1), 1010L)

        drawFrames(r, count = 40, startAt = 1200L)
    }

    /** 平面模式的绘制分支完全不同（刻度数字、扫描线、经纬网），必须单独跑。 */
    @Test
    fun flatFramesActuallyRender() {
        val land = floatArrayOf(1f, 0f, 0f, 0.707f, 0.707f, 0f, 0f, 1f, 0f)
        val r = GlobeRenderer(land)
        r.resize(1080f, 2340f, 321f, 48f)
        r.toggleFlat()
        r.addEvent(event(sev = 2), 1000L)

        drawFrames(r, count = 20, startAt = 1200L)
    }

    /** 横屏 + 平面模式：地图完整落在屏内，装饰框走的是另一条几何分支。 */
    @Test
    fun landscapeFlatFramesActuallyRender() {
        val r = GlobeRenderer(floatArrayOf(1f, 0f, 0f))
        r.resize(2340f, 1080f, 160f, 48f)
        r.toggleFlat()
        r.addEvent(event(sev = 0), 1000L)

        drawFrames(r, count = 20, startAt = 1200L)
    }

    private fun event(sev: Int) = AttackEvent(
        ts = 1L,
        src = GeoPoint("1.1.1.1", "CN", "中国", 39.9, 116.4),
        dst = GeoPoint("2.2.2.2", "US", "美国蜜罐", 38.9, -77.0),
        sev = sev,
        feed = "test",
        raw = "1.1.1.1:443",
    )

    private fun drawFrames(r: GlobeRenderer, count: Int, startAt: Long = 1000L) {
        val canvas = androidx.compose.ui.graphics.Canvas(
            androidx.compose.ui.graphics.ImageBitmap(1080, 1080),
        )
        val scope = androidx.compose.ui.graphics.drawscope.CanvasDrawScope()
        var t = startAt
        repeat(count) {
            scope.draw(
                density = androidx.compose.ui.unit.Density(2.75f),
                layoutDirection = androidx.compose.ui.unit.LayoutDirection.Ltr,
                canvas = canvas,
                size = androidx.compose.ui.geometry.Size(1080f, 1080f),
            ) {
                with(r) { frame(t) }
            }
            t += 16L
        }
    }
}
