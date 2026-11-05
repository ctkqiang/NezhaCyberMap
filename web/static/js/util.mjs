/*
util.mjs —— 共享常量与工具函数（纯函数，无 DOM 依赖）。
*/

// 严重度调色板与中文名（与后端 Severity 枚举 0..3 对齐）。
// 红黑主题：低=暗红、中=亮红、高=正红、严重=深红高亮。
export const SEV = {
  colors: ['#b05563', '#ff5a66', '#ff1f3d', '#ff0033'],
  names: ['低', '中', '高', '严重'],
};

// 火花线字符（低 → 高）。
export const SPARK_CHARS = '▁▂▃▄▅▆▇█';

// 前端事件与流水线容量。
export const MAX_EVENTS = 200;
export const TICKER_LINES = 8;

/*
esc 转义 HTML 特殊字符，防止事件字段注入标记。

参数:
  - s (any): 待转义内容。

返回:
  - string: 安全的 HTML 文本。
*/
export function esc(s) {
  return String(s).replace(/[&<>"']/g, (ch) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]
  ));
}

/*
fmtClock 将秒数格式化为 mm:ss 或 hh:mm:ss。

参数:
  - sec (number): 秒数。

返回:
  - string: 时钟文本。
*/
export function fmtClock(sec) {
  const s = Math.max(0, Math.floor(sec));
  if (s < 3600) {
    return String(Math.floor(s / 60)).padStart(2, '0') + ':' + String(s % 60).padStart(2, '0');
  }
  return [Math.floor(s / 3600), Math.floor(s % 3600 / 60), s % 60]
    .map((v) => String(v).padStart(2, '0')).join(':');
}

/*
fmtTime 将毫秒时间戳格式化为 HH:MM:SS。

参数:
  - ms (number): 毫秒时间戳。

返回:
  - string: 时间文本。
*/
export function fmtTime(ms) {
  const d = new Date(ms);
  return [d.getHours(), d.getMinutes(), d.getSeconds()]
    .map((v) => String(v).padStart(2, '0')).join(':');
}

/*
sparkline 将每秒计数数组映射为火花线字符序列。

参数:
  - values (number[]): 计数序列。

返回:
  - string: 火花线文本。
*/
export function sparkline(values) {
  const max = Math.max(1, ...values);
  return values.map((v) => SPARK_CHARS[Math.min(7, Math.floor(v / max * 7))]).join('');
}
