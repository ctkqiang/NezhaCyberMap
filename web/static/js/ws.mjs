/*
ws.mjs —— WebSocket 实时客户端（自动重连，指数退避）。

消息协议（服务端 → 前端）：
  welcome  {type, stats, events, feeds}   连接建立时的全量快照
  event    {type, event}                  单条攻击事件
  stats    {type, stats}                  每秒统计快照
  feeds    {type, feeds}                  feed 状态变更

前端 → 服务端：
  {"type":"pause","paused":true|false}    暂停/恢复事件转发
*/

const INITIAL_DELAY = 1000;
const MAX_DELAY = 10000;

/*
RealtimeClient 管理 WebSocket 连接生命周期。

用法：
  const ws = new RealtimeClient({
    onWelcome(msg), onEvent(ev), onStats(st), onFeeds(feeds), onConn(on),
  });
  ws.connect();
  ws.sendPause(paused);
*/
export class RealtimeClient {
  constructor({ onWelcome, onEvent, onStats, onFeeds, onConn }) {
    this.handlers = { onWelcome, onEvent, onStats, onFeeds, onConn };
    this.sock = null;
    this.delay = INITIAL_DELAY;
    this.timer = null;
  }

  /*
  connect 建立连接（失败时指数退避重连）。

  返回:
    - 无。
  */
  connect() {
    const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
    const sock = new WebSocket(proto + location.host + '/ws');
    this.sock = sock;

    sock.onopen = () => {
      this.delay = INITIAL_DELAY;
      this.handlers.onConn(true);
    };
    sock.onmessage = (e) => {
      let msg;
      try { msg = JSON.parse(e.data); } catch { return; }
      switch (msg.type) {
        case 'welcome':
          if (msg.stats) this.handlers.onStats(msg.stats);
          if (msg.feeds) this.handlers.onFeeds(msg.feeds);
          if (msg.events) this.handlers.onWelcome(msg.events);
          break;
        case 'event':
          this.handlers.onEvent(msg.event);
          break;
        case 'stats':
          this.handlers.onStats(msg.stats);
          break;
        case 'feeds':
          this.handlers.onFeeds(msg.feeds);
          break;
      }
    };
    sock.onclose = () => {
      this.handlers.onConn(false);
      if (this.timer) clearTimeout(this.timer);
      this.timer = setTimeout(() => {
        this.delay = Math.min(this.delay * 2, MAX_DELAY);
        this.connect();
      }, this.delay);
    };
    sock.onerror = () => { try { sock.close(); } catch { /* 忽略 */ } };
  }

  /*
  sendPause 发送暂停/恢复命令。

  参数:
    - paused (boolean): 是否暂停。

  返回:
    - 无。
  */
  sendPause(paused) {
    if (this.sock && this.sock.readyState === WebSocket.OPEN) {
      this.sock.send(JSON.stringify({ type: 'pause', paused }));
    }
  }
}
