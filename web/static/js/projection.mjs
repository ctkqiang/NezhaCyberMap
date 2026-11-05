/*
projection.mjs —— 正射投影纯函数（d3.geoOrthographic 同款公式）。

设计约定：
  - 地球单位向量：x = cosφ·cosλ，y = cosφ·sinλ，z = sinφ（φ 纬度，λ 经度）。
  - yaw Y 为屏幕中心经度；pitch P 为北极向观察者倾斜角。
  - 屏幕映射保证：东在右、北在上；depth > 0 为可见半球。
  - 本模块无 DOM 依赖，可被 node 测试直接验证（web/test/）。
*/

const DEG = Math.PI / 180;

/*
toVec 将经纬度转换为地球单位向量。

参数:
  - lat (number): 纬度（度，-90..90）。
  - lon (number): 经度（度，-180..180）。

返回:
  - [x, y, z]: 单位向量三分量。
*/
export function toVec(lat, lon) {
  const phi = lat * DEG, lam = lon * DEG;
  const cp = Math.cos(phi);
  return [cp * Math.cos(lam), cp * Math.sin(lam), Math.sin(phi)];
}

/*
makeProjectorVec 创建面向单位向量的投影函数（单一实现源）。

参数:
  - cx, cy (number): 球心屏幕坐标（CSS 像素）。
  - r (number): 球半径（CSS 像素）。
  - yaw (number): 屏幕中心经度（弧度）。
  - pitch (number): 北极倾斜角（弧度，正值表示北极朝向观察者）。

返回:
  - project(x, y, z): {x, y, z} 或 null（背面不可见）。
*/
export function makeProjectorVec(cx, cy, r, yaw, pitch) {
  const cosY = Math.cos(yaw), sinY = Math.sin(yaw);
  const cosP = Math.cos(pitch), sinP = Math.sin(pitch);

  return function project(x, y, z) {
    const c = x * cosY + y * sinY; // cosφ·cos(λ-Y)
    const s = y * cosY - x * sinY; // cosφ·sin(λ-Y)
    const depth = z * sinP + c * cosP;
    if (depth <= 0.03) return null;
    return {
      x: cx + r * s,
      y: cy - r * (z * cosP + c * sinP),
      z: depth,
    };
  };
}

/*
makeProjector 创建经纬度投影函数（委托给向量版，保证唯一实现）。

参数:
  - 同 makeProjectorVec。

返回:
  - project(lat, lon): {x, y, z} 或 null。
*/
export function makeProjector(cx, cy, r, yaw, pitch) {
  const pv = makeProjectorVec(cx, cy, r, yaw, pitch);
  return function project(lat, lon) {
    const [x, y, z] = toVec(lat, lon);
    return pv(x, y, z);
  };
}

/*
makeProjectorRaw 创建不剔除背面的投影函数（实心大陆边界裁剪用）。

与 makeProjector 的区别：背面点也返回屏幕坐标与深度（深度为负），
调用方可沿弦做深度插值求出与 depth=0 平面的交点。

参数:
  - 同 makeProjectorVec。

返回:
  - project(lat, lon): {x, y, d}（d 为深度，可正可负，永不为 null）。
*/
export function makeProjectorRaw(cx, cy, r, yaw, pitch) {
  const cosY = Math.cos(yaw), sinY = Math.sin(yaw);
  const cosP = Math.cos(pitch), sinP = Math.sin(pitch);
  return function projectRaw(lat, lon) {
    const [x, y, z] = toVec(lat, lon);
    const c = x * cosY + y * sinY;
    const s = y * cosY - x * sinY;
    return {
      x: cx + r * s,
      y: cy - r * (z * cosP + c * sinP),
      d: z * sinP + c * cosP,
    };
  };
}

/*
makeFlatProjector 创建等距圆柱投影（平面地图模式，类卡巴斯基平铺视图）。

与球体模式的差异：
  - 没有背面：所有点可见，z 恒为 1，调用方的深度衰减公式因此退化为满亮；
  - 经度按 panDeg 平移后回绕到 [-180, 180)，横向拖拽时地图左右无缝衔接；
  - 跨日界线的线段由调用方按相邻点横向跳变（> 半图宽）断笔处理。

参数:
  - x, y (number): 地图矩形左上角屏幕坐标（CSS 像素）。
  - w, h (number): 地图矩形宽高（h = w / 2）。
  - panDeg (number): 视图中心经度偏移（度）。

返回:
  - project(lat, lon): {x, y, z}（永不为 null）。
*/
export function makeFlatProjector(x, y, w, h, panDeg) {
  return function project(lat, lon) {
    const d = (((lon - panDeg) % 360) + 540) % 360 - 180;
    return {
      x: x + (d + 180) / 360 * w,
      y: y + (90 - lat) / 180 * h,
      z: 1,
    };
  };
}

/*
makeFlatProjectorVec 平面模式的向量投影适配器（反解经纬后走线性投影）。

陆地点阵应使用预解算的经纬度数组避免逐帧三角函数；
本函数只服务弹道采样点（每帧数百个，开销可忽略）。

参数:
  - 同 makeFlatProjector。

返回:
  - projectVec(x, y, z): {x, y, z}（z 恒为 1）。
*/
export function makeFlatProjectorVec(x, y, w, h, panDeg) {
  const p = makeFlatProjector(x, y, w, h, panDeg);
  return function projectVec(vx, vy, vz) {
    const lat = Math.asin(Math.max(-1, Math.min(1, vz))) / DEG;
    const lon = Math.atan2(vy, vx) / DEG;
    return p(lat, lon);
  };
}
