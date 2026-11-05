/*
renderer.test.mjs —— 渲染管线集成冒烟测试（node + canvas 桩）。

目的：不依赖浏览器，验证 GlobeRenderer 在真实事件数据下
可完整执行渲染循环且无运行时错误，并产出预期的绘制原语。
*/

import { readFileSync } from 'node:fs';
import { GlobeRenderer, PALETTE } from '../static/js/renderer.mjs';
import { buildLandVectors } from '../static/js/land.mjs';
import { makeProjectorRaw } from '../static/js/projection.mjs';

// ── canvas 2D 上下文桩：记录调用次数 ──
function makeStubContext() {
  const calls = { fillRect: 0, arc: 0, stroke: 0, fill: 0, beginPath: 0 };
  const grad = { addColorStop() {} };
  const ctx = {
    setTransform() {},
    createRadialGradient: () => grad,
    createLinearGradient: () => grad,
    fillRect() { calls.fillRect++; },
    arc() { calls.arc++; },
    beginPath() { calls.beginPath++; },
    moveTo() {}, lineTo() {}, quadraticCurveTo() {},
    stroke() { calls.stroke++; },
    fill() { calls.fill++; },
    strokeText() {}, fillText() {},
    save() {}, restore() {}, clip() {},
    set fillStyle(v) {}, set strokeStyle(v) {},
    set lineWidth(v) {}, set globalAlpha(v) {},
    set shadowBlur(v) {}, set shadowColor(v) {},
    set font(v) {},
  };
  return { ctx, calls };
}

// ── 浏览器全局桩 ──
globalThis.window = {
  devicePixelRatio: 2,
  innerWidth: 1440,
  innerHeight: 900,
  addEventListener() {},
};
globalThis.requestAnimationFrame = () => {};
globalThis.performance = { now: () => 1000 };
globalThis.document = { getElementById: () => null };

// ── 测试数据 ──
const src2 = readFileSync(new URL('../static/world-data.js', import.meta.url), 'utf8');
globalThis.WORLD_LAND = JSON.parse(src2.slice(src2.indexOf('= [') + 2, src2.lastIndexOf(';')));

const sampleEvent = (i) => {
  // 一半事件两端均可见（纽约 → 圣保罗），一半目标在背面（俄罗斯 → 新加坡），
  // 覆盖「绘制弧线」与「跳过弧线」两条分支。
  if (i % 2 === 0) {
    return {
      ts: 1000 + i * 700,
      sev: i % 4,
      feed: 'Mock',
      raw: '45.33.1.2:80',
      src: { ip: '45.33.1.2', code: 'US', name: '美国', lat: 40.7, lon: -74.0 },
      dst: { code: 'BR', name: '巴西·圣保罗', lat: -23.5, lon: -46.6 },
    };
  }
  return {
    ts: 1000 + i * 700,
    sev: i % 4,
    feed: 'Mock',
    raw: '185.220.101.5:80',
    src: { ip: '185.220.101.5', code: 'RU', name: '俄罗斯', lat: 55.75, lon: 37.61 },
    dst: { code: 'SG', name: '新加坡', lat: 1.35, lon: 103.82 },
  };
};

/*
testRenderer 执行渲染管线冒烟测试。

参数:
  - ok (function): 断言函数。
*/
export async function testRenderer(ok) {
  // 大陆可见性回归：大陆填充与球体基底的亮度对比度必须 ≥ 3:1
  // （历史教训：大陆太暗导致在球面上完全不可见）。
  const lum = (rgb) => {
    const c = rgb.map((v) => {
      const s = v / 255;
      return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    });
    return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
  };
  const fillRGB = PALETTE.landFill.match(/\d+/g).slice(0, 3).map(Number);
  const baseRGB = [parseInt(PALETTE.globe1.slice(1), 16) >> 16 & 255,
    parseInt(PALETTE.globe1.slice(1), 16) >> 8 & 255,
    parseInt(PALETTE.globe1.slice(1), 16) & 255];
  const ratio = (lum(fillRGB) + 0.05) / (lum(baseRGB) + 0.05);
  ok(ratio >= 3, '大陆与球底对比度 ≥ 3:1（实际 ' + ratio.toFixed(2) + '）');
  ok(fillRGB[0] > 120 && fillRGB[1] < 80 && fillRGB[2] < 80, '大陆填充为红色系（RGB ' + fillRGB + '）');

  // 原始投影（不剔除背面）：深度符号与可见性一致。
  const raw = makeProjectorRaw(720, 450, 378, -1.2, 0.46);
  const front = raw(40.7, -74.0);   // 纽约：当前视角可见
  const back = raw(39.9, 116.4);    // 北京：当前视角背面
  ok(front.d > 0.03, '可见点深度为正（' + front.d.toFixed(3) + '）');
  ok(back.d < 0, '背面点深度为负（' + back.d.toFixed(3) + '）');
  // 背面点仍返回有限屏幕坐标（供深度插值裁剪使用）。
  ok(Number.isFinite(back.x) && Number.isFinite(back.y), '背面点返回有限坐标');

  const land = buildLandVectors(globalThis.WORLD_LAND, 0.9);
  const { ctx, calls } = makeStubContext();
  const canvas = {
    getContext: () => ctx,
    addEventListener() {},
    setPointerCapture() {},
    style: {},
    width: 0, height: 0,
  };

  const r = new GlobeRenderer(canvas, land, globalThis.WORLD_LAND);
  for (let i = 0; i < 40; i++) r.addEvent(sampleEvent(i));

  // 连续渲染 3 帧（不同时间戳，覆盖扫绘与淡出分支）。
  for (let t = 1000; t <= 3000; t += 1000) {
    globalThis.performance.now = () => t;
    r._frame(t);
  }

  ok(calls.fill > 5, '球体与实心大陆填充（fill ' + calls.fill + ' 次）');
  ok(calls.fillRect > 20000, '点阵纹理绘制（fillRect ' + calls.fillRect + ' 次）');
  ok(calls.arc > 60, '球面与标记绘制（arc ' + calls.arc + ' 次）');
  ok(calls.stroke > 60, '海岸线/弧线/光柱绘制（stroke ' + calls.stroke + ' 次）');
  ok(calls.beginPath > 150, '路径开启次数正常（' + calls.beginPath + '）');

  // 自转方向：地球自西向东（中心经度随时间递减）。
  r.lastDragAt = -1e9; // 允许自动旋转
  const yawBefore = r.yaw;
  r._frame(5000);
  ok(r.yaw < yawBefore, '自动旋转方向自西向东（yaw 递减 ' +
    (yawBefore - r.yaw).toFixed(4) + ' rad）');
}
