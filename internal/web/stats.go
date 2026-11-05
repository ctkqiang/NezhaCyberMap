/*
Package web 实现赛博威胁地图的 Web 服务端：Gin 路由 + WebSocket 实时推送 +
服务端统计聚合 + 静态资源嵌入（go:embed，单二进制部署）。
*/
package web

import (
	"sync"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/internal/feed"
	"github.com/ctkqiang/NezhaCyberMap/internal/ingest"
)

/*
Stats 聚合全部展示级统计状态：事件总数、每秒速率环、攻击源国家计数、
严重度分布与 feed 状态。读写由 sync.RWMutex 保护（Hub 的 Run 协程写入，
HTTP 处理器与广播路径读取快照）。
*/
type Stats struct {
	mu        sync.RWMutex
	total     int
	rateRing  [60]int
	rateIdx   int
	countries map[string]*countryStat
	sevCounts [4]int
	start     time.Time
	paused    bool
	feeds     map[string]ingest.FeedStatus
}

// countryStat 是单个攻击源国家的计数。
type countryStat struct {
	code  string
	name  string
	count int
}

/*
NewStats 创建统计聚合器。

参数:
  - 无。

返回:
  - *Stats: 就绪的聚合器实例。
*/
func NewStats() *Stats {
	return &Stats{
		countries: make(map[string]*countryStat, 32),
		feeds:     make(map[string]ingest.FeedStatus, 4),
		start:     time.Now(),
	}
}

/*
ingest 摄入一条事件并更新全部计数（调用方需持有写锁或位于 Run 协程内）。

参数:
  - ev (feed.AttackEvent): 新事件。
*/
func (s *Stats) ingest(ev feed.AttackEvent) {
	s.total++
	cs, ok := s.countries[ev.SourceCode]
	if !ok {
		cs = &countryStat{code: ev.SourceCode, name: ev.SourceCountry}
		s.countries[ev.SourceCode] = cs
	}
	cs.count++
	if ev.Severity <= feed.SeverityCritical {
		s.sevCounts[ev.Severity]++
	}
	// 每秒速率环：处理秒跳跃，清空中间槽位。
	idx := int(time.Now().Unix() % 60)
	if idx != s.rateIdx {
		for k := (s.rateIdx + 1) % 60; k != idx; k = (k + 1) % 60 {
			s.rateRing[k] = 0
			if k == s.rateIdx {
				break
			}
		}
		s.rateRing[idx] = 0
		s.rateIdx = idx
	}
	s.rateRing[idx]++
}

/*
setFeed 更新单个 feed 的状态。

参数:
  - st (ingest.FeedStatus): feed 状态消息。
*/
func (s *Stats) setFeed(st ingest.FeedStatus) {
	s.feeds[st.Name] = st
}

/*
setPaused 设置暂停标志（暂停时 Hub 停止转发事件，统计照常推进）。

参数:
  - paused (bool): 是否暂停。
*/
func (s *Stats) setPaused(paused bool) {
	s.paused = paused
}

/*
ratePerMinute 计算最近 60 秒的事件速率（需持有读锁）。

返回:
  - int: 事件数/分钟。
*/
func (s *Stats) ratePerMinute() int {
	sum := 0
	for _, v := range s.rateRing {
		sum += v
	}
	return sum
}

// StatsSnapshot 是发送给前端的统计快照（JSON 序列化）。
type StatsSnapshot struct {
	Total     int           `json:"total"`
	Rate      int           `json:"rate"`
	UptimeSec int64         `json:"uptime"`
	Paused    bool          `json:"paused"`
	Countries []CountryItem `json:"countries"`
	Severity  [4]int        `json:"severity"`
	Spark     [30]int       `json:"spark"`
}

// CountryItem 是 Top 国家条目。
type CountryItem struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// FeedItem 是 feed 状态条目。
type FeedItem struct {
	State int    `json:"state"`
	Name  string `json:"name"`
	Msg   string `json:"msg"`
	At    int64  `json:"at"`
}

/*
snapshot 生成统计快照（调用方需持有读锁）。

返回:
  - StatsSnapshot: 快照数据。
*/
func (s *Stats) snapshot() StatsSnapshot {
	snap := StatsSnapshot{
		Total:     s.total,
		Rate:      s.ratePerMinute(),
		UptimeSec: int64(time.Since(s.start).Seconds()),
		Paused:    s.paused,
		Severity:  s.sevCounts,
	}
	// 最近 30 秒火花线（当前秒为最新）。
	slots := 30
	for i := 0; i < slots; i++ {
		snap.Spark[i] = s.rateRing[(s.rateIdx-slots+1+i+60)%60]
	}
	// 攻击源国家 Top10。
	type pair struct {
		cs *countryStat
	}
	list := make([]*countryStat, 0, len(s.countries))
	for _, cs := range s.countries {
		list = append(list, cs)
	}
	sortCountries(list)
	if len(list) > 10 {
		list = list[:10]
	}
	for _, cs := range list {
		snap.Countries = append(snap.Countries, CountryItem{
			Code: cs.code, Name: cs.name, Count: cs.count,
		})
	}
	return snap
}

/*
feedSnapshot 生成 feed 状态快照（调用方需持有读锁）。

返回:
  - map[string]FeedItem: feed 名 → 状态条目。
*/
func (s *Stats) feedSnapshot() map[string]FeedItem {
	out := make(map[string]FeedItem, len(s.feeds))
	for name, st := range s.feeds {
		out[name] = FeedItem{
			State: int(st.State),
			Name:  st.State.Name(),
			Msg:   st.Message,
			At:    st.At.UnixMilli(),
		}
	}
	return out
}

// sortCountries 按计数降序排序国家列表（插入排序，Top10 规模足够快且零额外分配）。
func sortCountries(list []*countryStat) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].count > list[j-1].count; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
