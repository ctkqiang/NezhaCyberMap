package web

/*
本文件实现 Gin 路由与 HTTP 处理器：页面、静态资源、WebSocket 升级、
统计与最近事件查询接口。
*/

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/ctkqiang/NezhaCyberMap/pkg/logger"
	webassets "github.com/ctkqiang/NezhaCyberMap/web"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// wsUpgrader 是 WebSocket 升级器。公开只读数据，允许任意来源连接。
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin: func(*http.Request) bool {
		return true
	},
}

/*
NewRouter 构建 Gin 路由。

参数:
  - hub (*Hub): 广播中心。

返回:
  - *gin.Engine: 就绪的路由引擎。
*/
func NewRouter(hub *Hub) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/", serveIndex)
	staticFS, err := fs.Sub(webassets.Static, "static")
	if err != nil {
		logger.Fatal("内嵌静态资源加载失败: %v", err)
	}
	// 静态资源（Any = GET+HEAD；无缓存头：前端迭代频繁，禁止浏览器缓存旧版本）。
	r.Any("/static/*name", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		http.StripPrefix("/static", http.FileServer(http.FS(staticFS))).ServeHTTP(c.Writer, c.Request)
	})

	r.GET("/ws", hub.serveWS)
	r.GET("/api/stats", hub.handleStats)
	r.GET("/api/events/recent", hub.handleRecent)
	r.GET("/api/feeds", hub.handleFeeds)
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	return r
}

/*
serveIndex 输出主页面。

参数:
  - c (*gin.Context): Gin 上下文。
*/
func serveIndex(c *gin.Context) {
	c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	data, err := webassets.Static.ReadFile("static/index.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "index.html 加载失败")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", data)
}

/*
serveWS 将 HTTP 连接升级为 WebSocket 并注册到 Hub。

参数:
  - c (*gin.Context): Gin 上下文。
*/
func (h *Hub) serveWS(c *gin.Context) {
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Warn("WebSocket 升级失败: %v", err)
		return
	}
	client := &client{conn: conn, send: make(chan []byte, sendBufCap)}
	h.register(client)
	// 欢迎帧：统计 + 最近事件 + feed 状态。
	msg, err := json.Marshal(h.welcome())
	if err != nil {
		logger.Warn("欢迎帧序列化失败: %v", err)
		return
	}
	select {
	case client.send <- msg:
	default:
	}
}

/*
handleStats 返回统计快照（HTTP GET /api/stats）。

参数:
  - c (*gin.Context): Gin 上下文。
*/
func (h *Hub) handleStats(c *gin.Context) {
	h.stats.mu.RLock()
	snap := h.stats.snapshot()
	h.stats.mu.RUnlock()
	c.JSON(http.StatusOK, snap)
}

/*
handleRecent 返回最近 N 条事件（HTTP GET /api/events/recent?limit=N）。

参数:
  - c (*gin.Context): Gin 上下文。
*/
func (h *Hub) handleRecent(c *gin.Context) {
	limit := 50
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	h.stats.mu.RLock()
	evs := h.ring.Slice() // 与 Run 协程的写入互斥
	h.stats.mu.RUnlock()
	if len(evs) > limit {
		evs = evs[len(evs)-limit:]
	}
	out := make([]eventJSON, 0, len(evs))
	for _, ev := range evs {
		out = append(out, toEventJSON(ev))
	}
	c.JSON(http.StatusOK, gin.H{"events": out})
}

/*
handleFeeds 返回 feed 状态快照（HTTP GET /api/feeds）。

参数:
  - c (*gin.Context): Gin 上下文。
*/
func (h *Hub) handleFeeds(c *gin.Context) {
	h.stats.mu.RLock()
	feeds := h.stats.feedSnapshot()
	h.stats.mu.RUnlock()
	c.JSON(http.StatusOK, feeds)
}
