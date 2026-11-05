/*
main.mjs —— 前端入口：装配渲染器 / HUD / 实时客户端。

数据流：
  WebSocket welcome → renderer.replaceEvents + hud.replaceEvents
  WebSocket event   → renderer.addEvent + hud.addEvent
  WebSocket stats   → hud.applyStats
  WebSocket feeds   → hud.applyFeeds
  HUD 暂停按钮       → ws.sendPause → 服务端回传 stats.paused → hud.setPaused
*/

import { GlobeRenderer } from './renderer.mjs';
import { HUD } from './hud.mjs';
import { RealtimeClient } from './ws.mjs';
import { buildLandVectors, DEFAULT_LAND_STEP_DEG } from './land.mjs';

function main() {
  const canvas = document.getElementById('map');
  const landVectors = buildLandVectors(globalThis.WORLD_LAND, DEFAULT_LAND_STEP_DEG);

  const renderer = new GlobeRenderer(canvas, landVectors);
  let ws; // 延迟初始化：HUD 回调在点击时才访问。
  const hud = new HUD({
    onPause: (paused) => ws.sendPause(paused),
    onStyle: () => hud.setStyleName(renderer.cycleStyle()),
  });
  hud.setStyleName('标准');
  ws = new RealtimeClient({
    onWelcome: (events) => {
      renderer.replaceEvents(events);
      hud.replaceEvents(events);
    },
    onEvent: (ev) => {
      renderer.addEvent(ev);
      hud.addEvent(ev);
    },
    onStats: (st) => hud.applyStats(st),
    onFeeds: (feeds) => hud.applyFeeds(feeds),
    onConn: (on) => hud.setConn(on),
  });

  window.addEventListener('resize', () => renderer.resize());
  renderer.start();
  ws.connect();
}

main();
