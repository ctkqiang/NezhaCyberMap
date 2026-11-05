package web

/*
本文件实现 WebSocket Hub：事件/统计的实时广播中心。

设计：
  - Hub.Run 是唯一的状态变更协程：消费摄入管道的事件与状态流，
    在持锁下更新 Stats 与最近事件环形缓冲区，然后向全部客户端广播。
  - 每个客户端有独立的缓冲发送通道（容量 256）；慢客户端溢出时丢弃该帧
    （统计帧每秒一次可自然恢复，事件帧可经 /api/events/recent 补拉）。
  - 暂停语义：pause 后事件照常摄入与计数，仅停止转发；统计帧持续推送
    使前端按钮状态保持同步。
*/

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/internal/feed"
	"github.com/ctkqiang/NezhaCyberMap/internal/ingest"
	"github.com/ctkqiang/NezhaCyberMap/pkg/logger"
	"github.com/ctkqiang/NezhaCyberMap/pkg/ringbuf"
	"github.com/gorilla/websocket"
)

// WebSocket 连接管理常量（心跳与缓冲）。
const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 54 * time.Second
	maxMsgSize = 512
	sendBufCap = 256
	statsEvery = time.Second
)

// 事件环形缓冲区容量（最近 500 条，供欢迎帧与新连接补拉）。
const eventBufCap = 500

/*
Hub 是广播中心：聚合状态并推送给全部 WebSocket 客户端。

字段:
  - events / statuses: 摄入管道的事件流与状态流。
  - stats: 统计聚合器（锁保护）。
  - ring: 最近事件环形缓冲区。
  - clientsMu / clients: 客户端注册表。
  - paused: 暂停标志（存于 stats 内）。
*/
type Hub struct {
	events   <-chan feed.AttackEvent
	statuses <-chan ingest.FeedStatus

	stats *Stats
	ring  *ringbuf.Ring[feed.AttackEvent]

	clientsMu sync.Mutex
	clients   map[*client]struct{}
}

// client 是单个 WebSocket 连接。
type client struct {
	conn *websocket.Conn
	send chan []byte
}

// wsMessage 是服务端 → 前端的统一消息信封。
type wsMessage struct {
	Type   string              `json:"type"`
	Stats  *StatsSnapshot      `json:"stats,omitempty"`
	Event  *eventJSON          `json:"event,omitempty"`
	Events []eventJSON         `json:"events,omitempty"`
	Feeds  map[string]FeedItem `json:"feeds,omitempty"`
}

// eventJSON 是单条攻击事件的传输格式。
type eventJSON struct {
	TS   int64     `json:"ts"`
	Src  pointJSON `json:"src"`
	Dst  pointJSON `json:"dst"`
	Sev  int       `json:"sev"`
	Feed string    `json:"feed"`
	Raw  string    `json:"raw"`
}

// pointJSON 是攻击源/目标点的传输格式。
type pointJSON struct {
	IP   string  `json:"ip,omitempty"`
	Code string  `json:"code"`
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}

// clientCommand 是前端 → 服务端的控制消息。
type clientCommand struct {
	Type   string `json:"type"`
	Paused bool   `json:"paused"`
}

/*
NewHub 创建广播中心。

参数:
  - events (<-chan feed.AttackEvent): 攻击事件流。
  - statuses (<-chan ingest.FeedStatus): feed 状态流。

返回:
  - *Hub: 就绪的 Hub 实例。
*/
func NewHub(events <-chan feed.AttackEvent, statuses <-chan ingest.FeedStatus) *Hub {
	return &Hub{
		events:   events,
		statuses: statuses,
		stats:    NewStats(),
		ring:     ringbuf.New[feed.AttackEvent](eventBufCap),
		clients:  make(map[*client]struct{}),
	}
}

/*
Run 启动 Hub 主循环：消费事件/状态流、定时广播统计。

参数:
  - ctx (context.Context): 根 context，取消时关闭全部客户端连接并退出。
*/
func (h *Hub) Run(ctx context.Context) {
	statsTicker := time.NewTicker(statsEvery)
	defer statsTicker.Stop()
	logger.Info("WebSocket Hub 已启动")
	for {
		select {
		case <-ctx.Done():
			h.closeAll()
			logger.Info("WebSocket Hub 已退出")
			return
		case ev, ok := <-h.events:
			if !ok {
				return
			}
			h.handleEvent(ev)
		case st, ok := <-h.statuses:
			if !ok {
				return
			}
			h.handleStatus(st)
		case <-statsTicker.C:
			h.broadcastStats()
		}
	}
}

/*
handleEvent 摄入单条事件：更新统计、写入环形缓冲区并广播。

参数:
  - ev (feed.AttackEvent): 新事件。
*/
func (h *Hub) handleEvent(ev feed.AttackEvent) {
	ej := toEventJSON(ev)
	h.stats.mu.Lock()
	h.stats.ingest(ev)
	h.ring.Push(ev)
	paused := h.stats.paused
	h.stats.mu.Unlock()

	if paused {
		return // 暂停：只计数不转发
	}
	msg, err := json.Marshal(wsMessage{Type: "event", Event: &ej})
	if err != nil {
		logger.Warn("事件序列化失败: %v", err)
		return
	}
	h.broadcast(msg)
}

/*
handleStatus 更新 feed 状态并广播。

参数:
  - st (ingest.FeedStatus): feed 状态消息。
*/
func (h *Hub) handleStatus(st ingest.FeedStatus) {
	h.stats.mu.Lock()
	h.stats.setFeed(st)
	h.stats.mu.Unlock()
	h.broadcastFeeds()
}

/*
broadcastStats 向全部客户端推送统计快照。

返回:
  - 无。
*/
func (h *Hub) broadcastStats() {
	h.stats.mu.RLock()
	snap := h.stats.snapshot()
	h.stats.mu.RUnlock()
	msg, err := json.Marshal(wsMessage{Type: "stats", Stats: &snap})
	if err != nil {
		logger.Warn("统计序列化失败: %v", err)
		return
	}
	h.broadcast(msg)
}

/*
broadcastFeeds 向全部客户端推送 feed 状态快照。

返回:
  - 无。
*/
func (h *Hub) broadcastFeeds() {
	h.stats.mu.RLock()
	feeds := h.stats.feedSnapshot()
	h.stats.mu.RUnlock()
	msg, err := json.Marshal(wsMessage{Type: "feeds", Feeds: feeds})
	if err != nil {
		logger.Warn("feed 状态序列化失败: %v", err)
		return
	}
	h.broadcast(msg)
}

/*
broadcast 将同一帧消息投递给全部客户端（慢客户端丢弃，不阻塞）。

参数:
  - msg ([]byte): 序列化后的消息帧。
*/
func (h *Hub) broadcast(msg []byte) {
	h.clientsMu.Lock()
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
			// 客户端消费过慢：丢弃本帧，统计帧每秒一次可自然恢复。
		}
	}
	h.clientsMu.Unlock()
}

/*
register 注册一个新客户端并启动读写泵。

参数:
  - c (*client): 新客户端。
*/
func (h *Hub) register(c *client) {
	h.clientsMu.Lock()
	h.clients[c] = struct{}{}
	h.clientsMu.Unlock()
	logger.Debug("WebSocket 客户端接入（当前 %d 个）", h.clientCount())
	go h.writePump(c)
	go h.readPump(c)
}

/*
unregister 移除客户端并关闭连接。

参数:
  - c (*client): 待移除客户端。
*/
func (h *Hub) unregister(c *client) {
	h.clientsMu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.clientsMu.Unlock()
	_ = c.conn.Close()
}

/*
closeAll 关闭全部客户端（ctx 取消时调用）。

返回:
  - 无。
*/
func (h *Hub) closeAll() {
	h.clientsMu.Lock()
	for c := range h.clients {
		close(c.send)
		_ = c.conn.Close()
	}
	h.clients = make(map[*client]struct{})
	h.clientsMu.Unlock()
}

/*
clientCount 返回当前客户端数量。

返回:
  - int: 客户端数。
*/
func (h *Hub) clientCount() int {
	h.clientsMu.Lock()
	defer h.clientsMu.Unlock()
	return len(h.clients)
}

/*
welcome 构造新连接欢迎帧：统计快照 + 最近事件 + feed 状态。

返回:
  - wsMessage: 欢迎帧。
*/
func (h *Hub) welcome() wsMessage {
	h.stats.mu.RLock()
	snap := h.stats.snapshot()
	feeds := h.stats.feedSnapshot()
	evs := h.ring.Slice() // 环形缓冲区与 stats 共用同一把锁保护
	h.stats.mu.RUnlock()

	recent := make([]eventJSON, 0, len(evs))
	if len(evs) > 50 {
		evs = evs[len(evs)-50:] // 仅回放最近 50 条
	}
	for _, ev := range evs {
		recent = append(recent, toEventJSON(ev))
	}
	return wsMessage{Type: "welcome", Stats: &snap, Events: recent, Feeds: feeds}
}

/*
setPaused 切换暂停状态并立即推送统计帧同步前端按钮。

参数:
  - paused (bool): 是否暂停。
*/
func (h *Hub) setPaused(paused bool) {
	h.stats.mu.Lock()
	h.stats.setPaused(paused)
	h.stats.mu.Unlock()
	logger.Info("事件转发已暂停: %v", paused)
	h.broadcastStats()
}

/*
readPump 读取客户端上行消息（控制命令），检测断开后注销。

参数:
  - c (*client): 客户端。
*/
func (h *Hub) readPump(c *client) {
	defer h.unregister(c)
	c.conn.SetReadLimit(maxMsgSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var cmd clientCommand
		if err := json.Unmarshal(data, &cmd); err != nil {
			continue
		}
		switch cmd.Type {
		case "pause":
			h.setPaused(cmd.Paused)
		case "ping":
			// 心跳回执：无需动作。
		}
	}
}

/*
writePump 将发送队列写入连接并维护心跳。

参数:
  - c (*client): 客户端。
*/
func (h *Hub) writePump(c *client) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

/*
toEventJSON 将内部事件转换为传输格式。

参数:
  - ev (feed.AttackEvent): 内部事件。

返回:
  - eventJSON: 传输格式事件。
*/
func toEventJSON(ev feed.AttackEvent) eventJSON {
	ej := eventJSON{
		TS:   ev.Timestamp.UnixMilli(),
		Sev:  int(ev.Severity),
		Feed: ev.FeedName,
		Raw:  ev.RawIndicator,
		Src: pointJSON{
			Code: ev.SourceCode, Name: ev.SourceCountry,
			Lat: ev.SourceLat, Lon: ev.SourceLon,
		},
		Dst: pointJSON{
			Code: ev.TargetCode, Name: ev.TargetCountry,
			Lat: ev.TargetLat, Lon: ev.TargetLon,
		},
	}
	if ev.SourceIP != nil {
		ej.Src.IP = ev.SourceIP.String()
	}
	return ej
}
