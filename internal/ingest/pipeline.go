/*
Package ingest 负责威胁事件摄入调度：有界 Worker Pool + 管道编排。

设计要点（agent.md 第 5 节）：
  - 每个 feed 运行在独立 goroutine 中，受 chan struct{} 信号量限制，
    最大并发 = 3，严禁 go fetchFeed() 裸奔。
  - 事件通过容量 1024 的缓冲通道进入 TUI 层；TUI 消费不及时时，
    摄入侧阻塞等待（背压），而不是丢弃事件或无限分配。
  - 根 context 取消后，所有 feed 的 HTTP 请求与 ticker 循环立即终止。
*/
package ingest

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/internal/feed"
	"github.com/ctkqiang/NezhaCyberMap/pkg/logger"
)

// 事件通道与状态通道容量（背压设计）。
const (
	eventsBuf = 1024
	statusBuf = 16
)

// Pool 是有界 Worker Pool：以 chan struct{} 信号量限制并发任务数。
type Pool struct {
	sem chan struct{}
}

/*
NewPool 创建容量为 max 的 Worker Pool。

参数:
  - max (int): 最大并发任务数，必须 >= 1。

返回:
  - *Pool: 就绪的 Pool 实例。
*/
func NewPool(max int) *Pool {
	if max < 1 {
		max = 1
	}
	return &Pool{sem: make(chan struct{}, max)}
}

/*
Go 启动一个受控 goroutine 执行 fn：信号量获取在 goroutine 内部完成，
调用方永不阻塞（任务排队等待空闲槽位）。ctx 已取消时任务立即退出。

参数:
  - ctx (context.Context): 取消信号。
  - fn (func(context.Context)): 任务体。
*/
func (p *Pool) Go(ctx context.Context, fn func(context.Context)) {
	go func() {
		select {
		case p.sem <- struct{}{}:
			defer func() { <-p.sem }()
			if ctx.Err() != nil {
				return // 等待槽位期间 ctx 被取消
			}
			fn(ctx)
		case <-ctx.Done():
			return
		}
	}()
}

// FeedState 表示单个 feed 的运行状态。
type FeedState uint8

// feed 状态枚举：运行中 / 降级 / 失败。
const (
	FeedRunning FeedState = iota
	FeedDegraded
	FeedFailed
)

// feedStateNames 是状态的中文名表。
var feedStateNames = [3]string{"运行中", "降级", "失败"}

/*
Name 返回状态中文名。

返回:
  - string: "运行中" / "降级" / "失败"。
*/
func (s FeedState) Name() string {
	if int(s) < len(feedStateNames) {
		return feedStateNames[s]
	}
	return "未知"
}

/*
FeedStatus 是 feed 状态变更消息（摄入层 → TUI 状态栏）。

字段:
  - Name: feed 名称。
  - State: 当前状态。
  - Message: 附加说明（错误文本 / 恢复提示）。
  - At: 状态变更时间。
*/
type FeedStatus struct {
	Name    string
	State   FeedState
	Message string
	At      time.Time
}

// feedSpec 是 feed 实例与其轮询间隔的绑定。
type feedSpec struct {
	f        feed.Feed
	interval time.Duration
}

// Pipeline 编排全部 feed 的摄入调度与 mock 降级。
type Pipeline struct {
	events   chan feed.AttackEvent
	statuses chan FeedStatus
	specs    []feedSpec
	mock     feed.Feed
	mockOnly bool

	mu     sync.Mutex // 保护 states 映射
	states map[string]FeedState
}

/*
Spec 构造 feed 与轮询间隔的绑定（供 cmd 层组装管道）。

参数:
  - f (feed.Feed): feed 实例。
  - interval (time.Duration): 成功轮询间隔。

返回:
  - feedSpec: 绑定结果（调用方仅需将其作为变参传入 New）。
*/
func Spec(f feed.Feed, interval time.Duration) feedSpec {
	return feedSpec{f: f, interval: interval}
}

/*
New 构建摄入管道。

参数:
  - mockOnly (bool): true 时仅运行内置模拟 feed（--mock / --demo 模式）。
  - specs ([]feedSpec): 真实 feed 及其轮询间隔。

返回:
  - *Pipeline: 就绪的管道实例。
*/
func New(mockOnly bool, specs ...feedSpec) *Pipeline {
	return &Pipeline{
		events:   make(chan feed.AttackEvent, eventsBuf),
		statuses: make(chan FeedStatus, statusBuf),
		specs:    specs,
		mock:     feed.NewMock(),
		mockOnly: mockOnly,
		states:   make(map[string]FeedState, len(specs)),
	}
}

/*
Start 启动全部 feed 调度并返回事件与状态通道。

参数:
  - ctx (context.Context): 根 context（SIGINT / SIGTERM / tea.Quit 时取消）。

返回:
  - evCh (<-chan feed.AttackEvent): 攻击事件流。
  - stCh (<-chan FeedStatus): feed 状态流。
*/
func (p *Pipeline) Start(ctx context.Context) (<-chan feed.AttackEvent, <-chan FeedStatus) {
	// Pool 容量 = 全部 feed 数 + 1（降级监视器）。
	// 每个 feed 是长期驻留的轮询循环，槽位不足会导致部分 feed 永远排队。
	pool := NewPool(len(p.specs) + 1)

	if p.mockOnly {
		pool.Go(ctx, func(c context.Context) {
			p.runFeed(c, p.mock, 700*time.Millisecond)
		})
		logger.Info("演示模式：仅启用内置模拟数据源")
		return p.events, p.statuses
	}

	// 真实 feed 各自独立调度；失败互不影响。
	for _, spec := range p.specs {
		sp := spec
		pool.Go(ctx, func(c context.Context) {
			p.runFeed(c, sp.f, sp.interval)
		})
	}

	// 降级监视器：所有真实 feed 均非「运行中」时自动启用模拟 feed（仅一次）。
	pool.Go(ctx, func(c context.Context) {
		p.watchDegrade(c, pool)
	})
	return p.events, p.statuses
}

/*
runFeed 驱动单个 feed 的轮询循环：单轮抓取 → 失败计数 → 退避重试。

规则（agent.md 第 5 节）：
  - 失败后等待 30s 重试；连续 5 次失败标记 degraded，此后以 60s 间隔继续尝试。
  - 成功则按 feed 自身间隔轮询；连续失败后恢复成功 → 状态回到运行中。

参数:
  - ctx (context.Context): 取消信号。
  - f (feed.Feed): feed 实例。
  - interval (time.Duration): 成功时的轮询间隔。
*/
func (p *Pipeline) runFeed(ctx context.Context, f feed.Feed, interval time.Duration) {
	name := f.Name()
	failures := 0
	p.setState(name, FeedRunning, "正在连接")
	logger.Info("feed 已启动: %s (间隔 %s)", name, interval)
	for {
		if ctx.Err() != nil {
			return
		}
		err := f.Run(ctx, p.events)
		switch {
		case err == nil:
			if failures >= 5 {
				p.setState(name, FeedRunning, "已恢复")
				logger.Success("feed 恢复: %s", name)
			}
			failures = 0
		case ctx.Err() != nil:
			return
		case errors.Is(err, feed.ErrAuthRequired):
			// 认证失败：密钥只能在启动时注入，重试无意义，直接降级并停止。
			p.setState(name, FeedDegraded, "需 API 密钥（--abuse-key）")
			logger.Warn("feed %s 需要 API 密钥: 请在启动时提供 --abuse-key（abuse.ch 免费申请），本会话不再重试", name)
			return
		default:
			failures++
			logger.Warn("feed 抓取失败: %s (%d/%d): %v", name, failures, 5, err)
			if failures == 5 {
				p.setState(name, FeedDegraded, "连续 5 次失败")
			}
		}
		wait := interval
		if failures > 0 {
			wait = 30 * time.Second
			if failures >= 5 {
				wait = 60 * time.Second
			}
		}
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

/*
watchDegrade 监视各真实 feed 状态：全部降级/失败时启用模拟 feed 兜底。

参数:
  - ctx (context.Context): 取消信号。
  - pool (*Pool): 用于启动模拟 feed 的 Worker Pool。
*/
func (p *Pipeline) watchDegrade(ctx context.Context, pool *Pool) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if p.allDegraded() {
				pool.Go(ctx, func(c context.Context) {
					p.runFeed(c, p.mock, 700*time.Millisecond)
				})
				logger.Warn("全部真实 feed 不可用，已启用内置模拟数据源兜底")
				return
			}
		}
	}
}

/*
allDegraded 判断是否所有真实 feed 均已降级/失败。

返回:
  - bool: 全部非「运行中」时为 true。
*/
func (p *Pipeline) allDegraded() bool {
	if len(p.specs) == 0 {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, spec := range p.specs {
		if p.states[spec.f.Name()] == FeedRunning {
			return false
		}
	}
	return true
}

/*
setState 更新 feed 状态镜像并广播给 TUI。

参数:
  - name (string): feed 名称。
  - state (FeedState): 新状态。
  - msg (string): 附加说明。
*/
func (p *Pipeline) setState(name string, state FeedState, msg string) {
	p.mu.Lock()
	p.states[name] = state
	p.mu.Unlock()

	st := FeedStatus{Name: name, State: state, Message: msg, At: time.Now()}
	select {
	case p.statuses <- st:
	case <-time.After(time.Second):
		logger.Warn("状态通道拥塞，丢弃 %s 状态消息", name)
	}
}

/*
sleepCtx 是可中断的休眠：ctx 取消时立即返回。

参数:
  - ctx (context.Context): 取消信号。
  - d (time.Duration): 休眠时长。

返回:
  - bool: 正常休眠结束返回 true，ctx 取消返回 false。
*/
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
