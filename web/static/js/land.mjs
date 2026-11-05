/*
land.mjs —— 陆地点阵生成（扫描线多边形填充，纯函数）。

输入：WORLD_LAND（globalThis 注入的 Natural Earth 110m 轮廓，
      [[lon, lat], ...] 多边形环数组，公有领域数据）。
输出：Float32Array 地球单位向量点阵（球面均匀密度），供投影渲染。

本模块无 DOM 依赖，可被 node 测试验证（web/test/）。
*/

// 默认角间距（度）。主线程与渲染器共用，避免点阵间距和绘制尺寸脱节。
export const DEFAULT_LAND_STEP_DEG = 0.9;

/*
buildLandVectors 用扫描线算法将多边形环填充为点阵向量。

步骤：
  1. 对每个环的每条边求与各纬度行的交点（扫描线桶）。
  2. 每行交点排序后成对填充（奇偶规则）。
  3. 经度步长按 1/cos(纬度) 放大，保证球面点密度均匀。

参数:
  - worldLand (Array<Array<[number, number]>>): 多边形环数组。
  - stepDeg (number): 基准角间距（度）。

返回:
  - Float32Array: [x0,y0,z0, x1,y1,z1, ...] 单位向量点阵。
*/
export function buildLandVectors(worldLand, stepDeg) {
  const RAD = Math.PI / 180;
  const rows = Math.round(168 / stepDeg); // 纬度 -84°..84°
  const buckets = new Array(rows);
  for (let i = 0; i < rows; i++) buckets[i] = [];
  const latOf = (i) => -84 + i * stepDeg;

  // 1. 扫描线求交。
  for (const ring of worldLand) {
    const n = ring.length;
    for (let e = 0; e < n; e++) {
      const [lo1, la1] = ring[e];
      const [lo2, la2] = ring[(e + 1) % n];
      if (la1 === la2) continue; // 水平边不参与扫描
      const laMin = Math.min(la1, la2);
      const laMax = Math.max(la1, la2);
      const iMin = Math.max(0, Math.ceil((laMin + 84) / stepDeg));
      const iMax = Math.min(rows - 1, Math.floor((laMax + 84) / stepDeg));
      for (let i = iMin; i <= iMax; i++) {
        const t = (latOf(i) - la1) / (la2 - la1);
        buckets[i].push(lo1 + t * (lo2 - lo1));
      }
    }
  }

  // 2+3. 成对填充并生成单位向量。
  const pts = [];
  for (let i = 0; i < rows; i++) {
    const lat = latOf(i);
    const xs = buckets[i].sort((p, q) => p - q);
    const phi = lat * RAD;
    const cosPhi = Math.cos(phi);
    const lonStep = stepDeg / Math.max(0.25, Math.abs(cosPhi));
    for (let k = 0; k + 1 < xs.length; k += 2) {
      for (let lon = xs[k]; lon <= xs[k + 1]; lon += lonStep) {
        const lam = lon * RAD;
        const cl = Math.cos(lam), sl = Math.sin(lam);
        pts.push(cosPhi * cl, cosPhi * sl, Math.sin(phi));
      }
    }
  }
  return new Float32Array(pts);
}
