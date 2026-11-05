package web

/*
本文件测试 Web 层：统计聚合、HTTP 接口与 WebSocket 端到端推送。
使用 mock 摄入管道 + httptest 服务器，禁止访问真实外部 API。
*/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/internal/feed"
	"github.com/ctkqiang/NezhaCyberMap/internal/ingest"
	"github.com/gorilla/websocket"
)

// TestStatsIngest 验证统计聚合：总数、国家计数、严重度、速率环。
func TestStatsIngest(t *testing.T) {
	s := NewStats()
	ev := feed.AttackEvent{
		Timestamp:     time.Now(),
		SourceCode:    "RU",
		SourceCountry: "俄罗斯",
		Severity:      feed.SeverityHigh,
	}
	for i := 0; i < 5; i++ {
		s.ingest(ev)
	}
	s.mu.RLock()
	snap := s.snapshot()
	s.mu.RUnlock()

	if snap.Total != 5 {
		t.Errorf("Total = %d，期望 5", snap.Total)
	}
	if snap.Severity[feed.SeverityHigh] != 5 {
		t.Errorf("Severity[高] = %d，期望 5", snap.Severity[feed.SeverityHigh])
	}
	if len(snap.Countries) != 1 || snap.Countries[0].Code != "RU" || snap.Countries[0].Count != 5 {
		t.Errorf("Countries = %+v，期望 RU×5", snap.Countries)
	}
}

// TestStatsFeedState 验证 feed 状态与快照。
func TestStatsFeedState(t *testing.T) {
	s := NewStats()
	s.setFeed(ingest.FeedStatus{Name: "Mock", State: ingest.FeedRunning, Message: "运行中"})
	s.mu.RLock()
	feeds := s.feedSnapshot()
	s.mu.RUnlock()
	f, ok := feeds["Mock"]
	if !ok || f.State != int(ingest.FeedRunning) || f.Name != "运行中" {
		t.Errorf("feedSnapshot = %+v，期望 Mock/运行中", feeds)
	}
}

// newTestServer 启动 mock 管道 + Hub + httptest 服务器。
func newTestServer(t *testing.T) (*httptest.Server, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p := ingest.New(true) // mock 模式
	events, statuses := p.Start(ctx)
	hub := NewHub(events, statuses)
	go hub.Run(ctx)
	srv := httptest.NewServer(NewRouter(hub))
	t.Cleanup(func() {
		cancel()
		srv.Close()
	})
	return srv, cancel
}

// TestHTTPEndpoints 验证 HTTP 接口基本行为。
func TestHTTPEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)

	// 首页
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET / 失败: %v", err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "赛博威胁地图") {
		t.Errorf("首页响应异常: %d", resp.StatusCode)
	}

	// 静态资源（模块入口 + 样式 + 数据）。
	for _, path := range []string{"/static/js/main.mjs", "/static/css/style.css", "/static/world-data.js"} {
		resp, err = http.Get(srv.URL + path)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Errorf("静态资源 %s 异常: %v", path, err)
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("静态资源 %s 缺少 no-cache 头: %q", path, cc)
		}
	}

	// 健康检查
	resp, err = http.Get(srv.URL + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("healthz 异常: %v", err)
	}

	// 统计接口（mock 700ms 一个事件，等待后应有数据）
	time.Sleep(1500 * time.Millisecond)
	resp, err = http.Get(srv.URL + "/api/stats")
	if err != nil {
		t.Fatalf("GET /api/stats 失败: %v", err)
	}
	var snap StatsSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("stats 解析失败: %v", err)
	}
	if snap.Total < 1 {
		t.Errorf("mock 模式 1.5s 后 Total = %d，期望 >= 1", snap.Total)
	}
	if len(snap.Countries) == 0 {
		t.Error("Countries 为空")
	}
}

// TestWebSocketFlow 验证 WebSocket 全链路：欢迎帧 → 事件帧 → 暂停控制。
func TestWebSocketFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket 连接失败: %v", err)
	}
	defer conn.Close()

	// 欢迎帧
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读取欢迎帧失败: %v", err)
	}
	var welcome wsMessage
	if err := json.Unmarshal(data, &welcome); err != nil {
		t.Fatalf("欢迎帧解析失败: %v", err)
	}
	if welcome.Type != "welcome" || welcome.Stats == nil {
		t.Errorf("欢迎帧异常: type=%s stats=%v", welcome.Type, welcome.Stats)
	}

	// 事件帧（mock 700ms 一条，5 秒内必达）
	gotEvent := false
	deadline := time.Now().Add(5 * time.Second)
	conn.SetReadDeadline(deadline)
	for time.Now().Before(deadline) {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg wsMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Type == "event" && msg.Event != nil {
			gotEvent = true
			if msg.Event.Src.Lat == 0 && msg.Event.Src.Lon == 0 {
				t.Error("事件坐标为空")
			}
			break
		}
	}
	if !gotEvent {
		t.Fatal("5 秒内未收到事件帧")
	}

	// 暂停命令 → 统计帧回传 paused=true
	if err := conn.WriteJSON(clientCommand{Type: "pause", Paused: true}); err != nil {
		t.Fatalf("发送暂停命令失败: %v", err)
	}
	gotPaused := false
	deadline = time.Now().Add(3 * time.Second)
	conn.SetReadDeadline(deadline)
	for time.Now().Before(deadline) {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg wsMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Type == "stats" && msg.Stats != nil && msg.Stats.Paused {
			gotPaused = true
			break
		}
	}
	if !gotPaused {
		t.Fatal("暂停命令后未收到 paused=true 的统计帧")
	}
}

// readAll 读取响应体并返回文本。
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	buf := make([]byte, 64<<10)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}
