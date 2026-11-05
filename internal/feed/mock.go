package feed

/*
本文件实现确定性模拟数据生成器（mock feed）。

用途：
  1. --mock / --demo 模式：完全离线演示，无需任何网络。
  2. 降级兜底：所有真实 feed 连续失败时，由 ingest 层自动启用，
     保证地图在任何网络条件下都有动画可看。

确定性保证：使用固定种子的 PCG 随机源，同一版本的程序每次运行
产生完全相同的攻击序列，便于复现与测试。
*/

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/pkg/logger"
)

// mockAttacker 描述一个模拟攻击源（国家 + 抽样 IP 前缀）。
type mockAttacker struct {
	code    string
	name    string
	lat     float64
	lon     float64
	prefixA uint8 // IP 前缀第一段
	prefixB uint8 // IP 前缀第二段
}

// mockAttackers 是内置攻击源清单：历史攻击流量高频来源国。
var mockAttackers = []mockAttacker{
	{"RU", "俄罗斯", 55.75, 37.61, 185, 220}, // 185.220.x.x（常见扫描段）
	{"CN", "中国", 35.86, 104.20, 112, 85},  // 112.85.x.x
	{"US", "美国", 39.83, -98.58, 45, 33},   // 45.33.x.x（Linode 等 VPS）
	{"KP", "朝鲜", 40.34, 127.51, 175, 45},  // 175.45.x.x
	{"IR", "伊朗", 32.43, 53.69, 5, 160},    // 5.160.x.x
	{"BR", "巴西", -14.24, -51.93, 177, 92}, // 177.92.x.x
	{"VN", "越南", 14.06, 108.28, 113, 185}, // 113.185.x.x
	{"IN", "印度", 20.59, 78.96, 152, 58},   // 152.58.x.x
	{"NL", "荷兰", 52.13, 5.29, 45, 148},    // 45.148.x.x（托管商）
	{"DE", "德国", 51.17, 10.45, 91, 240},   // 91.240.x.x
	{"RO", "罗马尼亚", 45.94, 24.97, 86, 120}, // 86.120.x.x
	{"ID", "印度尼西亚", -0.79, 113.92, 103, 94},
	{"UA", "乌克兰", 48.38, 31.17, 91, 200},
	{"TR", "土耳其", 38.96, 35.24, 88, 250},
}

// 严重度权重：低 50% / 中 28% / 高 15% / 严重 7%。
const (
	mockWeightLow      = 50
	mockWeightMedium   = 28
	mockWeightHigh     = 15
	mockWeightCritical = 7
)

/*
Mock 是确定性模拟数据源。

字段:
  - rng: 固定种子 PCG 随机源（确定性）。
  - count: 已生成事件总数（用于构造可复现的指标文本）。
*/
type Mock struct {
	rng   *rand.Rand
	count uint64
}

/*
NewMock 创建模拟数据源。

参数:
  - 无。

返回:
  - *Mock: 就绪的模拟数据源实例。
*/
func NewMock() *Mock {
	// 固定种子：保证每次运行的攻击序列完全一致。
	return &Mock{rng: rand.New(rand.NewPCG(20251103, 42))}
}

/*
Name 返回数据源显示名。

返回:
  - string: "Mock"。
*/
func (m *Mock) Name() string { return "Mock" }

/*
Run 生成一条模拟攻击事件（单轮）。ingest 层以固定间隔（默认 700ms）调用。

参数:
  - ctx (context.Context): 取消信号。
  - out (chan<- AttackEvent): 事件输出通道。

返回:
  - error: 恒为 nil（本地生成，无失败路径）。
*/
func (m *Mock) Run(ctx context.Context, out chan<- AttackEvent) error {
	m.count++
	attacker := mockAttackers[m.rng.IntN(len(mockAttackers))]
	ipText := fmt.Sprintf("%d.%d.%d.%d",
		attacker.prefixA, attacker.prefixB,
		m.rng.IntN(255), m.rng.IntN(254)+1)
	sev := m.rollSeverity()
	ts := time.Now()
	raw := ipText + ":" + fmt.Sprintf("%d", m.rng.IntN(64511)+1024)
	target := pickHoneypot(raw)

	ev := AttackEvent{
		Timestamp:     ts,
		SourceIP:      net.ParseIP(ipText),
		SourceCode:    attacker.code,
		SourceCountry: attacker.name,
		SourceLat:     attacker.lat,
		SourceLon:     attacker.lon,
		TargetCode:    target.code,
		TargetCountry: target.name,
		TargetLat:     target.lat,
		TargetLon:     target.lon,
		Severity:      sev,
		FeedName:      m.Name(),
		RawIndicator:  raw,
	}
	if ev.Severity == SeverityCritical {
		// 关键情报走 Success 日志（地图之外的听觉化信息源）。
		logger.Success("模拟: 检测到严重攻击事件 %s (%s → %s)",
			raw, attacker.name, target.name)
	}
	sendEvent(ctx, out, ev)
	return nil
}

/*
rollSeverity 按权重表掷出严重度。

返回:
  - Severity: 掷出的严重度等级。
*/
func (m *Mock) rollSeverity() Severity {
	total := mockWeightLow + mockWeightMedium + mockWeightHigh + mockWeightCritical
	r := m.rng.IntN(total)
	switch {
	case r < mockWeightLow:
		return SeverityLow
	case r < mockWeightLow+mockWeightMedium:
		return SeverityMedium
	case r < mockWeightLow+mockWeightMedium+mockWeightHigh:
		return SeverityHigh
	default:
		return SeverityCritical
	}
}
