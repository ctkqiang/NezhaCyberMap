package feed

/*
本文件实现 Emerging Threats（Proofpoint）开放 IP 封锁清单数据源
（免费，无需密钥）。

数据地址：https://rules.emergingthreats.net/fwrules/emerging-Block-IPs.txt
格式：每行一个 IP，# 开头为注释；每日更新的活跃恶意 IP（C2 / 扫描器）。
轮询建议：3600 秒（每日更新）。
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

// emergingAPI 是 Emerging Threats 封锁清单地址（测试中可覆盖）。
const emergingAPI = "https://rules.emergingthreats.net/fwrules/emerging-Block-IPs.txt"

// 每轮最大产出事件数（清单含数千条，采样前 N 条）。
const emergingPerRound = 50

// EmergingThreats 是 rules.emergingthreats.net 的客户端。
type EmergingThreats struct {
	resolver *geoip.Resolver
	baseURL  string
}

/*
NewEmergingThreats 创建 Emerging Threats 数据源。

参数:
  - resolver (*geoip.Resolver): 地理定位解析器。

返回:
  - *EmergingThreats: 就绪的数据源实例。
*/
func NewEmergingThreats(resolver *geoip.Resolver) *EmergingThreats {
	return &EmergingThreats{resolver: resolver, baseURL: emergingAPI}
}

/*
Name 返回数据源显示名。

返回:
  - string: "EmergThreats"。
*/
func (f *EmergingThreats) Name() string { return "EmergThreats" }

/*
Run 执行一轮抓取：下载封锁清单，全部标记高严重度。

参数:
  - ctx (context.Context): 取消信号。
  - out (chan<- AttackEvent): 事件输出通道。

返回:
  - error: 本轮网络或解析错误。
*/
func (f *EmergingThreats) Run(ctx context.Context, out chan<- AttackEvent) error {
	data, err := fetch(ctx, http.MethodGet, f.baseURL, nil, nil)
	if err != nil {
		return fmt.Errorf("请求 Emerging Threats 清单失败: %w", err)
	}
	ts := time.Now()
	emitted := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() && emitted < emergingPerRound {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		ip := parseIPText(line)
		if ip == nil {
			continue
		}
		// 该清单为活跃恶意 IP 封锁清单，统一标记高。
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
		return fmt.Errorf("解析 Emerging Threats 清单失败: %w", err)
	}
	if emitted > 0 {
		logger.Info("EmergThreats: 本轮产出 %d 条事件", emitted)
	}
	return nil
}
