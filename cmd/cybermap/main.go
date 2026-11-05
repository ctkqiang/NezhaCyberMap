/*
赛博威胁地图（cybermap）—— Web 版实时 OSINT 网络攻击地图。

灵感来自 Kaspersky Cybermap：持续摄入公开威胁情报流（ThreatFox /
Feodo Tracker / URLhaus），通过 GeoIP 解析将攻击源与蜜罐目标投影到
浏览器 Canvas 世界地图，实时绘制攻击弧线、事件流水与威胁统计面板。

子命令：
  - serve（默认）: 启动 Web 服务（Gin + WebSocket）。
  - feed:          列出可用威胁数据源。
  - version:       打印版本信息。
*/
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/internal/feed"
	"github.com/ctkqiang/NezhaCyberMap/internal/ingest"
	"github.com/ctkqiang/NezhaCyberMap/internal/web"
	"github.com/ctkqiang/NezhaCyberMap/pkg/geoip"
	"github.com/ctkqiang/NezhaCyberMap/pkg/logger"
	"github.com/ctkqiang/NezhaCyberMap/pkg/version"
)

// feedInfo 是「feed 子命令」输出的数据源说明。
type feedInfo struct {
	name     string
	source   string
	interval string
	desc     string
}

// feedList 是全部可用数据源（含内置模拟源）。
var feedList = []feedInfo{
	{"ThreatFox", "https://threatfox-api.abuse.ch/api/v1/", "60s", "IOC 情报（ip:port / C2 / 恶意软件），需 abuse.ch API 密钥"},
	{"Feodo", "https://feodotracker.abuse.ch/", "300s", "活跃 C2 服务器推荐封锁清单（无需密钥）"},
	{"URLhaus", "https://urlhaus-api.abuse.ch/", "300s", "恶意软件投放载荷（IP / 域名），需 abuse.ch API 密钥"},
	{"Blocklist.de", "https://api.blocklist.de/", "60s", "蜜罐网络实时攻击日志（attacker → 蜜罐，无需密钥）"},
	{"CINS", "https://cinsscore.com/list/ci-badguys.txt", "1h", "带信誉评分的恶意 IP 清单（无需密钥）"},
	{"EmergThreats", "https://rules.emergingthreats.net/", "1h", "Proofpoint 开放恶意 IP 封锁清单（无需密钥）"},
	{"TOR", "https://check.torproject.org/", "30m", "TOR 匿名网络出口节点（无需密钥）"},
	{"Mock", "内置（确定性伪随机）", "700ms", "离线演示 / 真实源全部失败时的降级兜底"},
}

// main 解析子命令并分发。
func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "feed":
		printFeeds()
	case "version":
		fmt.Println(version.String())
	case "help", "-h", "--help":
		printUsage()
	case "serve":
		serve(args)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", cmd)
		printUsage()
		os.Exit(2)
	}
}

/*
printUsage 打印使用说明。

返回:
  - 无。
*/
func printUsage() {
	fmt.Println("用法: cybermap [子命令] [flags]")
	fmt.Println()
	fmt.Println("子命令:")
	fmt.Println("  serve     启动 Web 服务（默认）")
	fmt.Println("  feed      列出可用威胁数据源")
	fmt.Println("  version   打印版本信息")
	fmt.Println()
	fmt.Println("serve 子命令 flags:")
	fmt.Println("  --host 地址          监听地址（默认 0.0.0.0）")
	fmt.Println("  --port 端口          监听端口（默认 8080）")
	fmt.Println("  --mock               仅使用内置模拟数据源（离线演示）")
	fmt.Println("  --demo               同 --mock")
	fmt.Println("  --geoip-db 路径       加载 MaxMind GeoLite2-City 数据库（可选）")
	fmt.Println("  --abuse-key 密钥      abuse.ch API 密钥（ThreatFox / URLhaus 需要，可免费申请）")
	fmt.Println("  --debug              启用调试日志")
}

/*
printFeeds 打印数据源列表。

返回:
  - 无。
*/
func printFeeds() {
	fmt.Println("可用威胁数据源:")
	fmt.Println()
	for _, f := range feedList {
		fmt.Printf("  %-10s 间隔 %-6s %s\n", f.name, f.interval, f.desc)
		fmt.Printf("  %-10s 来源  %s\n", "", f.source)
	}
	fmt.Println()
	fmt.Println("说明: 真实源全部连续失败 5 次后自动启用内置 Mock 源兜底。")
}

/*
serve 启动 Web 服务。

参数:
  - args ([]string): serve 子命令的 flags。
*/
func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	host := fs.String("host", "0.0.0.0", "监听地址")
	port := fs.Int("port", 8080, "监听端口")
	mock := fs.Bool("mock", false, "仅使用内置模拟数据源（离线演示）")
	demo := fs.Bool("demo", false, "同 --mock（离线演示模式）")
	debug := fs.Bool("debug", false, "启用调试日志")
	geoipDB := fs.String("geoip-db", "", "MaxMind GeoLite2-City 数据库文件路径")
	threatFoxKey := fs.String("threatfox-key", "", "已废弃：同 --abuse-key")
	abuseKey := fs.String("abuse-key", "", "abuse.ch API 密钥（ThreatFox / URLhaus 共用，可在 abuse.ch 免费申请）")
	if err := fs.Parse(args); err != nil {
		logger.Fatal("解析参数失败: %v", err)
	}
	logger.SetDebug(*debug)

	// API 密钥：--abuse-key 优先，--threatfox-key 为历史别名。
	key := *abuseKey
	if key == "" {
		key = *threatFoxKey
	}

	// GeoIP：优先加载 .mmdb 数据库，失败回退内置静态表。
	resolver := geoip.NewResolver()
	if *geoipDB != "" {
		if err := resolver.LoadMMDB(*geoipDB); err != nil {
			logger.Warn("GeoIP 数据库加载失败，回退内置静态表: %v", err)
		} else {
			logger.Info("已加载 GeoIP 数据库: %s", *geoipDB)
		}
	} else {
		logger.Warn("未加载 GeoIP 数据库：使用内置粗粒度静态表（仅覆盖常见网段，未收录的 IP 会被跳过，国家分布会失真）")
		logger.Warn("建议: --geoip-db /path/to/GeoLite2-City.mmdb（MaxMind 免费，见 README）")
	}

	// 根 context：SIGINT / SIGTERM 级联取消全部摄入 goroutine。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 组装摄入管道：7 个真实 feed + 自动降级 Mock。
	mockOnly := *mock || *demo
	pipeline := ingest.New(mockOnly,
		ingest.Spec(feed.NewThreatFox(resolver, key), 60*time.Second),
		ingest.Spec(feed.NewFeodoTracker(resolver), 300*time.Second),
		ingest.Spec(feed.NewURLhaus(resolver, key), 300*time.Second),
		ingest.Spec(feed.NewBlocklistDE(resolver), 60*time.Second),
		ingest.Spec(feed.NewCINS(resolver), 3600*time.Second),
		ingest.Spec(feed.NewEmergingThreats(resolver), 3600*time.Second),
		ingest.Spec(feed.NewTORExits(resolver), 1800*time.Second),
	)
	eventsCh, statusCh := pipeline.Start(ctx)

	// 广播中心 + Gin 路由。
	hub := web.NewHub(eventsCh, statusCh)
	go hub.Run(ctx)
	router := web.NewRouter(hub)

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// ctx 取消 → 优雅关停 HTTP 服务（3 秒宽限）。
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Success("赛博威胁地图已启动: http://%s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatal("HTTP 服务异常退出: %v", err)
	}
	stop() // 取消根 ctx → 摄入管道排空退出
	logger.Info("赛博威胁地图已退出")
}
