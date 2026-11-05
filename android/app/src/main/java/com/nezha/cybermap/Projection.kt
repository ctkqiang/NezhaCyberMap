/*
Projection.kt —— 正射投影（与 Web 端 projection.mjs 同一套公式）。

约定：
  - 地球单位向量：x = cosφ·cosλ，y = cosφ·sinλ，z = sinφ。
  - yaw 为屏幕中心经度（弧度）；pitch 为北极向观察者倾斜角。
  - 屏幕映射：东在右、北在上；depth > 0.03 为可见半球。
*/
package com.nezha.cybermap

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

/** 正射投影器；背面点返回 null。 */
class Projector(
    private val cx: Float,
    private val cy: Float,
    private val r: Float,
    yaw: Double,
    pitch: Double,
) {
    private val cosY = cos(yaw)
    private val sinY = sin(yaw)
    private val cosP = cos(pitch)
    private val sinP = sin(pitch)

    fun project(x: Double, y: Double, z: Double): Proj? {
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

    fun project(lat: Double, lon: Double): Proj? {
        val v = toVec(lat, lon)
        return project(v[0], v[1], v[2])
    }
}
