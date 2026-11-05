/*
hud.mjs —— HUD 视图层：只做 DOM 更新，不含业务逻辑。

职责边界：
  - 输入：后端 StatsSnapshot / FeedItem / 攻击事件。
  - 输出：顶栏数字、右侧面板、左下事件流。
  - 不做任何动画循环、不持有 socket、不碰 canvas。

所有来自后端的文本都经过 esc() 再拼进模板，防止把事件字段当标记执行。
对外 API 与 main.mjs 的调用约定保持一致：
  setStyleName / applyStats / applyFeeds / addEvent / replaceEvents
  setConn / setPaused
*/

import { SEV, esc, fmtClock, fmtTime, TICKER_LINES } from './util.mjs';

// 数据源健康度 → 类名与文案（与后端 FeedState 0/1/2 对齐）。
const FEED_STATE = [
  { cls: 'ok', label: '运行中' },
  { cls: 'warn', label: '降级' },
  { cls: 'bad', label: '失败' },
];

/*
feedState 安全地取数据源状态描述。

参数:
  - state (number): 后端 feed 状态枚举。

返回:
  - object: {cls, label}。
*/
function feedState(state) {
  return FEED_STATE[state] || FEED_STATE[2];
}

/*
donutGradient 依据严重度计数生成 conic-gradient 表达式。

环形图用 CSS mask 挖空中心，所以这里只输出纯色环；
总数由独立的 .donut-center 显示，不会被遮罩盖住。

参数:
  - counts (number[]): 四档计数（低 → 严重）。
  - total (number): 四档之和。

返回:
  - string: 可直接赋给 background 的渐变表达式。
*/
function donutGradient(counts, total) {
  if (!total) return 'conic-gradient(rgba(255,255,255,0.07) 0deg 360deg)';
  let acc = 0;
  const stops = counts.map((n, i) => {
    const from = acc / total * 360;
    acc += n;
    const to = acc / total * 360;
    const color = SEV.colors[i] || SEV.colors[0];
    return color + ' ' + from.toFixed(2) + 'deg ' + to.toFixed(2) + 'deg';
  });
  return 'conic-gradient(' + stops.join(', ') + ')';
}

/*
sparkSVG 把每秒计数序列画成面积折线图（内联 SVG，随容器自适应）。

参数:
  - values (number[]): 每秒计数（最新在末尾）。
  - rate (number): 当前事件速率（事件/分）。

返回:
  - string: SVG 标记 + 底部读数。
*/
function sparkSVG(values, rate) {
  const W = 100, H = 34, PAD_X = 2, PAD_Y = 4;
  const data = Array.isArray(values) && values.length ? values : [0];
  const max = Math.max(1, ...data);
  const step = data.length > 1 ? (W - PAD_X * 2) / (data.length - 1) : 0;

  const pts = data.map((v, i) => ({
    x: PAD_X + step * i,
    y: H - PAD_Y - (v / max) * (H - PAD_Y * 2),
  }));
  const first = pts[0];
  const last = pts[pts.length - 1];
  const poly = pts.map((p) => p.x.toFixed(2) + ' ' + p.y.toFixed(2)).join(' L ');
  const line = 'M' + poly;
  const area = line +
    ' L' + last.x.toFixed(2) + ' ' + (H - PAD_Y) +
    ' L' + first.x.toFixed(2) + ' ' + (H - PAD_Y) + ' Z';

  return '<svg viewBox="0 0 ' + W + ' ' + H + '" preserveAspectRatio="none" aria-hidden="true">' +
    '<defs><linearGradient id="sparkFill" x1="0" y1="0" x2="0" y2="1">' +
      '<stop offset="0" stop-color="#5ab0ff" stop-opacity="0.3"/>' +
      '<stop offset="1" stop-color="#5ab0ff" stop-opacity="0"/>' +
    '</linearGradient></defs>' +
    '<path d="M0 ' + (H - PAD_Y) + ' H' + W + '" stroke="rgba(255,255,255,0.05)" stroke-width="1" fill="none"/>' +
    '<path d="' + area + '" fill="url(#sparkFill)"/>' +
    '<path d="' + line + '" fill="none" stroke="#7cc2ff" stroke-width="1.4" ' +
      'stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke"/>' +
    '<circle cx="' + last.x.toFixed(2) + '" cy="' + last.y.toFixed(2) + '" r="1.6" fill="#5ab0ff"/>' +
  '</svg>' +
  '<div class="spark-foot"><span>峰值</span><b>' + max + '</b><em>事件/秒</em>' +
    '<span class="spark-peak">当前 ' + rate + ' 事件/分</span></div>';
}

/*
HUD 管理顶栏指标、右侧数据面板与左下事件流。

用法：
  const hud = new HUD({
    onPause: (paused) => ws.sendPause(paused),
    onStyle: () => renderer.cycleStyle(),
  });
*/
export class HUD {
  constructor({ onPause, onStyle }) {
    this.onPause = onPause;
    this.onStyle = onStyle;
    this.paused = false;

    this.btnPause = document.getElementById('btn-pause');
    this.btnPause.addEventListener('click', () => this.onPause(!this.paused));

    const btnStyle = document.getElementById('btn-style');
    if (btnStyle) btnStyle.addEventListener('click', () => this.onStyle());

    const btnPanel = document.getElementById('btn-panel');
    if (btnPanel) {
      btnPanel.addEventListener('click', () => {
        const open = document.body.classList.toggle('panel-open');
        btnPanel.setAttribute('aria-expanded', open ? 'true' : 'false');
      });
    }

    window.addEventListener('keydown', (e) => {
      if (e.target !== document.body) return;
      if (e.code === 'Space') {
        e.preventDefault();
        this.onPause(!this.paused);
      } else if (e.key === 'v' || e.key === 'V') {
        this.onStyle();
      } else if (e.key === 'Escape') {
        document.body.classList.remove('panel-open');
        if (btnPanel) btnPanel.setAttribute('aria-expanded', 'false');
      }
    });
  }

  /*
  q 取元素；元素缺失时返回 null，调用方直接跳过即可。

  参数:
    - id (string): DOM id。

  返回:
    - HTMLElement|null。
  */
  q(id) {
    return document.getElementById(id);
  }

  /*
  setText 安全地写入文本（元素不存在时静默跳过）。

  参数:
    - id (string): DOM id。
    - value (any): 待写入内容。

  返回:
    - 无。
  */
  setText(id, value) {
    const el = this.q(id);
    if (el) el.textContent = String(value);
  }

  /*
  setStyleName 更新风格按钮文案（保留 .ico 图标）。

  参数:
    - name (string): 风格名。

  返回:
    - 无。
  */
  setStyleName(name) {
    const btn = this.q('btn-style');
    if (!btn) return;
    const lbl = btn.querySelector('.lbl');
    if (lbl) lbl.textContent = name;
  }

  /*
  applyStats 刷新顶栏指标与面板内的四个统计区。

  参数:
    - st (object): 后端 StatsSnapshot。

  返回:
    - 无。
  */
  applyStats(st) {
    if (!st) return;

    this.setText('stat-total', st.total);
    this.setText('stat-rate', st.rate);
    this.setText('stat-uptime', fmtClock(st.uptime));
    this.setPaused(!!st.paused);

    const list = this.q('country-list');
    if (list) {
      if (!st.countries || st.countries.length === 0) {
        list.innerHTML = '<li class="empty">暂无数据</li>';
      } else {
        const max = st.countries[0].count || 1;
        list.innerHTML = st.countries.map((c, i) => {
          const pct = Math.max(3, Math.round(c.count / max * 100));
          return '<li class="row">' +
            '<span class="rank">' + String(i + 1).padStart(2, '0') + '</span>' +
            '<span class="code">' + esc(c.code) + '</span>' +
            '<span class="body">' +
              '<span class="name" title="' + esc(c.name) + '">' + esc(c.name) + '</span>' +
              '<span class="bar"><i style="width:' + pct + '%"></i></span>' +
            '</span>' +
            '<span class="cnt">' + c.count + '</span>' +
          '</li>';
        }).join('');
      }
    }

    const counts = Array.isArray(st.severity) ? st.severity : [0, 0, 0, 0];
    const total = counts.reduce((a, b) => a + b, 0);

    const donut = this.q('sev-donut');
    if (donut) donut.style.background = donutGradient(counts, total);
    this.setText('sev-total', total);

    const sevList = this.q('sev-list');
    if (sevList) {
      sevList.innerHTML = counts.map((n, i) => {
        const pct = total ? Math.round(n / total * 100) : 0;
        const color = SEV.colors[i] || SEV.colors[0];
        return '<li class="row">' +
          '<i class="sd" style="background:' + color + '"></i>' +
          '<span class="name">' + SEV.names[i] + '</span>' +
          '<span class="cnt">' + n + '</span>' +
          '<span class="pct"><i style="width:' + pct + '%;background:' + color + '"></i></span>' +
        '</li>';
      }).join('');
    }

    const spark = this.q('spark');
    if (spark) spark.innerHTML = sparkSVG(st.spark, st.rate);
  }

  /*
  applyFeeds 刷新数据源健康度列表。

  参数:
    - feeds (object): feed 名 → FeedItem。

  返回:
    - 无。
  */
  applyFeeds(feeds) {
    if (!feeds) return;
    const names = Object.keys(feeds).sort();
    const online = names.filter((n) => feeds[n].state === 0).length;
    this.setText('feed-count', online + '/' + names.length);

    const list = this.q('feed-list');
    if (!list) return;

    list.innerHTML = names.map((n) => {
      const f = feeds[n];
      const st = feedState(f.state);
      return '<li class="row">' +
        '<i class="fdot" style="background:var(--' + st.cls + ')"></i>' +
        '<span class="fname" title="' + esc(n) + '">' + esc(n) + '</span>' +
        '<span class="fstate st-' + st.cls + '">' + esc(f.name) + '</span>' +
        '<span class="fmsg" title="' + esc(f.msg) + '">' + esc(f.msg) + '</span>' +
      '</li>';
    }).join('');
  }

  /*
  addEvent 在事件流顶部插入一行。

  参数:
    - ev (object): 后端攻击事件。

  返回:
    - 无。
  */
  addEvent(ev) {
    const color = SEV.colors[ev.sev] || SEV.colors[0];
    const line = document.createElement('li');
    line.className = 'ln';
    line.innerHTML =
      '<span class="t">' + fmtTime(ev.ts) + '</span>' +
      '<i class="sv" style="background:' + color + '"></i>' +
      '<span class="rt">' +
        '<b>' + esc(ev.src.ip || ev.raw) + '</b> ' +
        '<span class="src">' + esc(ev.src.name) + '</span>' +
        '<span class="ar">→</span>' +
        '<span class="dst">' + esc(ev.dst.name) + '</span>' +
      '</span>' +
      '<span class="fd">' + esc(ev.feed) + '</span>';

    const ticker = this.q('ticker');
    if (!ticker) return;
    ticker.prepend(line);
    while (ticker.children.length > TICKER_LINES) {
      ticker.lastElementChild.remove();
    }
  }

  /*
  replaceEvents 清空并按时间顺序重建事件流（欢迎帧回放）。

  参数:
    - evs (object[]): 事件数组（升序）。

  返回:
    - 无。
  */
  replaceEvents(evs) {
    const ticker = this.q('ticker');
    if (!ticker) return;
    ticker.innerHTML = '';
    for (const ev of evs) this.addEvent(ev);
  }

  /*
  setConn 更新连接状态：绿色常亮 vs 红色呼吸。

  参数:
    - on (boolean): 是否已连接。

  返回:
    - 无。
  */
  setConn(on) {
    const el = this.q('conn-status');
    if (!el) return;
    el.classList.toggle('on', !!on);
    const lbl = el.querySelector('.lbl');
    if (lbl) lbl.textContent = on ? '已连接' : '重连中';
  }

  /*
  setPaused 同步暂停按钮的图标、文案与强调色。

  参数:
    - paused (boolean): 是否暂停。

  返回:
    - 无。
  */
  setPaused(paused) {
    this.paused = !!paused;
    if (!this.btnPause) return;
    this.btnPause.classList.toggle('paused', this.paused);
    this.btnPause.setAttribute('aria-pressed', this.paused ? 'true' : 'false');
    const ico = this.btnPause.querySelector('.ico');
    const lbl = this.btnPause.querySelector('.lbl');
    if (ico) ico.textContent = this.paused ? '▶' : '⏸';
    if (lbl) lbl.textContent = this.paused ? '恢复' : '暂停';
  }
}
