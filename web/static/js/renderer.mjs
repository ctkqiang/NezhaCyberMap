/*
renderer.mjs —— 正交投影地球渲染器（深空指挥台版）。

视觉目标：让地球像一台正在运转的仪器，而不是一张发光海报。

为此定下三条渲染原则：
  1. 陆地只用点阵。多边形填充在正交投影下跨地平线时会自动闭合，
     必然画出横穿球体的直线楔形；点阵由球面采样结果驱动，永远贴在真实大陆上。
  2. 攻击轨迹用大圆。屏幕空间贝塞尔的控制点按弦长外推，长弧的拱顶会冲出球体，
     看起来像光剑乱挥；大圆逐点采样保证每个点都在球面上，数学上不可能飞出。
  3. 弧线是"彗星"不是"激光"。等亮细长线在暗球面上极其锐利，
     改为「一颗亮头 + 一段渐隐尾焰」，读作弹药飞向目标，而不是一道划痕。
     飞行时长按球面弧长分配、弹头位置按分数下标插值：
     若所有弹道都固定 850ms、弹头每 28ms 才挪一格，眼睛立刻能看出这是假的。

时间基准说明（重要）：
  事件里的 ts 是服务端墙钟毫秒（约 1.7e12），而 requestAnimationFrame 的 now
  是页面启动后的相对毫秒（几十万量级）。两者直接相减会得到巨大负数，
  扫绘进度变成负值、弧线被反向延长。因此每帧计算一律使用本地的 bornAt。

本模块只依赖 util.mjs / projection.mjs / land.mjs，与 DOM 解耦（只持有 canvas）。
*/

import { SEV, MAX_EVENTS } from './util.mjs';
import { DEFAULT_LAND_STEP_DEG } from './land.mjs';
import { makeProjectorVec, makeProjector, makeFlatProjector, makeFlatProjectorVec, toVec } from './projection.mjs';

const DEG = Math.PI / 180;

// ── 相机 ──
const PITCH_BASE = 0.42;        // 初始北极倾斜角（弧度）
const SPIN_RATE = 0.00019;      // 自转角速度（弧度/毫秒）≈ 33 秒一圈
const SPIN_RESUME_MS = 4200;    // 拖拽结束后恢复自转的等待时间
const SPIN_RAMP_MS = 1400;      // 恢复自转的缓入时长（0 → 全速）
const SPIN_WOBBLE = 0.09;       // 自转速率浮动幅度（±9%）
const SPIN_WOBBLE_MS = 27500;   // 浮动周期：远长于肉眼记忆时长，读作漂移而非抖动
const FLING_HALF_MS = 900;      // 抬手惯性的半衰期
const FLING_MAX = 0.018;        // 惯性角速度上限（弧度/毫秒），防止一次甩动失控
const PITCH_LIMIT = 1.1;        // 俯仰夹紧，避免翻到球极之外

// ── 弧线（彗星弹道）──
const ARC_STEPS = 30;           // 大圆采样段数（等角分布，因此下标即角进度）
const ARC_TAIL = 13;            // 尾焰长度（段）≈ 全程的 43%
const ARC_SPEED = 0.085;        // 弹头角速度（球面度/毫秒）→ 90° 的弧约飞 1060ms
const ARC_FLIGHT_MIN_MS = 480;  // 短弧下限：再快就一闪而过，看不清弹道
const ARC_FLIGHT_MAX_MS = 1500; // 长弧上限：对径目标不该让人干等
const ARC_RETRACT_MS = 1500;    // 命中后尾焰滑入目标并被吸收的时长
const ARC_HALF_MS = 2600;       // 亮度半衰期
const ARC_MAX = 20;             // 同时存在的轨迹上限（同一国家对只留最新一条）
const FADE_FLOOR = 0.04;        // 低于此不透明度直接不画，避免历史事件叠成浊光

// ── 标记 ──
const LAND_MIN_DEPTH = 0.15;    // 陆地点阵的临边剔除阈值（见 _paintLand）
const LAND_BINS = 8;            // 陆地点阵的深度分桶数（见 _paintLand）
const LAND_JITTER = 0.55;       // 临边位移抖动幅度（相对格宽，见构造函数）
const SEED_MAX = 12;            // 参与标注的攻击源事件数
const SEED_TARGET_MAX = 6;      // 参与标注的目标数
const SEED_LIFE_MS = 7000;      // 攻击源标记的完整淡出时长
const RIPPLE_MS = 2400;         // 新事件扩散环时长

// ── 回放 ──
const REPLAY_STEP_MS = 150;     // 欢迎帧：相邻历史事件的入场间隔

const LAND_STEP_RAD = DEFAULT_LAND_STEP_DEG * Math.PI / 180;

/*
hexToRgba 把 #rrggbb 转成带透明度的 rgba 字符串。

Canvas 的线性渐变必须接受带 alpha 的色标才能表达"由实到淡"，
而 SEV.colors 存的是十六进制，所以需要这一层转换。

参数:
  - hex (string): #rrggbb 颜色。
  - a (number): 0..1 的不透明度。

返回:
  - string: rgba(...) 字符串。
*/
function hexToRgba(hex, a) {
  const v = parseInt(hex.slice(1), 16);
  const r = (v >> 16) & 255;
  const g = (v >> 8) & 255;
  const b = v & 255;
  return 'rgba(' + r + ',' + g + ',' + b + ',' + a + ')';
}

/*
easeOutCubic 三次缓出：起手快、末端收。

波前向外的正确形状——水波扩散、冲击波、涟漪都遵循这个速度曲线。
匀速扩张的圆环是最容易暴露"这是程序画的"的细节之一。

参数:
  - t (number): 0..1 进度。

返回:
  - number: 缓动后的进度。
*/
function easeOutCubic(t) {
  const u = 1 - t;
  return 1 - u * u * u;
}

/*
smoothstep 平滑阶跃（一阶导在两端为 0）。

自转权重的缓入用它，速度从 0 升到全速时不会有可以察觉的"起步抖动"。

参数:
  - t (number): 0..1。

返回:
  - number: 0..1。
*/
function smoothstep(t) {
  return t * t * (3 - 2 * t);
}

/*
视觉风格表（顶栏按钮或 V 键循环）。

前三个是球体模式下的信息密度档位，第四个切换到平面地图：
  标准  致密点阵 + 经纬网 + 大气辉光（默认，信息量最大）
  极简  稀疏点阵、关掉星空/经纬网/大气（最安静，只留大陆与轨迹）
  点阵  细点阵 + 强光照、无经纬网（复古终端感）
  平面  等距圆柱投影的平铺世界地图（类卡巴斯基）：无背面剔除，
        横拖平移经度且左右无缝回绕，全球事件一眼看全
*/
const STYLES = [
  { name: '标准', dotScale: 0.78, dotAlpha: 0.92, stars: true, graticule: true, atmosphere: true, light: 1.0 },
  { name: '极简', dotScale: 0.52, dotAlpha: 0.48, stars: false, graticule: false, atmosphere: false, light: 0.5 },
  { name: '点阵', dotScale: 0.34, dotAlpha: 0.62, stars: true, graticule: false, atmosphere: true, light: 1.15 },
  { name: '平面', dotScale: 0.9, dotAlpha: 0.95, stars: true, graticule: true, atmosphere: false, light: 0, flat: true },
];

// 平面档在表中的下标，以及球体档位的数量（供 cycleStyle 跳过平面档）。
const FLAT_STYLE_IDX = STYLES.findIndex((s) => s.flat);
const SPHERE_STYLE_COUNT = STYLES.filter((s) => !s.flat).length;

// 红黑主题调色板（全模块唯一色彩来源）。
// landFill 与 globe1 的亮度对比度必须 ≥3:1，否则大陆在球面上完全不可见，
// web/test/renderer.test.mjs 会对此做回归断言。
export const PALETTE = {
  skyCore: '#120d14',
  skyMid: '#09080c',
  skyEdge: '#050507',
  globe0: '#3d1420',                    // 球体受光侧（暗红黑）
  globe1: '#150409',                    // 球体背光侧（比天空亮且偏红，行星必须是实心体）
  landFill: 'rgba(204, 40, 58, 0.96)',  // 陆地点阵
  source: 'rgba(255, 244, 246, 0.92)',  // 攻击源环
  sourceCore: 'rgba(255, 255, 255, 0.95)',
  target: 'rgba(255, 74, 96, 0.95)',    // 目标（蜜罐）环
  label: 'rgba(255, 200, 208, 0.9)',
  targetLabel: 'rgba(255, 120, 140, 0.72)',
  star: '#fff4f5',
  rim: '235, 108, 126',                 // 地平线亮边（RGB 分量，配合 alpha 拼接）
  grid: 'rgba(255, 110, 130, 0.09)',
};

/*
GlobeRenderer 渲染旋转地球与攻击轨迹。

用法：
  const r = new GlobeRenderer(canvas, landVectors);
  r.start();
  r.replaceEvents(events);   // 欢迎帧回放
  r.addEvent(event);         // 实时事件
  window.addEventListener('resize', () => r.resize());
*/
export class GlobeRenderer {
  constructor(canvas, landVectors) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');
    this.land = landVectors;
    this.events = [];

    // 每个陆地点三组稳定抖动系数（只生成一次，不随帧变化）：
    //   _landSize —— 尺寸抖动，打散点阵的颗粒感；
    //   _landJx/_landJy —— 屏幕空间位移抖动。
    // 等距经纬采样有个通病：点阵在临边与极区被投影压缩后，
    // 同一纬度的点会粘成一条弧、相邻纬度之间留缝，
    // 整片大陆就成了「一圈圈同心等高线」。位移抖动按压缩强度递增来打断这些条带：
    // 球心处（z≈1）不抖，点阵保持锐利；越靠近地平线抖得越多，条带就散成雾。
    const dots = landVectors ? Math.floor(landVectors.length / 3) : 0;
    this._landSize = new Float32Array(dots);
    this._landJx = new Float32Array(dots);
    this._landJy = new Float32Array(dots);
    for (let i = 0; i < dots; i++) {
      this._landSize[i] = 0.78 + Math.random() * 0.44;
      this._landJx[i] = Math.random() * 2 - 1;
      this._landJy[i] = Math.random() * 2 - 1;
    }
    // 平面模式用的经纬度点阵：从单位向量反解一次并缓存，
    // 避免每帧对 1.5 万个点做 asin/atan2。
    this.landLL = null;
    if (landVectors) {
      this.landLL = new Float32Array(dots * 2);
      for (let i = 0; i < dots; i++) {
        this.landLL[i * 2] = Math.asin(Math.max(-1, Math.min(1, landVectors[i * 3 + 2]))) / DEG;
        this.landLL[i * 2 + 1] = Math.atan2(landVectors[i * 3 + 1], landVectors[i * 3]) / DEG;
      }
    }
    this._landBins = null;   // 深度分桶缓存，复用数组避免每帧分配

    this.w = 0; this.h = 0; this.dpr = 1;
    this.cx = 0; this.cy = 0; this.r = 0;
    // 平面地图矩形与横向平移（度）。
    this.mapX = 0; this.mapY = 0; this.mapW = 0; this.mapH = 0;
    this.panLon = 0;
    // 弹道描边的横向跳变阈值：球体模式无穷大（永不触发），
    // 平面模式为半图宽（跨日界线的相邻采样点不连线）。
    this._jumpX = Infinity;
    this.stars = [];

    this.yaw = -1.2;          // 初始朝向美洲
    this.pitch = PITCH_BASE;
    this.styleIdx = 0;
    this._flatReturnIdx = 0;   // 进入平面前的风格档，退出时恢复
    this.dragging = false;
    this.dragX = 0; this.dragY = 0;
    this.lastDragAt = -Infinity;  // 未拖拽过 → 启动即自转
    this.lastFrame = 0;

    // 相机运动状态：甩动惯性角速度（弧度/毫秒）与自转缓入权重。
    this.flingVel = 0;
    this.spinWeight = 1;
    this._lastMoveAt = 0;

    this.projector = null;
    this.projectorVec = null;
    // 同一帧内所有弧线采样共用；投影每帧都变，故每帧清空。
    this._arcCache = new Map();

    this._bindDrag();
    this.resize();
  }

  /*
  resize 依据视口重建画布与相机参数。

  返回:
    - 无。
  */
  resize() {
    const dpr = window.devicePixelRatio || 1;
    const w = window.innerWidth;
    const h = window.innerHeight;
    this.dpr = dpr; this.w = w; this.h = h;

    this.canvas.width = Math.round(w * dpr);
    this.canvas.height = Math.round(h * dpr);
    this.canvas.style.width = w + 'px';
    this.canvas.style.height = h + 'px';
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

    // 右侧面板是常驻的视觉区域，球心让到"面板以外的可视区"正中，
    // 否则地球会被面板切掉一截，看起来像没画完。
    const panelW = w >= 1180 ? 296 : (w >= 980 ? 268 : 0);
    const areaW = w - panelW;
    this.cx = areaW / 2;
    this.cy = h * (panelW ? 0.52 : 0.42);
    // 桌面受限于高度（宽屏下半径由 h 决定），窄屏受限于宽度（由 w 决定），
    // 两种约束取小值，任何比例下都不会顶到边界。
    this.r = Math.min(areaW * 0.48, h * (panelW ? 0.40 : 0.36));

    // 平面地图矩形：宽受面板外侧可视区约束，高受视口约束，保持 2:1。
    const flatW = Math.min(areaW * 0.94, h * 1.44);
    this.mapW = flatW;
    this.mapH = flatW / 2;
    this.mapX = (areaW - flatW) / 2;
    this.mapY = (h - this.mapH) * (panelW ? 0.52 : 0.46);

    this._buildStars();
    this._updateProjectors();
  }

  /*
  start 启动动画循环。

  返回:
    - 无。
  */
  start() {
    requestAnimationFrame(this._frame);
  }

  /*
  addEvent 追加一条事件（超出上限时淘汰最旧的）。

  参数:
    - ev (object): 后端事件 JSON（含 src/dst/sev/ts）。

  返回:
    - 无。
  */
  addEvent(ev) {
    this.events.push(Object.assign({}, ev, { bornAt: performance.now() }));
    if (this.events.length > MAX_EVENTS) this.events.shift();
  }

  /*
  replaceEvents 整体替换事件集合（欢迎帧回放）。

  参数:
    - evs (object[]): 事件数组（按时间升序）。

  返回:
    - 无。
  */
  replaceEvents(evs) {
    const now = performance.now();
    const tail = evs.slice(-MAX_EVENTS);
    const last = tail.length - 1;
    // 历史事件在时间轴上依次错开入场，而不是同一瞬间全部炸开。
    this.events = tail.map((ev, i) => Object.assign({}, ev, {
      bornAt: now - (last - i) * REPLAY_STEP_MS,
    }));
  }

  /*
  cycleStyle 在球体的信息密度档位之间循环（不含平面档）。

  平面是"视图"而非"风格"，由顶栏独立按钮控制；
  把它混进循环里，用户得点三次才能找到，语义也纠缠不清。

  返回:
    - string: 新风格名。
  */
  cycleStyle() {
    // 从平面回到球体时，落到标准档，避免"风格"按钮看起来毫无反应。
    this.styleIdx = this._style.flat ? 0 : (this.styleIdx + 1) % SPHERE_STYLE_COUNT;
    return STYLES[this.styleIdx].name;
  }

  /*
  toggleFlat 在球体与平面视图之间切换。

  进入平面时记住原来的风格档，退出时原样恢复——
  用户从"点阵"切到平面再切回来，不该被重置成"标准"。

  返回:
    - object: { flat: boolean, name: string } 切换后的视图与风格名。
  */
  toggleFlat() {
    if (this._style.flat) {
      this.styleIdx = this._flatReturnIdx;
      return { flat: false, name: STYLES[this.styleIdx].name };
    }
    this._flatReturnIdx = this.styleIdx;
    this.styleIdx = FLAT_STYLE_IDX;
    return { flat: true, name: STYLES[this.styleIdx].name };
  }

  // 当前风格配置。
  get _style() {
    return STYLES[this.styleIdx];
  }

  // ── 相机与输入 ──

  _updateProjectors() {
    if (this._style.flat) {
      const { mapX, mapY, mapW, mapH, panLon } = this;
      this.projector = makeFlatProjector(mapX, mapY, mapW, mapH, panLon);
      this.projectorVec = makeFlatProjectorVec(mapX, mapY, mapW, mapH, panLon);
      this._jumpX = mapW / 2;
    } else {
      this.projector = makeProjector(this.cx, this.cy, this.r, this.yaw, this.pitch);
      this.projectorVec = makeProjectorVec(this.cx, this.cy, this.r, this.yaw, this.pitch);
      this._jumpX = Infinity;
    }
  }

  _buildStars() {
    const n = Math.round(this.w * this.h / 14000);
    this.stars = [];
    for (let i = 0; i < n; i++) {
      this.stars.push({
        x: Math.random() * this.w,
        y: Math.random() * this.h,
        a: 0.06 + Math.random() * 0.14,
        phase: Math.random() * Math.PI * 2,
        speed: 0.5 + Math.random() * 1.1,
      });
    }
  }

  _bindDrag() {
    this._onDown = (e) => {
      this.dragging = true;
      this.dragX = e.clientX;
      this.dragY = e.clientY;
      this.canvas.setPointerCapture(e.pointerId);
    };
    this._onMove = (e) => {
      if (!this.dragging) return;
      const dx = e.clientX - this.dragX;
      const dy = e.clientY - this.dragY;
      this.dragX = e.clientX;
      this.dragY = e.clientY;

      const t = performance.now();
      const gap = Math.max(1, t - this._lastMoveAt);
      this._lastMoveAt = t;

      if (this._style.flat) {
        // 平面模式：横拖平移经度（右拖 → 地表右移），回绕到 [-180, 180)；
        // 纵向拖拽无意义（等距圆柱投影上下就是南北极），直接忽略。
        // 惯性甩动在平面图上读作"地图打滑"，也不记录。
        this.panLon = (((this.panLon - dx * 360 / this.mapW) % 360) + 540) % 360 - 180;
        this.lastDragAt = t;
        return;
      }

      // 手势跟手：右拖 → 地表右移（yaw 减小）；下拖 → 北极远离观察者。
      this.yaw -= dx * 0.005;
      this.pitch = Math.max(-PITCH_LIMIT, Math.min(PITCH_LIMIT, this.pitch - dy * 0.005));
      this.lastDragAt = t;

      // 记录瞬时角速度作为抬手后的惯性初速度（弧度/毫秒）。
      // 用 0.6 权重做一次低通，滤掉指针事件的坐标抖动，
      // 否则抬手瞬间的噪声会被放大成一次突兀的甩尾。
      const v = -(dx * 0.005) / gap;
      this.flingVel = this.flingVel * 0.4 + v * 0.6;
    };
    this._onUp = () => {
      this.dragging = false;
      this.lastDragAt = performance.now();
      this._lastMoveAt = 0;
      // 夹紧初速度：一次快甩可能算出上千弧度/秒，会让地球直接失控。
      this.flingVel = Math.max(-FLING_MAX, Math.min(FLING_MAX, this.flingVel));
      this.spinWeight = 0;   // 松手后自转从 0 缓入，而不是直接接上全速
    };
    this.canvas.addEventListener('pointerdown', this._onDown);
    this.canvas.addEventListener('pointermove', this._onMove);
    this.canvas.addEventListener('pointerup', this._onUp);
    this.canvas.addEventListener('pointercancel', this._onUp);
  }

  /*
  _stepCamera 推进相机姿态。

  三段角速度叠加，这是"真实感"的来源，缺任何一段都会显得机械：
    1. 甩动惯性：抬手后角速度按半衰期衰减，地球不会瞬间刹死；
    2. 自转缓入：静止一段时间后恢复自转，权重从 0 平滑升到 1，
       避免"停四秒、然后突然全速转"的档位感；
    3. 基础自转：恒定角速度，方向自西向东。

  参数:
    - dt (number): 本帧时长（毫秒，已夹紧）。
    - now (number): 本帧时间戳（毫秒）。

  返回:
    - 无。
  */
  _stepCamera(dt, now) {
    // 平面模式没有自转与惯性：地图是铺开的仪表板，静止才是正确的"性格"。
    if (this.dragging || this._style.flat) return;

    if (this.flingVel) {
      this.yaw -= this.flingVel * dt;
      this.flingVel *= Math.pow(0.5, dt / FLING_HALF_MS);
      // 低于视觉可辨速度直接归零，避免极小浮点数无限累加。
      if (Math.abs(this.flingVel) < 1e-5) this.flingVel = 0;
    }

    if (now - this.lastDragAt > SPIN_RESUME_MS) {
      this.spinWeight = Math.min(1, this.spinWeight + dt / SPIN_RAMP_MS);
    } else {
      this.spinWeight = 0;
    }

    // 中心经度递减 → 地表特征从屏幕左侧进入、向右侧移出。
    // 角速度叠一层极慢的浮动：绝对匀速是最典型的"转盘感"，
    // 真实镜头永远有微小起伏。周期 27.5 秒远长于肉眼能记住的时长，
    // 于是它读作"缓慢漂移"，而不是一台马达在转。
    const wobble = 1 + SPIN_WOBBLE * Math.sin((now / SPIN_WOBBLE_MS) * Math.PI * 2);
    this.yaw -= SPIN_RATE * smoothstep(this.spinWeight) * wobble * dt;
  }

  // ── 帧管线 ──

  _frame = (now) => {
    // dt 夹紧：页面切到后台再回来时 now 会跳很大，
    // 不夹紧的话一帧要转小半圈，看起来像"瞬移"。
    const dt = Math.min(48, this.lastFrame ? now - this.lastFrame : 16);
    this.lastFrame = now;

    this._stepCamera(dt, now);

    this._updateProjectors();
    this._arcCache.clear();

    this._paintSky(now);
    if (this._style.flat) {
      this._paintMapBg();
      if (this._style.graticule) this._paintGraticuleFlat();
      this._paintLandFlat();
    } else {
      this._paintAtmosphere();
      this._paintSphere();
      if (this._style.graticule) this._paintGraticule();
      this._paintLand();
      this._paintLight();
    }
    this._paintTrails(now);
    this._paintSeeds(now);

    requestAnimationFrame(this._frame);
  };

  _paintSky(now) {
    const { ctx, cx, cy, w, h } = this;
    const g = ctx.createRadialGradient(cx, cy, 0, cx, cy, Math.max(w, h) * 0.72);
    g.addColorStop(0, PALETTE.skyCore);
    g.addColorStop(0.55, PALETTE.skyMid);
    g.addColorStop(1, PALETTE.skyEdge);
    ctx.fillStyle = g;
    ctx.fillRect(0, 0, w, h);

    if (!this._style.stars) return;
    const t = now / 1000;
    ctx.fillStyle = PALETTE.star;
    for (const s of this.stars) {
      // 极慢正弦闪烁：固定亮度的星空像脏点，缓慢闪动才有纵深。
      ctx.globalAlpha = s.a * (0.7 + 0.3 * Math.sin(t * s.speed + s.phase));
      ctx.fillRect(s.x, s.y, 0.7, 0.7);
    }
    ctx.globalAlpha = 1;
  }

  /*
  _paintAtmosphere 大气层：紧贴地平线向外递减的一层红光 + 一道亮边。

  范围必须收窄：一旦把 halo 拉到 1.2r 以上并抬高峰值，
  球体外围就会套出一圈清晰的"红铁圈"，比不加辉光更脏。
  */
  _paintAtmosphere() {
    if (!this._style.atmosphere) return;
    const { ctx, cx, cy, r } = this;
    const PI2 = Math.PI * 2;
    const rgb = PALETTE.rim;

    // 辉光中心略向受光侧（左上）偏移。
    // 同心圆的光晕无论多淡，整圈强度都相同，眼睛会立刻读成"描边"；
    // 让地平线光晕带方向性，它才像被照亮的散射层而不是一圈发光的边。
    const gx = cx - r * 0.03, gy = cy - r * 0.04;
    const halo = ctx.createRadialGradient(gx, gy, r * 0.98, gx, gy, r * 1.42);
    halo.addColorStop(0, 'rgba(' + rgb + ',0.020)');
    halo.addColorStop(0.18, 'rgba(' + rgb + ',0.014)');
    halo.addColorStop(0.42, 'rgba(' + rgb + ',0.007)');
    halo.addColorStop(0.72, 'rgba(' + rgb + ',0.0025)');
    halo.addColorStop(1, 'rgba(' + rgb + ',0)');
    ctx.fillStyle = halo;
    ctx.beginPath();
    ctx.arc(cx, cy, r * 1.42, 0, PI2);
    ctx.fill();

    // 亮边是一条"内侧渐亮、外侧快衰"的辉光带，不是一条线。
    // 之前的写法把峰值压在轮廓上、宽度只有 0.05r，等于给球描了一圈红边——
    // 眼睛看到的不是大气，而是一个贴上去的发光圆环。
    // 这里把内侧拉到 0.88r 起步（宽约 0.15r），峰值压到 0.017，
    // 外侧再交给上面那条极宽的辉光淡出，轮廓就"化"在光里，
    // 不再有可以沿着描一圈的边。
    const edge = ctx.createRadialGradient(cx, cy, r * 0.88, cx, cy, r * 1.03);
    edge.addColorStop(0, 'rgba(' + rgb + ',0)');
    edge.addColorStop(0.4, 'rgba(' + rgb + ',0.006)');
    edge.addColorStop(0.62, 'rgba(' + rgb + ',0.014)');
    edge.addColorStop(0.74, 'rgba(' + rgb + ',0.017)');
    edge.addColorStop(0.88, 'rgba(' + rgb + ',0.007)');
    edge.addColorStop(1, 'rgba(' + rgb + ',0)');
    ctx.fillStyle = edge;
    ctx.beginPath();
    ctx.arc(cx, cy, r * 1.03, 0, PI2);
    ctx.fill();
  }

  _paintSphere() {
    const { ctx, cx, cy, r } = this;
    const base = ctx.createRadialGradient(cx - r * 0.32, cy - r * 0.34, r * 0.04, cx, cy, r);
    base.addColorStop(0, PALETTE.globe0);
    base.addColorStop(1, PALETTE.globe1);
    ctx.fillStyle = base;
    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, Math.PI * 2);
    ctx.fill();
  }

  /*
  _paintGraticule 经纬网。

  作用不是"装饰"，而是给海洋一层坐标参照：
  没有网格时，海洋是一片纯黑洞，大陆会显得漂浮无依。
  */
  _paintGraticule() {
    const { ctx, cx, cy, r } = this;
    ctx.save();
    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, Math.PI * 2);
    ctx.clip();
    // 经纬网在地平线附近淡出：把径向渐变直接当 strokeStyle 用，
    // 网格就不会一路亮到球的轮廓边缘，纵深立刻出来。
    const grid = ctx.createRadialGradient(cx, cy, r * 0.3, cx, cy, r);
    grid.addColorStop(0, PALETTE.grid);
    grid.addColorStop(0.72, 'rgba(255, 110, 130, 0.062)');
    grid.addColorStop(1, 'rgba(255, 110, 130, 0.012)');
    ctx.strokeStyle = grid;
    ctx.lineWidth = 0.7;

    for (let lon = -180; lon < 180; lon += 30) {
      ctx.beginPath();
      this._strokeCurve((lat) => this.projector(lat, lon), -90, 90, 5);
    }
    for (let lat = -60; lat <= 60; lat += 30) {
      ctx.beginPath();
      this._strokeCurve((lon) => this.projector(lat, lon), -180, 180, 5);
    }
    ctx.restore();
  }

  // 沿参数化曲线采样描边；投影返回 null（背面）处断笔，
  // 避免把跨地平线的两点误连成一条穿过球体的直线。
  _strokeCurve(sample, from, to, step) {
    const { ctx } = this;
    let pen = false;
    for (let a = from; a <= to; a += step) {
      const p = sample(a);
      if (!p) { pen = false; continue; }
      if (!pen) { ctx.moveTo(p.x, p.y); pen = true; }
      else ctx.lineTo(p.x, p.y);
    }
    ctx.stroke();
  }

  /*
  _paintLand 陆地点阵。

  方点尺寸随球半径与角间距缩放，相邻方点几乎相接，远看是致密大陆、
  近看保留数据矩阵质感。逐点按深度调亮度，让大陆在球面上有前后层次。

  两个性能/画质上的关键处理：
    1. 深度分桶。逐点写 globalAlpha 会触发数千次状态切换，而任何
       globalAlpha 变化都会让 Canvas 立刻 flush 掉刚攒下的绘制命令。
       把深度量化成 LAND_BINS 档后，状态切换从「每点一次」降到「每档一次」，
       fillRect 的总量不变（保证点阵密度）。
    2. 尺寸与位移抖动。见构造函数处的注释：它负责打散临边/极区被压缩成
       同心条带的点阵，那是等距经纬采样最明显的"等高线感"。
  */
  _paintLand() {
    const { ctx, land, projectorVec, cx, cy, r, _landSize, _landJx, _landJy } = this;
    if (!land) return;

    const base = Math.max(1, r * LAND_STEP_RAD * this._style.dotScale);
    const alpha = this._style.dotAlpha;
    const span = 1 - LAND_MIN_DEPTH;

    const bins = this._landBins || (this._landBins = []);
    for (let b = 0; b < LAND_BINS; b++) {
      const arr = bins[b] || (bins[b] = []);
      arr.length = 0;
    }

    let idx = 0;
    for (let i = 0, n = land.length; i < n; i += 3, idx++) {
      const p = projectorVec(land[i], land[i + 1], land[i + 2]);
      if (!p) continue;
      // 临边剔除：正交投影把最后 ~1.5% 半径内的点全部压进同一条像素带，
      // 十几个点一叠，alpha 就会从 0.12 累积到 0.7，变成一圈沿着轮廓的红描边。
      // 这一带本来就被临边昏暗压成暗红，直接不画，轮廓交给大气辉光收口。
      if (p.z < LAND_MIN_DEPTH) continue;

      const z = p.z;
      // 抖动幅度随压缩强度递增（见构造函数处的注释）。
      const jf = (1 - z) * LAND_JITTER * base;
      const x = p.x + _landJx[idx] * jf;
      const y = p.y + _landJy[idx] * jf;
      // 近处方点大、地平线附近方点小：配合亮度衰减构成球面纵深。
      const s = base * (0.55 + 0.45 * z) * (_landSize[idx] || 1);
      const b = Math.min(LAND_BINS - 1, Math.floor((z - LAND_MIN_DEPTH) / span * LAND_BINS));
      bins[b].push(x, y, s);
    }

    ctx.save();
    // 必须裁剪到球面：方点有尺寸，临边那排点的外半边会伸到球体轮廓之外，
    // 在没有裁剪的情况下会在太空里留下一圈细碎的红点。
    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, Math.PI * 2);
    ctx.clip();
    ctx.fillStyle = PALETTE.landFill;

    for (let b = 0; b < LAND_BINS; b++) {
      const arr = bins[b];
      if (!arr.length) continue;
      // 用档位中点反推深度：分桶只省状态切换，不改变原来的明暗曲线。
      const z = LAND_MIN_DEPTH + (b + 0.5) * span / LAND_BINS;
      // 亮度下限必须压到很低（0.10）：正交投影会把临边一圈的方点
      // 强行压到同一个像素带上，几千个点叠成的实心带就是一圈"描边"。
      ctx.globalAlpha = alpha * (0.10 + 0.90 * z);
      for (let j = 0, m = arr.length; j < m; j += 3) {
        const s = arr[j + 2];
        ctx.fillRect(arr[j] - s * 0.5, arr[j + 1] - s * 0.5, s, s);
      }
    }

    ctx.restore();
    ctx.globalAlpha = 1;
  }

  /*
  _paintMapBg 平面地图底板。

  比天空略亮、顶部一层极淡的受光、底部收暗：
  平面没有球体光照可借，若底板和天空同色系，地图会读成"屏幕上开了个洞"。
  */
  _paintMapBg() {
    const { ctx, mapX, mapY, mapW, mapH } = this;
    ctx.fillStyle = PALETTE.globe1;
    ctx.fillRect(mapX, mapY, mapW, mapH);
    const g = ctx.createLinearGradient(0, mapY, 0, mapY + mapH);
    g.addColorStop(0, 'rgba(255, 132, 132, 0.05)');
    g.addColorStop(0.5, 'rgba(0, 0, 0, 0)');
    g.addColorStop(1, 'rgba(0, 0, 0, 0.22)');
    ctx.fillStyle = g;
    ctx.fillRect(mapX, mapY, mapW, mapH);
    ctx.strokeStyle = 'rgba(235, 108, 126, 0.16)';
    ctx.lineWidth = 1;
    ctx.strokeRect(mapX + 0.5, mapY + 0.5, mapW - 1, mapH - 1);
  }

  /*
  _paintGraticuleFlat 平面经纬网：经纬各 30° 的直线网格。
  */
  _paintGraticuleFlat() {
    const { ctx, mapX, mapY, mapW, mapH } = this;
    ctx.save();
    ctx.beginPath();
    ctx.rect(mapX, mapY, mapW, mapH);
    ctx.clip();
    ctx.strokeStyle = PALETTE.grid;
    ctx.lineWidth = 0.7;
    for (let lon = -150; lon <= 150; lon += 30) {
      const x = this.projector(0, lon).x;
      ctx.beginPath();
      ctx.moveTo(x, mapY);
      ctx.lineTo(x, mapY + mapH);
      ctx.stroke();
    }
    for (let lat = -60; lat <= 60; lat += 30) {
      const y = this.projector(lat, 0).y;
      ctx.beginPath();
      ctx.moveTo(mapX, y);
      ctx.lineTo(mapX + mapW, y);
      ctx.stroke();
    }
    ctx.restore();
  }

  /*
  _paintLandFlat 平面陆地点阵。

  与球体版的三点差异：
    1. 数据走预解算的经纬度数组（landLL），不做逐点三角函数；
    2. 无深度衰减与临边剔除——平面图所有点满亮，亮度只叠加尺寸抖动；
    3. 尺寸不按深度收缩，密度即信息量；抖动只保留少量，打散经纬网格感。
  */
  _paintLandFlat() {
    const { ctx, landLL, mapX, mapY, mapW, mapH, _landSize, _landJx, _landJy } = this;
    if (!landLL) return;

    // 点宽 = 每度像素 × 采样间距 × 风格系数，相邻点近相接。
    const base = Math.max(1, mapW / 360 * DEFAULT_LAND_STEP_DEG * this._style.dotScale);
    const jf = 0.35 * base;

    ctx.save();
    ctx.beginPath();
    ctx.rect(mapX, mapY, mapW, mapH);
    ctx.clip();
    ctx.fillStyle = PALETTE.landFill;
    ctx.globalAlpha = this._style.dotAlpha;

    for (let i = 0, n = landLL.length; i < n; i += 2) {
      const p = this.projector(landLL[i], landLL[i + 1]);
      const idx = i >> 1;
      const s = base * (_landSize[idx] || 1);
      const x = p.x + _landJx[idx] * jf;
      const y = p.y + _landJy[idx] * jf;
      ctx.fillRect(x - s * 0.5, y - s * 0.5, s, s);
    }

    ctx.restore();
    ctx.globalAlpha = 1;
  }

  /*
  _paintLight 方向性光照。

  关键：光源是一个方向，不是一个点。
  旧做法用"中心亮、边缘暗"的径向渐变，那是平面贴纸的光影；
  真正的球体是左上方受光、右下方进入晨昏线，轮廓处再自然收暗。
  */
  _paintLight() {
    const { ctx, cx, cy, r } = this;
    const L = this._style.light;
    const PI2 = Math.PI * 2;

    const lx = cx - r * 0.62, ly = cy - r * 0.62;
    const light = ctx.createRadialGradient(lx, ly, r * 0.04, lx, ly, r * 1.65);
    light.addColorStop(0, 'rgba(255, 132, 132,' + (0.15 * L).toFixed(3) + ')');
    light.addColorStop(0.5, 'rgba(255, 84, 84,' + (0.045 * L).toFixed(3) + ')');
    light.addColorStop(1, 'rgba(0, 0, 0, 0)');
    ctx.fillStyle = light;
    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, PI2);
    ctx.fill();

    // 背光侧只压到 0.6：压到 0.76 时右下半个球面彻底死黑，
    // 球体读起来像一张挖掉一半的圆盘，而不是一个有体积的球。
    const sx = cx + r * 0.62, sy = cy + r * 0.6;
    const shade = ctx.createRadialGradient(sx, sy, r * 0.28, sx, sy, r * 1.42);
    shade.addColorStop(0, 'rgba(0, 0, 0, 0)');
    shade.addColorStop(0.45, 'rgba(0, 0, 0,' + (0.28 * L).toFixed(3) + ')');
    shade.addColorStop(1, 'rgba(0, 0, 0,' + (0.60 * L).toFixed(3) + ')');
    ctx.fillStyle = shade;
    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, PI2);
    ctx.fill();

    // 临边昏暗必须比"亮边"更强，这是球体看起来像球、而不是像描边圆盘的关键：
    // 表面最亮的像素绝不能落在轮廓上，否则那条轮廓就是一圈高对比的边。
    // 压暗到 0.32 之后，最后一圈陆地退成暗红，边缘由外侧大气辉光负责收口。
    const limb = ctx.createRadialGradient(cx, cy, r * 0.78, cx, cy, r);
    limb.addColorStop(0, 'rgba(0, 0, 0, 0)');
    limb.addColorStop(1, 'rgba(0, 0, 0, 0.32)');
    ctx.fillStyle = limb;
    ctx.beginPath();
    ctx.arc(cx, cy, r, 0, PI2);
    ctx.fill();
  }

  /*
  _paintTrails 绘制攻击轨迹。

  形态是「彗星」而非「光束」，分三层叠出来：
    - 外层烟：宽度是内芯的 4~5 倍、亮度不足一成，负责让边缘化进背景；
    - 内芯：只覆盖尾焰靠近弹头的那一段，形成"头亮尾淡"的方向感；
    - 弹头：白热核心 + 红色辉光，命中后化作扩散环。

  整层用 lighter（加性）混合。这是关键的一条：
  source-over 会把底下的球面像素"换掉"，得到的是一根彩色油漆线；
  加性混合把亮度相加，重叠处自然烧成白芯、外圈保留红光——
  这才是一个发光体在暗背景上真实的成像方式。
  */
  _paintTrails(now) {
    const { ctx, cx, cy, r, mapX, mapY, mapW, mapH } = this;
    const PI2 = Math.PI * 2;
    ctx.save();
    ctx.beginPath();
    // 球体裁圆、平面裁矩形；弹道绝不能越出地图边界。
    if (this._style.flat) ctx.rect(mapX, mapY, mapW, mapH);
    else ctx.arc(cx, cy, r, 0, PI2);
    ctx.clip();
    // save/restore 会一并还原该状态，不需要在末尾手动复位。
    ctx.globalCompositeOperation = 'lighter';

    for (const ev of this._activeTrails()) {
      const age = Math.max(0, now - ev.bornAt);
      const fade = Math.pow(0.5, age / ARC_HALF_MS);
      if (fade < FADE_FLOOR) continue;

      const geo = this._arcPoints(ev);
      if (!geo || geo.pts.length < 2) continue;

      // 弹头按「球面角进度」推进：采样点是等角分布的，所以进度 p 直接
      // 对应真实飞过的弧度。飞行时长由弧长决定（远慢近快），
      // 所有弹道统一 850ms 是"演示动画"最明显的破绽之一。
      const travel = Math.min(1, age / geo.flight);
      const fHead = travel * ARC_STEPS;

      // 命中后尾焰持续向目标滑落、长度收缩到 0（被吸收）。
      // 旧写法要先"吃掉"尾端差额才轮到尾巴移动，前 70% 时间尾焰完全静止，
      // 读起来就是"动画卡住了然后消失"。
      const retract = Math.min(1, Math.max(0, (age - geo.flight) / ARC_RETRACT_MS));
      const absorbed = smoothstep(retract);
      const fTail = Math.min(fHead, fHead - ARC_TAIL * (1 - absorbed));

      const headPt = this._lerpPt(geo.pts, fHead);
      if (!headPt) continue;
      const tailPt = this._lerpPt(geo.pts, fTail);
      if (!tailPt) continue;

      const color = SEV.colors[ev.sev] || SEV.colors[0];
      const thick = ev.sev >= 3;

      // 临边衰减：正交投影把地平线附近压成极窄的一条边，
      // 弹道在那里若保持全亮，会沿着 clip 边界"啪"地断掉。
      // 按弹头深度整体压暗，轨迹就能自己淡进轮廓里。
      // 弹头与尾焰一起被吸收：命中后仍挂着一个亮点的是 UI 标记，不是弹道。
      // 出生缓入：弹头若在第 0 帧就以满亮度出现在源点上，会读成一次"贴图刷新"；
      // 110ms 内淡入，才像一颗从发射点里"出来"的东西。
      const launch = smoothstep(Math.min(1, age / 110));
      const dk = 0.34 + 0.66 * headPt.z;
      const live = fade * (1 - absorbed) * dk * launch;

      if (live > 0.02) {
        const span = fHead - fTail;

        // 亮度与形状必须解耦：渐变色标只描述"哪里亮、哪里暗"（相对权重），
        // 生命周期衰减交给 globalAlpha。旧写法把 live 乘进色标里，
        // 等于打两次折，4.4px 宽的外层烟只剩 0.04 不透明度，看上去还是一根细线。
        ctx.globalAlpha = live;

        if (span > 0.6) {
          // 外层烟：整段尾焰的宽而极淡的底。
          // 只有 1px 硬线的轨迹看起来像用矢量笔描出来的；把宽度铺开、
          // 峰值压到三成以下，边缘就化进背景，读作弥散的光而不是一条边。
          const wide = ctx.createLinearGradient(tailPt.x, tailPt.y, headPt.x, headPt.y);
          wide.addColorStop(0, hexToRgba(color, 0));
          wide.addColorStop(0.45, hexToRgba(color, 0.10));
          wide.addColorStop(1, hexToRgba(color, 0.30));
          ctx.beginPath();
          ctx.strokeStyle = wide;
          ctx.lineWidth = (thick ? 10 : 7) * (0.70 + 0.30 * dk);
          this._tracePathF(geo.pts, fTail, fHead);
          ctx.stroke();

          // 内芯：只覆盖尾焰靠近弹头的那一段。
          // 尾端交给外层烟单独散掉，整条轨迹才有"头亮尾淡"的方向，
          // 而不是从头亮到尾、只能靠整体降透明度来消失的等亮线。
          const coreFrom = fTail + span * 0.40;
          const corePt = this._lerpPt(geo.pts, coreFrom);
          if (corePt) {
            const core = ctx.createLinearGradient(corePt.x, corePt.y, headPt.x, headPt.y);
            core.addColorStop(0, hexToRgba(color, 0));
            core.addColorStop(0.5, hexToRgba(color, 0.30));
            core.addColorStop(1, hexToRgba(color, 0.95));
            ctx.beginPath();
            ctx.strokeStyle = core;
            ctx.lineWidth = thick ? 1.9 : 1.35;
            this._tracePathF(geo.pts, coreFrom, fHead);
            ctx.stroke();
          }
        }

        // 弹头：白热核心 + 红色辉光。
        // 硬像素点在任何缩放下都像 UI 装饰，柔光才像"在发光的东西"。
        // 加性混合下中心那一圈会自然烧白，这里直接给近白色而不是纯色。
        const glowR = (thick ? 13 : 10) * (0.62 + 0.38 * dk);
        const glow = ctx.createRadialGradient(headPt.x, headPt.y, 0, headPt.x, headPt.y, glowR);
        glow.addColorStop(0, 'rgba(255, 238, 242, 0.62)');
        glow.addColorStop(0.22, hexToRgba(color, 0.44));
        glow.addColorStop(0.55, hexToRgba(color, 0.14));
        glow.addColorStop(1, hexToRgba(color, 0));
        ctx.fillStyle = glow;
        ctx.beginPath();
        ctx.arc(headPt.x, headPt.y, glowR, 0, PI2);
        ctx.fill();

        ctx.globalAlpha = Math.min(1, live * 1.4);
        ctx.beginPath();
        ctx.arc(headPt.x, headPt.y, thick ? 2.0 : 1.5, 0, PI2);
        ctx.fillStyle = 'rgba(255, 248, 250, 1)';
        ctx.fill();
        // 冲击环复用同一个 ctx，别让它继承刚才的 globalAlpha。
        ctx.globalAlpha = 1;
      }

      if (travel >= 1 && age < geo.flight + RIPPLE_MS) {
        // 冲击环落在目标真实经纬度上，而不是"最后一个可见采样点"，
        // 目标转到地平线附近时环的位置才不会漂。
        const dstPt = this.projector(ev.dst.lat, ev.dst.lon) || headPt;
        this._ripple(dstPt.x, dstPt.y, (age - geo.flight) / RIPPLE_MS,
          'rgba(255, 96, 118,', thick ? 9 : 6);
      }
      ctx.globalAlpha = 1;
    }
    ctx.restore();
  }

  /*
  _activeTrails 选取要绘制的轨迹：同一「源国家 → 目标国家」只留最新一条。

  欢迎帧一次会倒进上百条历史事件，其中大量是同一组国家对反复命中。
  全画出来会叠成一张放射状光网，完全读不出"谁在打谁"。
  倒序扫描，先命中的（最新的）留下，重复路径直接丢弃。

  返回:
    - Array<object>: 最多 ARC_MAX 条事件。
  */
  _activeTrails() {
    const seen = new Set();
    const out = [];
    for (let i = this.events.length - 1; i >= 0 && out.length < ARC_MAX; i--) {
      const ev = this.events[i];
      const key = (ev.src.code || '?') + '>' + (ev.dst.code || '?');
      if (seen.has(key)) continue;
      seen.add(key);
      out.push(ev);
    }
    return out;
  }

  /*
  _arcPoints 沿球面大圆采样轨迹，并算出这条弹道的飞行时长。

  做法：把起点向量绕「a 与 b 张成的平面」按等角度旋转到终点，
  即 v(φ) = a·cosφ + u·sinφ，其中 u 是与 a 正交、指向 b 的单位向量。
  等角采样是关键——下标 i 因此正比于走过的球面弧度，
  弹道速度、尾焰长度、命中时刻才有统一的物理刻度。

  （对照：旧的弦插值 + 归一化虽然也落在大圆上，但角速度中段快、两端慢，
   再叠加 30 段离散采样，弹头会呈现肉眼可辨的顿挫。）

  参数:
    - ev (object): 事件（含 src/dst 经纬度）。

  返回:
    - {pts: Array<{x,y}|null>, flight: number}: 屏幕点序列与飞行时长（毫秒）。
  */
  _arcPoints(ev) {
    const cached = this._arcCache.get(ev);
    if (cached) return cached;

    const a = toVec(ev.src.lat, ev.src.lon);
    const b = toVec(ev.dst.lat, ev.dst.lon);
    const dot = Math.max(-1, Math.min(1, a[0] * b[0] + a[1] * b[1] + a[2] * b[2]));
    const theta = Math.acos(dot);
    const cosT = Math.cos(theta);

    // u = (b - a·cosθ) / sinθ：与 a 正交的单位向量，指向 b。
    let ux = b[0] - a[0] * cosT;
    let uy = b[1] - a[1] * cosT;
    let uz = b[2] - a[2] * cosT;
    let m = Math.sqrt(ux * ux + uy * uy + uz * uz);
    if (m < 1e-9) {
      // 起终点重合或对径时 u 退化，任取一条与 a 正交的方向：
      // a 与 z 轴叉乘；a 本身就接近 z 轴时改用 x 轴，避免叉积再次归零。
      if (Math.abs(a[2]) < 0.9) {
        ux = a[1]; uy = -a[0]; uz = 0;
      } else {
        ux = 0; uy = a[2]; uz = -a[1];
      }
      m = Math.sqrt(ux * ux + uy * uy + uz * uz) || 1;
    }
    ux /= m; uy /= m; uz /= m;

    const pts = [];
    for (let i = 0; i <= ARC_STEPS; i++) {
      const phi = theta * (i / ARC_STEPS);
      const c = Math.cos(phi), s = Math.sin(phi);
      pts.push(this.projectorVec(a[0] * c + ux * s, a[1] * c + uy * s, a[2] * c + uz * s));
    }

    // 飞行时长与弧长成正比，两端夹紧：短弧不至于一闪而过，长弧不至于拖沓。
    const deg = theta * 180 / Math.PI;
    const flight = Math.min(ARC_FLIGHT_MAX_MS,
      Math.max(ARC_FLIGHT_MIN_MS, deg / ARC_SPEED));

    const geo = { pts, flight };
    this._arcCache.set(ev, geo);
    return geo;
  }

  /*
  _lerpPt 在两个相邻采样点之间取分数位置。

  离散采样是弹头顿挫的根源：30 段在大约 1 秒内只提供 30 个落点，
  60fps 下每次落点的间隔有近两帧，看起来像"跳格子"。
  按分数下标做一次线性插值，弹头才是连续移动的。

  参数:
    - pts (Array<{x,y}|null>): 采样点序列。
    - f (number): 0..pts.length-1 之间的分数下标。

  返回:
    - {x,y,z}|null: 插值点；两端都不可见时返回 null。
  */
  _lerpPt(pts, f) {
    const last = pts.length - 1;
    if (f <= 0) return pts[0];
    if (f >= last) return pts[last];
    const i = Math.floor(f);
    const k = f - i;
    const a = pts[i], b = pts[i + 1];
    if (!a || !b) return a || b;
    return {
      x: a.x + (b.x - a.x) * k,
      y: a.y + (b.y - a.y) * k,
      z: a.z + (b.z - a.z) * k,
    };
  }

  // 把 [from, to] 的分数区间写入当前路径；遇背面点断笔。
  // 平面模式下相邻点横向跳变超过半图宽（跨日界线）同样断笔，
  // 否则一条弹道会拉成横穿整张地图的水平线。
  _tracePathF(pts, from, to) {
    const { ctx } = this;
    let pen = false;
    let lastX = 0;
    const push = (p) => {
      if (!p) { pen = false; return; }
      if (pen && Math.abs(p.x - lastX) > this._jumpX) pen = false;
      if (!pen) { ctx.moveTo(p.x, p.y); pen = true; }
      else ctx.lineTo(p.x, p.y);
      lastX = p.x;
    };
    push(this._lerpPt(pts, from));
    const last = Math.min(pts.length - 1, Math.floor(to));
    for (let i = Math.ceil(from); i <= last; i++) {
      if (i > from) push(pts[i]);
    }
    if (to > last) push(this._lerpPt(pts, to));
  }

  /*
  _paintSeeds 绘制攻防两端的落点标记。

  攻击源用"环 + 实心点"（我方观测到的来源），
  目标用"空心红环"（蜜罐侧），形状差异保证色盲情况下也能区分。
  */
  _paintSeeds(now) {
    const { ctx, projector } = this;
    const recent = this.events.slice(-SEED_MAX);
    const placedSrc = new Set();
    const placedDst = new Set();
    const newestFrom = recent.length - SEED_TARGET_MAX;

    for (let i = recent.length - 1; i >= 0; i--) {
      const ev = recent[i];
      const age = Math.max(0, now - ev.bornAt);
      const life = Math.max(0, 1 - age / SEED_LIFE_MS);
      // 标记的出生缓入：环和点若在同一帧满不透明度出现，
      // 整块画面会像被人"盖章"；240ms 内边长边亮，才像长出来的。
      const birth = smoothstep(Math.min(1, age / 240));

      const s = projector(ev.src.lat, ev.src.lon);
      if (s) {
        // 攻击源环：亮度随生命周期衰减，但保留一个下限，
        // 保证任何时候都能看出威胁源分布，而不是一片漆黑。
        // 标记尺寸随深度收缩：贴在地平线上的环如果和球心处一样大，
        // 球面就没有纵深，整张图会退回成一张平面贴纸。
        const k = (0.68 + 0.32 * s.z) * (0.80 + 0.20 * birth);
        const a = (0.35 + 0.65 * life) * k * birth;
        ctx.beginPath();
        ctx.arc(s.x, s.y, 3.4 * k, 0, Math.PI * 2);
        ctx.strokeStyle = 'rgba(255, 244, 246,' + (0.5 * a).toFixed(3) + ')';
        ctx.lineWidth = 1;
        ctx.globalAlpha = 1;
        ctx.stroke();

        ctx.beginPath();
        ctx.arc(s.x, s.y, 1.5 * k, 0, Math.PI * 2);
        ctx.fillStyle = PALETTE.sourceCore;
        ctx.globalAlpha = a;
        ctx.fill();
        ctx.globalAlpha = 1;

        if (age < RIPPLE_MS) this._ripple(s.x, s.y, age / RIPPLE_MS, 'rgba(255,255,255,', 5);

        if (ev.src.code) {
          const key = Math.round(s.x / 40) + ':' + Math.round(s.y / 18);
          if (!placedSrc.has(key)) {
            placedSrc.add(key);
            ctx.font = '9.5px Menlo, Consolas, monospace';
            ctx.fillStyle = PALETTE.label;
            ctx.fillText(ev.src.code, s.x + 6, s.y - 6);
          }
        }
      }

      const d = projector(ev.dst.lat, ev.dst.lon);
      if (d) {
        // 目标环用略慢于攻击源的出生时长（300ms）：蜜罐是被"打到"的，
        // 它应该在弹头接近之后才浮现，而不是和攻击源同时盖章。
        const dbirth = smoothstep(Math.min(1, age / 300));
        ctx.beginPath();
        ctx.arc(d.x, d.y, 2.6 * (0.68 + 0.32 * d.z) * (0.78 + 0.22 * dbirth), 0, Math.PI * 2);
        ctx.strokeStyle = PALETTE.target;
        ctx.lineWidth = 1;
        ctx.globalAlpha = dbirth;
        ctx.stroke();
        ctx.globalAlpha = 1;

        if (age < RIPPLE_MS && ev.sev >= 2) {
          this._ripple(d.x, d.y, age / RIPPLE_MS, 'rgba(255,74,96,', 6);
        }

        if (i >= newestFrom && ev.dst.code) {
          const key = 'd' + Math.round(d.x / 40) + ':' + Math.round(d.y / 18);
          if (!placedDst.has(key)) {
            placedDst.add(key);
            ctx.font = '9px Menlo, Consolas, monospace';
            ctx.fillStyle = PALETTE.targetLabel;
            ctx.fillText(ev.dst.code, d.x + 5, d.y + 11);
          }
        }
      }
    }
  }

  /*
  _ripple 扩散环。

  参数:
    - x, y (number): 圆心屏幕坐标。
    - t (number): 0..1 进度（越小越新）。
    - rgb (string): 形如 "rgba(255,255,255," 的颜色前缀。
    - spread (number): 最大扩散半径。

  返回:
    - 无。
  */
  _ripple(x, y, t, rgb, spread) {
    const { ctx, cx, cy, r } = this;
    // t 必须夹紧：Canvas 对负半径会抛 IndexSizeError。
    const p = Math.min(1, Math.max(0, t));
    // 靠近地平线的环会有一部分切出球面、飘在太空里——那是贴纸感的来源。
    // 按到球心的归一化距离压暗，波前自然收在地平线之内。
    // 平面模式没有地平线，恒为满亮。
    const nd = r > 0 ? Math.hypot(x - cx, y - cy) / r : 0;
    const limb = this._style.flat ? 1 : Math.min(1, Math.max(0, (1.02 - nd) / 0.24));
    if (limb <= 0.01) return;
    // 半径走缓出、透明度走二次衰减：匀速扩张的圆环读起来像示意图，
    // 缓出的波前才有"推出去然后散掉"的物理感。
    const radius = 1.5 + easeOutCubic(p) * spread;
    // 出生缓入：环在 p=0 直接满亮度会出现"啪"地弹出一帧的硬切。
    // 前 18% 用 smoothstep 淡入，再接后面的二次衰减，才有完整的
    // 「推出 → 扩散 → 散掉」曲线，而不是一个凭空出现又缩水的圈。
    const rise = smoothstep(Math.min(1, p / 0.18));
    const fade = Math.pow(1 - p, 2) * limb * rise;

    ctx.beginPath();
    ctx.arc(x, y, radius, 0, Math.PI * 2);
    ctx.strokeStyle = rgb + (0.5 * fade).toFixed(3) + ')';
    ctx.lineWidth = 1;
    ctx.stroke();

    // 第二圈比主波前慢一点，形成"波前厚度"，而不是一根细线在放大。
    ctx.beginPath();
    ctx.arc(x, y, radius * 0.72, 0, Math.PI * 2);
    ctx.strokeStyle = rgb + (0.3 * fade).toFixed(3) + ')';
    ctx.stroke();
  }
}
