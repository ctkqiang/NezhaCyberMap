
# 赛博威胁终端 - Agent 定义

## 1. 系统人设与核心指令

你是一名顶尖的底层系统工程师 + 终端可视化专家，专注于 Go (Golang) 开发。你构建的是纯粹的、零外部依赖（除 TUI 框架外）、经过深度优化的实时威胁情报终端。你将**内存安全、零分配缓冲区 (Zero-allocation) 和跨平台静态编译**置于绝对优先的位置。

你绝不使用除 `charmbracelet/bubbletea` / `lipgloss` / `bubbles` 以外的任何第三方包。绝不编写冗余代码。绝不靠猜——在编写代码前必须先检查当前工作区。

## 2. 项目定位

本项目是一个**终端 TUI 版实时 OSINT 网络攻击地图**，灵感来自 Kaspersky Cybermap（https://cybermap.kaspersky.com/）。它持续摄入公开威胁情报流（Honeypot 事件、ThreatFox、URLhaus、Feodo Tracker 等），通过 GeoIP 解析将攻击源/目标映射到 ASCII 世界地图，并在终端中绘制实时攻击连线、滚动事件日志和威胁统计面板。

核心理念：**Kaspersky 用 WebGL 和 3D 地球做的事，我们用字符在终端里做**——不是降级，是重新定义媒介。

## 3. 运行环境约束（极其重要）

你的代码必须在这个特定的沙箱环境中完美执行。严禁提供违反以下约束的解决方案：

- **目标环境**：Termux / 任何 POSIX 终端。最低推荐 80x24，必须优雅处理窗口缩放。
- **资源限制**：OS 可能具有激进的进程杀手和休眠模式。必须严格控制 Goroutine 生命周期和内存分配，避免 OOM。
- **网络**：所有外部 HTTP 请求必须封装在 `context.Context` 中，超时不得超过 **5 秒**（威胁流 API 响应可能缓慢，但绝不允许无限阻塞）。
- **禁止调用系统 Shell**：严禁使用 `os/exec` 去调用 `curl`、`wget` 或 `nmap`。所有网络请求必须通过纯 Go 的 `net/http` 完成。

## 4. 技术栈（强制）

| 层 | 选型 | 理由 |
|---|---|---|
| TUI 框架 | `charmbracelet/bubbletea` | Elm Architecture (MVU)，单向数据流，适合实时事件驱动渲染 |
| 样式 | `charmbracelet/lipgloss` | CSS-like 样式，用于地图着色、攻击线高亮、面板边框 |
| 组件 | `charmbracelet/bubbles` | viewport（滚动日志）、spinner（加载态）、key（键绑定） |
| 入口 | `cobra` 或标准库 `flag` | 子命令：`run`（TUI）、`feed`（列表可用数据源）、`version` |
| GeoIP | 纯 Go 实现的 MaxMind DB 读取器（自行在 `pkg/geoip` 中实现，零外部依赖） | 不引入 `oschwald/geoip2-golang`，符合零哲学 |

TUI 架构规范：**Model 持有全部状态，Update 是唯一的状态变更入口，View 是纯函数**。禁止在 View 中执行 I/O 或副作用。所有副作用（网络请求、GeoIP 查询、feed 轮询）以 `tea.Cmd` 形式返回，结果作为 `tea.Msg` 回到 Update。

## 5. 数据层核心规则

- **有界 Worker Pool**：每个威胁 feed 的摄入必须运行在独立的 goroutine 中，受 `chan struct{}` 信号量限制（最大并发 = 3）。严禁 `go fetchFeed()` 裸奔。
- **Buffered Channel + 背压**：事件从摄入层进入 TUI 层通过 `chan AttackEvent`（容量 1024）传递。当 TUI 渲染跟不上时，摄入层阻塞等待，而不是丢弃事件或无限分配内存。
- **Context 级联**：每个 feed 轮询器持有一个从根 context 派生的子 context。收到 `SIGINT` 或 `tea.Quit` 时，根 context 取消 → 所有子 context 取消 → 所有 HTTP 请求立即终止 → Worker Pool 排空退出。
- **确定性模拟模式**：当外部 feed 不可达时，自动切换到**内置模拟数据生成器**（类似 SecKC-MHN-Globe 的 mock fallback），保证 TUI 在任何网络条件下都可演示。

## 6. 中文文档与日志

- 所有 CLI 输出、flag 描述、行内注释、函数 GoDoc **全部使用中文**。
- 禁止使用 `log` 或 `fmt.Println` 输出运行时状态。使用 `pkg/logger`（自行实现，零外部依赖），日志级别：
  - `logger.Info` → `[+]` 绿色，标准状态变更（feed 已连接、事件批次到达）
  - `logger.Warn` → `[-]` 黄色，非致命问题（feed 超时、单条事件解析失败）
  - `logger.Error` → `[!]` 红色，局部组件故障（GeoIP 数据库损坏）
  - `logger.Fatal` → `[FATAL]` 粗体红色，致命崩溃
  - `logger.Success` → `[★]` 粗体绿色，关键情报（高风险攻击事件、新 feed 上线）
  - `logger.Debug` → `[·]` 灰色，底层字节流追踪（仅在 `--debug` 时启用）

## 7. 编译目标

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
  -ldflags="-s -w -extldflags '-static'" -o cybermap .
```

同时提供 `make build-all` 用于 amd64 交叉编译。

## 8. 交互键绑定（固定）

| 键 | 动作 |
|---|---|
| `q` / `Ctrl+C` | 退出 |
| `Space` | 暂停/恢复 feed 摄入 |
| `Tab` | 在「地图视图 / 日志视图 / 统计视图」间切换 |
| `g` | 切换 GeoIP 标注（显示国家名/仅坐标） |
| `m` | 切换单色模式（兼容低色终端） |
| `+` / `-` | 调整地图刷新率 |
| `?` | 显示帮助覆盖层 |
```

---

## `plan.md`

```markdown
# 赛博威胁终端 - 实施计划

## 目标

交付一个单二进制、零外部依赖（TUI 框架除外）、可在 Termux / 任意 POSIX 终端运行的**实时 OSINT 威胁攻击地图**。视觉上复刻 Kaspersky Cybermap 的核心体验：**地理投影 + 攻击连线 + 滚动事件流**，但用字符和 ANSI 颜色在终端中呈现。

## 阶段划分

### Phase 0：项目骨架与基础设施（2–3 天）

**交付物**：可编译的空壳 TUI，窗口自适应，`q` 退出。

- `go.mod`：module `github.com/<user>/cybermap`，Go 1.22+
- 目录结构按 `rules.md` 第 3 节建立
- `cmd/cybermap/main.go`：解析 flag / 子命令，构造根 `tea.Program`
- `internal/tui/model.go`：最小 `Model`，仅含 `width/height` 和 `tea.Quit` 逻辑
- `internal/tui/view.go`：居中渲染版本号
- `pkg/logger`：零依赖日志实现，支持颜色输出
- Makefile：`build` / `build-arm64` / `test`

**验收**：`go build` 通过，`./cybermap` 在 80x24 终端中正常启动和退出。

### Phase 1：数据层——威胁事件摄入管道（3–5 天）

**交付物**：后台 goroutine 持续产出 `AttackEvent`，日志中可见事件到达。

- `internal/feed/feed.go`：`Feed` 接口
  ```go
  type Feed interface {
      Name() string
      Run(ctx context.Context, out chan<- AttackEvent) error
  }
  ```
- 实现至少 **2 个真实 feed** + **1 个模拟 feed**：
  - `feed/threatfox.go`：ThreatFox API（abuse.ch），JSON 解析，IOC → `AttackEvent`
  - `feed/feodotracker.go`：Feodo Tracker C2 IP 列表，CSV/纯文本解析
  - `feed/mock.go`：确定性模拟生成器，在 `--mock` 或 feed 失败时启用
- `internal/ingest/pool.go`：有界 Worker Pool，最大 3 个 feed 并发，每个 feed 独立 ticker（默认 30s）
- `internal/ingest/pipeline.go`：`Start(ctx) <-chan AttackEvent`，内部启动所有 feed，返回只读 channel
- 事件结构：
  ```go
  type AttackEvent struct {
      Timestamp   time.Time
      SourceIP    net.IP
      SourceLat   float64
      SourceLon   float64
      TargetLat   float64
      TargetLon   float64
      Severity    Severity   // Low / Medium / High / Critical
      FeedName    string
      RawIndicator string
  }
  ```
- Context 级联：`main` 中 `signal.NotifyContext` → pipeline 的根 ctx → 每个 feed 的子 ctx

**验收**：`go test ./internal/ingest/...` 通过；运行 10 秒内日志可见至少 1 条事件到达。

### Phase 2：GeoIP 与地理投影（3–4 天）

**交付物**：IP 地址可解析为经纬度；ASCII 世界地图可渲染，点可投影到地图网格。

- `pkg/geoip/geoip.go`：自实现 MaxMind DB 格式读取器（仅支持 GeoLite2-City 的 `location` 子集），零外部依赖。备选：若实现复杂度过高，先内置一张静态 `IP → 国家` 映射表用于 demo，Phase 4 再替换。
- `internal/mapview/projection.go`：**等距圆柱投影 (Equirectangular)** 将经纬度映射到终端网格：
  ```go
  func Project(lat, lon float64, width, height int) (x, y int)
  ```
  这是最简单的投影，误差可接受，且与 SecKC-MHN-Globe 的 ASCII 地球实现一致。
- `internal/mapview/world.go`：内置低分辨率 ASCII 世界地图（约 80x24 字符），以 `[]rune` 二维切片存储陆地/海洋掩码。来源：Natural Earth 低清数据离线简化，或手工绘制粗粒度版本。
- `internal/mapview/render.go`：将地图掩码 + 攻击源/目标坐标叠加，输出 `string`。攻击点用 `*`（高严重度）或 `·`（低严重度）；源→目标连线用 Bresenham 算法在字符网格上绘制，用 `→` `↗` `↘` 等方向字符近似。
- Lipgloss 样式：海洋深蓝背景，陆地暗绿，攻击线按严重度着色（Low 黄 / Medium 橙 / High 红 / Critical 品红闪烁）。

**验收**：给定任意 `lat/lon`，`Project` 返回合法 `(x,y)`；`./cybermap --demo` 在地图上显示静态攻击点和连线。

### Phase 3：TUI 集成——MVU 完整闭环（3–4 天）

**交付物**：TUI 实时消费事件，地图、日志、统计三个视图可切换，窗口缩放自适应。

- `internal/tui/model.go`：根 Model 持有：
  - `width, height int`
  - `events []AttackEvent`（环形缓冲区，上限 500）
  - `eventsCh <-chan AttackEvent`
  - `viewport textinput.Model`（日志视图滚动）
  - `activeView ViewMode`（地图 / 日志 / 统计）
  - `paused bool`
- `internal/tui/update.go`：
  - 处理 `tea.WindowSizeMsg` → 更新尺寸，重新计算投影
  - 处理自定义 `AttackEventMsg` → 追加到环形缓冲区，检查严重度触发 lipgloss 高亮
  - 处理 `tea.KeyMsg` → 按 `rules.md` 的键绑定表分发
  - `Init()` 返回一个 `tea.Cmd`，从 `eventsCh` 读取第一条事件并包装为 `AttackEventMsg`；该 Cmd 在每次处理事件后重新返回自身，形成**持续订阅循环**
- `internal/tui/view.go`：
  - `viewMap()`：调用 `mapview.Render(events, width, height)`
  - `viewLog()`：`viewport.View()`，每行格式 `[时间] [严重度] feed → sourceIP → target`
  - `viewStats()`：攻击源国家 Top 10、严重度分布、事件速率（条/分钟）
  - `viewHelp()`：覆盖层，列出键绑定
- `internal/tui/styles.go`：全部 Lipgloss 样式集中定义
- `internal/tui/commands.go`：`waitForEvent(ch)` Cmd 工厂

**验收**：`./cybermap` 启动后 5 秒内地图出现攻击点和连线；`Tab` 切换视图；`Space` 暂停后地图不再更新；窗口缩放不崩溃。

### Phase 4：打磨与发布（2–3 天）

- `--mock` 模式完善：无需网络即可演示全部功能
- 单色模式：`m` 切换，所有颜色降级为纯字符标记
- 性能验证：`go test -bench=.` 确保 `Project` 和 `Render` 零堆分配（使用 `testing.AllocsPerRun`）
- `README.md`：包含 ASCII 截图（用 `charmbracelet/vhs` 生成）、构建说明、数据源列表
- `goreleaser.yml`：多平台二进制发布

## 里程碑

| 日期 | 里程碑 |
|---|---|
| D+3 | Phase 0 完成，骨架可运行 |
| D+8 | Phase 1 完成，事件流贯通 |
| D+12 | Phase 2 完成，地图可渲染 |
| D+16 | Phase 3 完成，TUI 闭环 |
| D+19 | Phase 4 完成，v0.1.0 发布 |

## 风险与缓解

| 风险 | 缓解 |
|---|---|
| MaxMind 格式自实现复杂 | Phase 2 先用静态映射表，Phase 4 再替换 |
| ASCII 世界地图精度低 | 不追求地理精确，追求**视觉可读性**；陆地轮廓可辨认即可 |
| 终端颜色兼容性 | 单色模式兜底；Lipgloss 自动检测 `TERM` / `COLORTERM` |
| Feed API 不稳定 | 多 feed + mock fallback；单个 feed 失败不影响其他 |
| Bubble Tea v2 导入路径变更 | 锁定 `charm.land/bubbletea/v2`；若尚未稳定，先用 v1 并标记迁移 TODO |
```

---

## `rules.md`

```markdown
# 赛博威胁终端 - 编码规范与约束

## 1. 核心哲学（不可协商）

- **零外部依赖**：除 `charmbracelet/bubbletea`、`lipgloss`、`bubbles` 外，禁止引入任何 `github.com/...` 第三方包。所有工具函数（GeoIP 读取、Bresenham 画线、环形缓冲区、日志）在项目内自行实现。
- **零内存分配热点**：地图渲染路径（`Project` → `Render`）必须通过 `sync.Pool` 复用 `[]rune` 缓冲区和 `strings.Builder`。每次渲染的堆分配目标为 **0**。
- **禁止 Goroutine 泄露**：严禁裸 `go func()`。所有后台工作必须属于以下两类之一：
  1. 受 `chan struct{}` 信号量限制的 Worker Pool。
  2. 在 `errgroup`（自实现，零依赖）管理下的固定数量 goroutine，且必须在 ctx 取消后 100ms 内退出。
- **Context 级联**：每一个 HTTP 请求、每一个 ticker 循环、每一个 channel 接收操作都必须接受 `ctx context.Context` 作为第一个参数，并在 `ctx.Done()` 时立即返回。

## 2. 严格的文件结构（自上而下）

每一个 Go 文件必须严格按照以下顺序组织。严禁将函数实现与类型声明混杂在一起。

```
package [名称]

import (
    // 仅限：标准库 + charmbracelet/* + 本项目内部包
)

const (
    // 集中声明
)

var (
    // 集中声明（含 sync.Pool 实例）
)

type ... struct {}
type ... interface {}

// 所有函数和方法实现必须放在文件最后
```

标准文件结构示例：

```go
package mapview

import (
    "context"
    "strings"
    "sync"
    "time"

    "github.com/charmbracelet/lipgloss"
)

const (
    MapWidth  = 80
    MapHeight = 24
    MaxTrails = 64
)

var (
    runePool = sync.Pool{
        New: func() interface{} {
            buf := make([]rune, MapWidth*MapHeight)
            return &buf
        },
    }
    builderPool = sync.Pool{
        New: func() interface{} { return &strings.Builder{} },
    }
)

type Projection struct {
    Width  int
    Height int
}

type AttackEvent struct { /* ... */ }

// 具体的函数实现从这里开始往下写...
```

## 3. 项目目录结构

```
cybermap/
├── .opencode/
│   ├── agent.md
│   ├── plan.md
│   └── rules.md
├── cmd/
│   └── cybermap/
│       └── main.go              # 入口：flag/cobra 解析，signal.NotifyContext，tea.NewProgram
├── internal/
│   ├── tui/                     # Bubble Tea MVU 层
│   │   ├── model.go             # 根 Model + 全部状态字段
│   │   ├── update.go            # 消息处理（WindowSize / Key / AttackEvent / Tick）
│   │   ├── view.go              # 纯渲染（map / log / stats / help）
│   │   ├── styles.go            # 全部 Lipgloss 样式定义
│   │   ├── commands.go          # tea.Cmd 工厂（waitForEvent、tickerCmd）
│   │   └── keymap.go            # 键绑定表
│   ├── feed/                    # 威胁情报摄入实现
│   │   ├── feed.go              # Feed 接口 + AttackEvent 定义
│   │   ├── threatfox.go
│   │   ├── feodotracker.go
│   │   ├── urlhaus.go
│   │   └── mock.go
│   ├── ingest/                  # 摄入管道调度
│   │   ├── pool.go              # 有界 Worker Pool
│   │   └── pipeline.go          # Start(ctx) 返回 <-chan AttackEvent
│   └── mapview/                 # 地理投影与 ASCII 地图渲染
│       ├── projection.go        # Equirectangular 投影
│       ├── world.go             # ASCII 世界地图掩码
│       └── render.go            # 叠加事件、绘制连线
├── pkg/
│   ├── logger/                  # 零依赖日志
│   │   └── logger.go
│   ├── geoip/                   # MaxMind DB 自读取器（或静态映射表）
│   │   └── geoip.go
│   └── ringbuf/                 # 固定容量环形缓冲区
│       └── ringbuf.go
├── Makefile
├── go.mod
└── README.md
```

**规则**：
- `internal/` 下的包禁止相互循环导入。依赖方向必须是 `tui → ingest → feed` 和 `tui → mapview → geoip`，禁止反向。
- `pkg/` 下的包必须无 `internal/` 依赖，可被外部复用。
- 每个包必须有且仅有一个职责。

## 4. Bubble Tea MVU 约定

- **Model 是唯一状态容器**。禁止使用包级可变变量存储运行时状态（`sync.Pool` 和只读常量除外）。
- **Update 是唯一状态变更入口**。View 必须是 `Model → string` 的纯函数，禁止在其中调用任何 I/O、网络、文件读取或随机数生成。
- **副作用通过 Cmd 执行**。`tea.Cmd` 是 `func() tea.Msg`，其结果回到 Update。
- **消息类型必须有明确前缀**：`AttackEventMsg`、`FeedStatusMsg`、`TickMsg`、`ErrMsg`。禁止使用裸 `struct{}` 或 `string` 作为消息。
- **持续订阅模式**：
  ```go
  func waitForEvent(ch <-chan ingest.AttackEvent) tea.Cmd {
      return func() tea.Msg {
          event, ok := <-ch
          if !ok {
              return ErrMsg{Err: errors.New("事件流已关闭")}
          }
          return AttackEventMsg{Event: event}
      }
  }
  ```
  `Update` 处理 `AttackEventMsg` 后必须返回 `waitForEvent(m.eventsCh)` 重新挂起订阅。
- **窗口缩放必须无 panic**。`View` 中所有尺寸计算必须对 `m.width` / `m.height` 做下界保护（`max(1, x)`）。

## 5. 网络与数据摄入约束

- **超时**：所有 `http.Client` 必须设置 `Timeout: 5 * time.Second`。禁止使用无超时的 `http.Get`。
- **User-Agent**：必须设置 `"cybermap/<version> (+https://github.com/<user>/cybermap)"`，尊重 API 提供方的可识别性要求。
- **重试**：单个 feed 失败后等待 `30s` 重试，连续 5 次失败后标记该 feed 为 `degraded` 并在 TUI 中显示警告。禁止无限快速重试。
- **背压**：摄入层写入 `chan AttackEvent` 时使用 `select { case ch <- ev: case <-ctx.Done(): return }`。**禁止 `default` 丢弃事件**（除非是模拟模式的速率控制）。
- **事件环形缓冲区**：TUI 侧最多保留 **500** 条事件。超出时覆盖最旧条目。禁止 `append` 无界增长。

## 6. 渲染性能约束

- `mapview.Render` 每帧的目标分配为 **0**。实现方式：
  - 从 `runePool` 获取 `[]rune` 缓冲区，填充后通过 `builderPool` 的 `strings.Builder` 写出，`Reset` 后放回池。
  - 禁止在 Render 中调用 `fmt.Sprintf`（会分配）。使用 `strconv.AppendFloat` / `strconv.AppendInt` 到预分配 byte slice。
- 攻击连线绘制使用 **Bresenham 算法**，在 rune 网格上原地绘制。禁止为每条连线分配新的 `[][2]int` 坐标切片。
- 世界地图掩码是**只读**的 `[MapHeight][MapWidth]bool`（或 `[][MapWidth]uint8`），在 `init()` 中一次性初始化，运行时不做任何修改。

## 7. 中文 GoDoc 规范（严格遵守）

每个导出函数和方法必须拥有多行注释块，包含：**作用与目的**、**参数**、**返回**。格式示例：

```go
/*
Project 将经纬度坐标等距圆柱投影到终端字符网格。

参数:
  - lat (float64): 纬度，范围 [-90, 90]。
  - lon (float64): 经度，范围 [-180, 180]。
  - width (int): 目标网格宽度（字符数），必须 >= 1。
  - height (int): 目标网格高度（字符数），必须 >= 1。

返回:
  - x (int): 水平字符坐标，范围 [0, width-1]。
  - y (int): 垂直字符坐标，范围 [0, height-1]。
  - ok (bool): 若输入经纬度非法则返回 false。
*/
func Project(lat, lon float64, width, height int) (x, y int, ok bool) {
    // 实现...
}
```

**行内注释**同样必须使用中文。禁止英文注释（代码标识符和包名除外）。

## 8. 错误处理

- **禁止 `panic`**，除非是真正的不可恢复编程错误（如 nil 解引用无法避免）。所有 feed 解析错误、网络错误、GeoIP 查找失败必须通过 `error` 返回，并由调用方决定降级或记录。
- **错误包装**：使用 `fmt.Errorf("...: %w", err)` 保留错误链。
- **Feed 错误隔离**：单个 feed 的解析错误不得导致整个管道退出。错误应作为 `FeedStatusMsg` 发送到 TUI 的状态栏，同时继续运行其他 feed。
- **优雅退出**：`main` 中 `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` 产生根 ctx。所有 goroutine 必须监听该 ctx，在取消后 100ms 内完成清理并退出。

## 9. DRY 原则

- 在编写工具函数（IP 解析、经纬度验证、严重度颜色映射、时间格式化）之前，先搜索工作区，查看 `pkg/` 和 `internal/` 中是否已存在相关实现。
- 禁止复制粘贴代码。两个地方需要相同逻辑时，提取到 `pkg/` 下的独立函数。
- 每次只输出与当前功能严格相关的代码。禁止“顺手”添加未被请求的功能或抽象。

## 10. 测试要求

- `pkg/geoip`、`pkg/ringbuf`、`internal/mapview/projection`、`internal/ingest/pool` 必须有单元测试。
- `internal/mapview/render` 必须有基准测试，并使用 `testing.AllocsPerRun` 断言每帧分配为 0。
- Feed 的 HTTP 解析逻辑使用 `httptest.Server` 提供静态响应进行测试，禁止在测试中访问真实外部 API。
- `go vet ./...` 和 `staticcheck ./...` 必须通过。

## 11. 编译目标（固定）

```makefile
.PHONY: build build-arm64 test clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o cybermap ./cmd/cybermap

build-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
		-ldflags="-s -w -extldflags '-static'" -o cybermap-arm64 ./cmd/cybermap

test:
	go test -race ./...

clean:
	rm -f cybermap cybermap-arm64
```

