/*
projection.test.mjs —— 投影数学的正确性验证。

覆盖：东西方位、南北方位、背面剔除、屏幕边界、经纬度极限。
*/

import { makeProjector, toVec, makeProjectorVec } from '../static/js/projection.mjs';

const CX = 720, CY = 450, R = 378;
const YAW = -1.2;   // 美洲视角
const PITCH = 0.46;

/*
testProjection 执行投影测试套件。

参数:
  - ok (function): 断言函数。
*/
export async function testProjection(ok) {
  const project = makeProjector(CX, CY, R, YAW, PITCH);

  // 1. 西在左、东在右（纽约 < 伦敦 < 北京所在侧）。
  const ny = project(40.7, -74.0);
  const london = project(51.5, -0.1);
  ok(ny && london && ny.x < london.x, '西经城市位于东经城市左侧（纽约 < 伦敦）');

  const la = project(34.1, -118.2);
  ok(la && ny && la.x < ny.x, '更西的洛杉矶位于纽约左侧');

  // 2. 北在上（北半球城市 y 小于南半球城市；俯仰角下南极点应在背面）。
  const northPole = project(89, 0);
  const southPole = project(-89, 0);
  ok(northPole !== null && southPole === null, '北极可见且在南极上方（南极在背面被剔除）');
  const ny2 = project(40.7, -74.0);
  const saoPaulo = project(-23.5, -46.6); // 与纽约同半球可见
  ok(ny2 && saoPaulo && ny2.y < saoPaulo.y, '纽约（北纬）在圣保罗（南纬）上方');

  // 3. 背面剔除：当前视角下东亚/澳洲在背面。
  ok(project(39.9, 116.4) === null, '北京在背面被剔除（yaw=-1.2）');
  ok(project(-33.9, 151.2) === null, '悉尼在背面被剔除（yaw=-1.2）');

  // 4. 可见点均落在球体轮廓内。
  const samples = [
    [40.7, -74.0], [51.5, -0.1], [34.1, -118.2], [0, -69],
    [64.1, -21.9], [19.4, -99.1], [-23.5, -46.6], [-70, -60],
  ];
  let inBounds = true;
  for (const [lat, lon] of samples) {
    const p = project(lat, lon);
    if (!p) continue;
    const d = Math.hypot(p.x - CX, p.y - CY);
    if (d > R + 1) inBounds = false;
  }
  ok(inBounds, '全部可见投影点均落在球体轮廓内');

  // 5. 向量版与经纬度版结果一致。
  const pv = makeProjectorVec(CX, CY, R, YAW, PITCH);
  let consistent = true;
  for (const [lat, lon] of samples) {
    const a = project(lat, lon);
    const b = pv(...toVec(lat, lon));
    if ((a === null) !== (b === null)) { consistent = false; break; }
    if (a && b && (Math.abs(a.x - b.x) > 0.001 || Math.abs(a.y - b.y) > 0.001)) {
      consistent = false;
      break;
    }
  }
  ok(consistent, '经纬度投影与向量投影完全一致');

  // 6. 旋转一个完整视角（yaw+π）后，原本背面的大陆转正可见。
  const flipped = makeProjector(CX, CY, R, YAW + Math.PI, PITCH);
  ok(flipped(39.9, 116.4) !== null, '旋转 180° 后北京可见');

  // 7. 俯仰角 0 时赤道中心点位于球心水平线上。
  const flat = makeProjector(CX, CY, R, 0, 0);
  const center = flat(0, 0);
  ok(center && Math.abs(center.y - CY) < 1, '俯仰 0° 时赤道中心与球心同高');

  // 8. 自转方向：地球自西向东——中心经度递减时，固定地表特征
  //    应从屏幕左（西）向右（东）移动。
  const t1 = makeProjector(CX, CY, R, 0.5, 0.46);
  const t2 = makeProjector(CX, CY, R, 0.5 - 0.05, 0.46); // 时间推移 Δyaw = -0.05
  const p1 = t1(0, 0.5 + 0.4); // 可见区东侧的特征点（λ = yaw+23°）
  const p2 = t2(0, 0.5 + 0.4);
  ok(p1 && p2 && p2.x > p1.x, '自西向东自转：特征点随中心经度递减向右移动');
  // 特征点应始终可见（仍在可见窗口内）。
  ok(p1 !== null && p2 !== null, '自转过程中特征点保持可见');
}
