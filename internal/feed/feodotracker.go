package feed

/*
本文件实现 Feodo Tracker 数据源（abuse.ch）：拉取推荐 C2 封锁清单
（纯文本，每行一个活跃 C2 IP），全部标记为「高」严重度。

数据地址：https://feodotracker.abuse.ch/downloads/ipblocklist_recommended.txt
轮询建议：300 秒（该清单由后端定期更新，无需高频拉取）。
*/

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/pkg/geoip"
	"github.com/ctkqiang/NezhaCyberMap/pkg/logger"
)

// feodoAPI 是 Feodo Tracker 推荐封锁清单地址（测试中可覆盖）。
const feodoAPI = "https://feodotracker.abuse.ch/downloads/ipblocklist_recommended.txt"

/*
FeodoTracker 是 feodotracker.abuse.ch 的客户端。

字段:
  - resolver: 地理定位解析器。
  - baseURL: 清单地址（默认 feodoAPI，测试时注入 httptest 地址）。
*/
type FeodoTracker struct {
	resolver *geoip.Resolver
	baseURL  string
}

/*
NewFeodoTracker 创建 Feodo Tracker 数据源。

参数:
  - resolver (*geoip.Resolver): 地理定位解析器。

返回:
  - *FeodoTracker: 就绪的数据源实例。
*/
func NewFeodoTracker(resolver *geoip.Resolver) *FeodoTracker {
	return &FeodoTracker{resolver: resolver, baseURL: feodoAPI}
}

/*
Name 返回数据源显示名。

返回:
  - string: "Feodo"。
*/
func (f *FeodoTracker) Name() string { return "Feodo" }

/*
Run 执行一轮抓取：下载推荐 C2 清单，逐行解析 IP 并产出高严重度事件。

参数:
  - ctx (context.Context): 取消信号。
  - out (chan<- AttackEvent): 事件输出通道。

返回:
  - error: 本轮网络或解析错误。
*/
func (f *FeodoTracker) Run(ctx context.Context, out chan<- AttackEvent) error {
	data, err := fetch(ctx, http.MethodGet, f.baseURL, nil, nil)
	if err != nil {
		return fmt.Errorf("请求 Feodo 清单失败: %w", err)
	}
	ts := time.Now()
	emitted := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue // 跳过注释与空行
		}
		ip := parseIPText(line)
		if ip == nil {
			continue
		}
		// 推荐清单中的 IP 均为活跃 C2 节点，统一标记「高」。
		ev, ok := buildEvent(f.resolver, f.Name(), ip, SeverityHigh, ip.String(), ts)
		if !ok {
			continue
		}
		if !sendEvent(ctx, out, ev) {
			return nil
		}
		emitted++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("解析 Feodo 清单失败: %w", err)
	}
	if emitted > 0 {
		logger.Info("Feodo: 本轮产出 %d 条事件", emitted)
	}
	return nil
}
