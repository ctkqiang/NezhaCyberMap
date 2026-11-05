package feed

/*
本文件实现 TOR 出口节点数据源（免费，无需密钥）。

数据地址：https://check.torproject.org/torbulkexitlist
格式：每行一个出口节点 IP，约 30 分钟更新一次。
用途：标记来自匿名网络的攻击源（高匿名性，攻击溯源困难）。
轮询建议：1800 秒。
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

// torAPI 是 TOR 出口节点清单地址（测试中可覆盖）。
const torAPI = "https://check.torproject.org/torbulkexitlist"

// 每轮最大产出事件数（出口节点约 2000 个，采样前 N 条）。
const torPerRound = 50

// TORExits 是 check.torproject.org 的客户端。
type TORExits struct {
	resolver *geoip.Resolver
	baseURL  string
}

/*
NewTORExits 创建 TOR 出口节点数据源。

参数:
  - resolver (*geoip.Resolver): 地理定位解析器。

返回:
  - *TORExits: 就绪的数据源实例。
*/
func NewTORExits(resolver *geoip.Resolver) *TORExits {
	return &TORExits{resolver: resolver, baseURL: torAPI}
}

/*
Name 返回数据源显示名。

返回:
  - string: "TOR"。
*/
func (f *TORExits) Name() string { return "TOR" }

/*
Run 执行一轮抓取：下载出口节点清单，标记中等严重度（匿名来源）。

参数:
  - ctx (context.Context): 取消信号。
  - out (chan<- AttackEvent): 事件输出通道。

返回:
  - error: 本轮网络或解析错误。
*/
func (f *TORExits) Run(ctx context.Context, out chan<- AttackEvent) error {
	data, err := fetch(ctx, http.MethodGet, f.baseURL, nil, nil)
	if err != nil {
		return fmt.Errorf("请求 TOR 出口清单失败: %w", err)
	}
	ts := time.Now()
	emitted := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() && emitted < torPerRound {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		ip := parseIPText(line)
		if ip == nil {
			continue
		}
		ev, ok := buildEvent(f.resolver, f.Name(), ip, SeverityMedium, ip.String(), ts)
		if !ok {
			continue
		}
		if !sendEvent(ctx, out, ev) {
			return nil
		}
		emitted++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("解析 TOR 清单失败: %w", err)
	}
	if emitted > 0 {
		logger.Info("TOR: 本轮产出 %d 条事件", emitted)
	}
	return nil
}
