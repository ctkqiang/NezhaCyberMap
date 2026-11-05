/*
land.test.mjs —— 陆地点阵生成的正确性验证。

覆盖：点阵规模合理、地标分类（陆/海）、单位向量合法性。
*/

import { readFileSync } from 'node:fs';
import { buildLandVectors } from '../static/js/land.mjs';
import { toVec } from '../static/js/projection.mjs';

// 加载世界轮廓数据：world-data.js 内容为「赋值语句 + 纯 JSON 数组」。
const src = readFileSync(new URL('../static/world-data.js', import.meta.url), 'utf8');
const body = src.slice(src.indexOf('= [') + 2, src.lastIndexOf(';'));
globalThis.WORLD_LAND = JSON.parse(body);

/*
landAt 判断经纬度是否在任意多边形环内（射线法，测试专用）。

参数:
  - lat, lon (number): 目标点。

返回:
  - boolean: 陆地为 true。
*/
function landAt(lat, lon) {
  for (const ring of globalThis.WORLD_LAND) {
    let inside = false;
    for (let e = 0, j = ring.length - 1; e < ring.length; j = e++) {
      const [x1, y1] = ring[e];
      const [x2, y2] = ring[j];
      if ((y1 > lat) !== (y2 > lat) &&
          lon < (x2 - x1) * (lat - y1) / (y2 - y1) + x1) {
        inside = !inside;
      }
    }
    if (inside) return true;
  }
  return false;
}

/*
testLand 执行点阵测试套件。

参数:
  - ok (function): 断言函数。
*/
export async function testLand(ok) {
  const land = buildLandVectors(globalThis.WORLD_LAND, 0.9);

  // 1. 点阵规模合理（0.9° 步长约 1.5 万点）。
  const dots = land.length / 3;
  ok(dots > 12000 && dots < 22000, '点阵规模合理（' + Math.round(dots) + ' 点）');

  // 2. 全部向量为单位长度（球面上）。
  let unit = true;
  for (let i = 0; i < land.length; i += 3) {
    const m = land[i] * land[i] + land[i + 1] * land[i + 1] + land[i + 2] * land[i + 2];
    if (Math.abs(m - 1) > 0.001) { unit = false; break; }
  }
  ok(unit, '全部点阵向量为单位长度');

  // 3. 点阵与轮廓多边形的地标分类一致。
  const landmarks = [
    ['堪萨斯（陆）', 39, -98, true],
    ['四川（陆）', 30, 103, true],
    ['太平洋（海）', 0, -150, false],
    ['大西洋（海）', 10, -40, false],
    ['撒哈拉（陆）', 23, 10, true],
    ['印度洋（海）', -20, 80, false],
  ];
  for (const [name, lat, lon, expectLand] of landmarks) {
    ok(landAt(lat, lon) === expectLand, name + (expectLand ? ' 应为陆地' : ' 应为海洋'));
  }

  // 4. 点阵中存在可验证的地标邻域点（以向量形式反查）。
  const beijing = toVec(39.9, 116.4);
  let nearBeijing = false;
  for (let i = 0; i < land.length; i += 3) {
    const d = (land[i] - beijing[0]) ** 2 + (land[i + 1] - beijing[1]) ** 2 + (land[i + 2] - beijing[2]) ** 2;
    if (d < 0.01) { nearBeijing = true; break; }
  }
  ok(nearBeijing, '点阵包含北京附近点');
}
