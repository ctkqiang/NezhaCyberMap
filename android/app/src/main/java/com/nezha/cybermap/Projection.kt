/*
Projection.kt —— 正射投影（与 Web 端 projection.mjs 同一套公式）。

约定：
  - 地球单位向量：x = cosφ·cosλ，y = cosφ·sinλ，z = sinφ。
  - yaw 为屏幕中心经度（弧度）；pitch 为北极向观察者倾斜角。
  - 屏幕映射：东在右、北在上；depth > 0.03 为可见半球。
*/
package com.nezha.cybermap

import kotlin.math.asin
import kotlin.math.atan2
import kotlin.math.cos
import kotlin.math.sin

private const val DEG = Math.PI / 180.0

/** 经纬度 → 地球单位向量。 */
fun toVec(lat: Double, lon: Double): DoubleArray {
    val phi = lat * DEG
    val lam = lon * DEG
    val cp = cos(phi)
    return doubleArrayOf(cp * cos(lam), cp * sin(lam), sin(phi))
}

/** 投影结果；depth 为朝向观察者的深度（1 = 正对着，0 = 地平线）。 */
data class Proj(val x: Float, val y: Float, val depth: Double)

/** 投影器公共接口：球体（Projector）与平面（FlatProjector）在渲染管线中可互换。 */
interface IProjector {
    fun project(x: Double, y: Double, z: Double): Proj?
    fun project(lat: Double, lon: Double): Proj?
}

/** 正射投影器；背面点返回 null。 */
class Projector(
    private val cx: Float,
    private val cy: Float,
    private val r: Float,
    yaw: Double,
    pitch: Double,
) : IProjector {
    private val cosY = cos(yaw)
    private val sinY = sin(yaw)
    private val cosP = cos(pitch)
    private val sinP = sin(pitch)

    override fun project(x: Double, y: Double, z: Double): Proj? {
        val c = x * cosY + y * sinY
        val s = y * cosY - x * sinY
        val depth = z * sinP + c * cosP
        if (depth <= 0.03) return null
        return Proj(
            x = cx + r * s.toFloat(),
            y = cy - (r * (z * cosP + c * sinP)).toFloat(),
            depth = depth,
        )
    }

    override fun project(lat: Double, lon: Double): Proj? {
        val v = toVec(lat, lon)
        return project(v[0], v[1], v[2])
    }
}

/**
 * FlatProjector —— 等距圆柱投影（平面地图模式，类卡巴斯基平铺视图）。
 *
 * 与球体模式的差异：
 *   - 没有背面：所有点可见，depth 恒为 1，渲染器的深度衰减公式因此退化为满亮；
 *   - 经度按 panDeg 平移后回绕到 [-180, 180)，横向拖拽时地图左右无缝衔接；
 *   - 跨日界线的线段由渲染器按相邻点横向跳变（> 半图宽）断笔处理。
 */
class FlatProjector(
    private val x0: Float,
    private val y0: Float,
    private val w: Float,
    private val h: Float,
    private val panDeg: Double,
) : IProjector {

    override fun project(lat: Double, lon: Double): Proj {
        val d = ((lon - panDeg) % 360.0 + 540.0) % 360.0 - 180.0
        return Proj(
            x = x0 + (((d + 180.0) / 360.0) * w).toFloat(),
            y = y0 + (((90.0 - lat) / 180.0) * h).toFloat(),
            depth = 1.0,
        )
    }

    /** 向量入口：反解经纬后走线性投影（只服务弹道采样点，陆地点阵用预算的经纬度数组）。 */
    override fun project(x: Double, y: Double, z: Double): Proj {
        return project(asin(z.coerceIn(-1.0, 1.0)) / DEG, atan2(y, x) / DEG)
    }
}
