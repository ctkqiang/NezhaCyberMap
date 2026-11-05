/*
LandData.kt —— 陆地点阵生成（扫描线多边形填充）。

输入：assets/world-land.json（Natural Earth 110m 轮廓，[[lon,lat],...] 环数组）。
输出：FloatArray 地球单位向量点阵 [x0,y0,z0, x1,y1,z1, ...]。

与 Web 端 land.mjs 同算法：
  1. 每个环的每条边与各纬度行求交（扫描线桶）；
  2. 每行交点排序后成对填充（奇偶规则）；
  3. 经度步长按 1/cos(纬度) 放大，保证球面点密度均匀。
*/
package com.nezha.cybermap

import android.content.res.AssetManager
import org.json.JSONArray
import kotlin.math.abs
import kotlin.math.ceil
import kotlin.math.cos
import kotlin.math.floor
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt
import kotlin.math.sin

const val LAND_STEP_DEG = 0.9

/** 从 assets 读取多边形环并填充为球面点阵单位向量。 */
fun buildLandVectors(assets: AssetManager, stepDeg: Double = LAND_STEP_DEG): FloatArray {
    val text = assets.open("world-land.json").bufferedReader().use { it.readText() }
    val rings = JSONArray(text)

    val rows = (168.0 / stepDeg).roundToInt()
    val buckets = Array(rows) { mutableListOf<Double>() }
    val latOf = { i: Int -> -84.0 + i * stepDeg }

    for (ri in 0 until rings.length()) {
        val ring = rings.getJSONArray(ri)
        val n = ring.length()
        for (e in 0 until n) {
            val p1 = ring.getJSONArray(e)
            val p2 = ring.getJSONArray((e + 1) % n)
            val lo1 = p1.getDouble(0); val la1 = p1.getDouble(1)
            val lo2 = p2.getDouble(0); val la2 = p2.getDouble(1)
            if (la1 == la2) continue
            val laMin = min(la1, la2)
            val laMax = max(la1, la2)
            val iMin = max(0.0, ceil((laMin + 84) / stepDeg)).toInt()
            val iMax = min(rows - 1.0, floor((laMax + 84) / stepDeg)).toInt()
            for (i in iMin..iMax) {
                val t = (latOf(i) - la1) / (la2 - la1)
                buckets[i].add(lo1 + t * (lo2 - lo1))
            }
        }
    }

    val pts = ArrayList<Float>(16384)
    for (i in 0 until rows) {
        val lat = latOf(i)
        val xs = buckets[i]
        xs.sort()
        val phiRad = lat * Math.PI / 180.0
        val cosPhi = cos(phiRad)
        val lonStep = stepDeg / max(0.25, abs(cosPhi))
        var k = 0
        while (k + 1 < xs.size) {
            var lon = xs[k]
            while (lon <= xs[k + 1]) {
                val lam = lon * Math.PI / 180.0
                pts.add((cosPhi * cos(lam)).toFloat())
                pts.add((cosPhi * sin(lam)).toFloat())
                pts.add(sin(phiRad).toFloat())
                lon += lonStep
            }
            k += 2
        }
    }
    return pts.toFloatArray()
}
