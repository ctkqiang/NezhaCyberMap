# 赛博威胁地图（CyberMap）

> Kaspersky 用 WebGL 和 3D 地球做的事，我们用 Canvas 在浏览器里做——实时 OSINT 网络攻击地图。

Web 版**实时威胁情报攻击地图**：持续摄入公开威胁情报流（ThreatFox /
Feodo Tracker / URLhaus），通过 GeoIP 解析将攻击源与蜜罐目标投影到浏览器
Canvas 世界地图，实时绘制攻击弧线、事件流水与威胁统计面板。

## 特性

- **旋转 3D 地球**：正射投影 + 扫描线生成的点阵陆地（1.2 万点，球面均匀密度），
  大气辉光、球面明暗、经纬网格、星空背景，自动缓慢旋转，鼠标拖拽可转动
  （松手 4 秒后恢复自转）。
- **实时攻击弧线**：贴合球面的贝塞尔弧线，光点沿弧线从攻击源飞向目标
  （直观展示「哪国在攻击哪国」），双端标注国家代码；攻击源白点+光柱 →
  蜜罐红点，严重度四档红色着色，弧线随时间淡出。
- **WebSocket 实时推送**：事件逐条推送 + 统计每秒快照 + feed 状态变更，
  断线自动重连（指数退避），欢迎帧回放最近 50 条事件。
- **真实威胁情报**：ThreatFox（IOC）、URLhaus（恶意载荷）需 abuse.ch API 密钥，
  Feodo Tracker（活跃 C2 清单）免密钥，5 秒超时、30 秒退避重试、
  连续 5 次失败自动降级；全部真实源失效时启用确定性模拟源兜底。
- **GeoIP 双通道**：`--geoip-db` 加载自研解析的 GeoLite2-City（零第三方库），
  否则回退内置静态 CIDR 表（覆盖常见 VPS / 云厂商网段）。
- **统计面板**：攻击源国家 Top10、严重度分布、30 秒速率火花线、数据源状态。
- **单二进制部署**：前端静态资源 go:embed 内嵌，一个可执行文件即整个服务。

## 安装与运行

```bash
make build                     # 本机构建 → ./cybermap
make build-arm64               # Linux arm64 静态编译（云主机 / 树莓派）

./cybermap                     # 启动（默认 0.0.0.0:8080，真实 feed）
./cybermap serve --mock        # 离线演示模式
./cybermap serve --port 9000   # 指定端口
./cybermap serve --geoip-db /path/to/GeoLite2-City.mmdb    # 高精度 GeoIP
./cybermap serve --abuse-key YOUR_API_KEY                 # abuse.ch 密钥（ThreatFox / URLhaus）
./cybermap feed                 # 列出可用数据源
./cybermap version              # 版本信息
```

浏览器打开 `http://localhost:8080` 即可看到实时攻击地图。

### 交互

| 操作 | 效果 |
|---|---|
| 拖拽地球 | 旋转视角（松手 4 秒后恢复自动旋转） |
| 点击「⏸ 暂停」/ 按 `Space` | 暂停 / 恢复事件转发（统计照常） |
| 悬停事件流水 | 查看完整攻击源信息 |

## 数据源

| 源 | 间隔 | 说明 |
|---|---|---|
| Blocklist.de | 60s | 蜜罐网络**实时攻击日志**（谁在攻击谁的第一手数据，无需密钥） |
| ThreatFox | 60s | IOC 情报（ip:port / C2 / 恶意软件），需 abuse.ch API 密钥 |
| Feodo | 300s | 活跃 C2 服务器推荐封锁清单（无需密钥） |
| URLhaus | 300s | 恶意软件投放载荷（IP / 域名），需 abuse.ch API 密钥 |
| TOR | 30m | TOR 匿名网络出口节点（无需密钥） |
| CINS | 1h | CINS Army 恶意 IP 清单（无需密钥） |
| EmergThreats | 1h | Proofpoint 开放恶意 IP 封锁清单（无需密钥） |
| Mock | 700ms | 内置确定性模拟，离线演示与降级兜底 |

## 架构

```
cmd/cybermap/    入口：子命令、signal.NotifyContext、HTTP 服务优雅关停
internal/feed/   威胁情报摄入（ThreatFox / Feodo / URLhaus / Mock）
internal/ingest/ 有界 Worker Pool（并发 ≤3）+ 管道编排 + 背压通道（1024）
internal/web/    Gin 路由 + WebSocket Hub（广播 / 心跳 / 暂停控制）+ 统计聚合
web/static/js/   前端 ES 模块（零依赖）：
                 main.mjs      装配入口（渲染器 / HUD / WS 客户端）
                 renderer.mjs  Canvas 3D 地球渲染（弧线扫绘 / 拖拽 / 自转）
                 projection.mjs 正射投影纯函数（d3.geoOrthographic 同款）
                 land.mjs      陆地点阵扫描线生成（纯函数）
                 hud.mjs       DOM 视图层（esc 转义防注入）
                 ws.mjs        WebSocket 客户端（指数退避重连）
                 util.mjs      共享常量与格式化
web/static/css/  设计系统（CSS 令牌 / 玻璃拟态 / 响应式）
web/test/        node 前端逻辑测试（投影 / 点阵 / 渲染管线冒烟）
web/embed.go     go:embed 静态资源（单二进制部署）
pkg/geoip/      自研 MaxMind DB 读取器 + 静态回退表（零第三方库）
pkg/ringbuf/    500 条事件环形缓冲区
pkg/logger/     零依赖分级日志（[+] [-] [!] [FATAL] [★] [·]）
```

第三方依赖仅 `gin-gonic/gin`（HTTP 路由）与 `gorilla/websocket`（WebSocket），
其余全部标准库。

## 接口

| 路径 | 说明 |
|---|---|
| `GET /` | 主页面 |
| `GET /ws` | WebSocket：`welcome` / `event` / `stats` / `feeds` 消息，上行 `{"type":"pause"}` |
| `GET /api/stats` | 统计快照（总数 / 速率 / 国家 Top10 / 严重度 / 火花线） |
| `GET /api/events/recent?limit=N` | 最近 N 条事件 |
| `GET /api/feeds` | 数据源状态 |
| `GET /healthz` | 健康检查 |

## 开发

```bash
make test          # Go 测试（-race）+ 前端逻辑测试（node）
make test-frontend # 仅前端：投影 / 点阵 / 渲染管线冒烟（23 项）
make vet           # go vet ./...
```
