package feed

/*
本文件实现 CINS Army 数据源：带信誉评分的恶意 IP 清单（免费，无需密钥）。

数据地址：https://cinsscore.com/list/ci-badguys.txt
格式：每行 "IP 评分"（评分 0-100，越高越恶意），# 开头为注释。
轮询建议：3600 秒（每日更新）。
*/

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/pkg/geoip"
	"github.com/ctkqiang/NezhaCyberMap/pkg/logger"
)

// cinsAPI 是 CINS Army 清单地址（测试中可覆盖）。
const cinsAPI = "https://cinsscore.com/list/ci-badguys.txt"

// 每轮最大产出事件数（清单含数万条，采样前 N 条）。
const cinsPerRound = 50

// 清单格式说明：ci-badguys.txt 为纯 IP 列表（每行一个 IP）；
// 兼容 "IP 评分" 两列格式（评分 0-100），无评分时按威胁清单整体标记「中」。
const cinsBaseSeverity = SeverityMedium

// CINS 是 cinsscore.com 的客户端。
type CINS struct {
	resolver *geoip.Resolver
	baseURL  string
}

/*
NewCINS 创建 CINS Army 数据源。

参数:
  - resolver (*geoip.Resolver): 地理定位解析器。

返回:
  - *CINS: 就绪的数据源实例。
*/
func NewCINS(resolver *geoip.Resolver) *CINS {
	return &CINS{resolver: resolver, baseURL: cinsAPI}
}

/*
Name 返回数据源显示名。

返回:
  - string: "CINS"。
*/
func (f *CINS) Name() string { return "CINS" }

/*
Run 执行一轮抓取：下载清单并按评分映射严重度产出事件。

参数:
  - ctx (context.Context): 取消信号。
  - out (chan<- AttackEvent): 事件输出通道。

返回:
  - error: 本轮网络或解析错误。
*/
func (f *CINS) Run(ctx context.Context, out chan<- AttackEvent) error {
	data, err := fetch(ctx, http.MethodGet, f.baseURL, nil, nil)
	if err != nil {
		return fmt.Errorf("请求 CINS 清单失败: %w", err)
	}
	ts := time.Now()
	emitted := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() && emitted < cinsPerRound {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		ip := parseIPText(fields[0])
		if ip == nil {
			continue
		}
		score := -1
		if len(fields) > 1 {
			score, _ = strconv.Atoi(fields[1])
		}
		sev := cinsBaseSeverity
		switch {
		case score >= 80:
			sev = SeverityHigh
		case score >= 50 && score >= 0:
			sev = SeverityMedium
		}
		raw := fields[0]
		if score > 0 {
			raw += " [评分 " + strconv.Itoa(score) + "]"
		}
		ev, ok := buildEvent(f.resolver, f.Name(), ip, sev, raw, ts)
		if !ok {
			continue
		}
		if !sendEvent(ctx, out, ev) {
			return nil
		}
		emitted++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("解析 CINS 清单失败: %w", err)
	}
	if emitted > 0 {
		logger.Info("CINS: 本轮产出 %d 条事件", emitted)
	}
	return nil
}
