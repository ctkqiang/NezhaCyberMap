package feed

/*
本文件实现 blocklist.de 数据源：真实蜜罐攻击日志（免费，无需密钥）。

API: https://api.blocklist.de/getlast.php?time=<unixtime> 返回该时刻之后
被其蜜罐网络观测到的攻击源 IP（纯文本，每行一个 IP）。
这是「谁在攻击谁」的第一手数据——攻击源 → 蜜罐节点。

轮询建议：60 秒（实时数据）。
*/

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/pkg/geoip"
	"github.com/ctkqiang/NezhaCyberMap/pkg/logger"
)

// blocklistAPI 是 blocklist.de 攻击日志接口（测试中可覆盖）。
// 注意：time 参数为 unix 时间戳（返回该时刻之后的攻击报告），非分钟数。
const blocklistAPI = "https://api.blocklist.de/getlast.php"

// 攻击日志回看窗口（秒）。
const blocklistLookback = 90

// 每轮最大产出事件数（攻击日志量可能很大，防止洪泛）。
const blocklistPerRound = 100

// BlocklistDE 是 api.blocklist.de 的客户端。
type BlocklistDE struct {
	resolver *geoip.Resolver
	baseURL  string
}

/*
NewBlocklistDE 创建 blocklist.de 数据源。

参数:
  - resolver (*geoip.Resolver): 地理定位解析器。

返回:
  - *BlocklistDE: 就绪的数据源实例。
*/
func NewBlocklistDE(resolver *geoip.Resolver) *BlocklistDE {
	return &BlocklistDE{resolver: resolver, baseURL: blocklistAPI}
}

/*
Name 返回数据源显示名。

返回:
  - string: "Blocklist.de"。
*/
func (f *BlocklistDE) Name() string { return "Blocklist.de" }

/*
Run 执行一轮抓取：拉取最近攻击日志（纯文本，每行一个攻击源 IP）。

参数:
  - ctx (context.Context): 取消信号。
  - out (chan<- AttackEvent): 事件输出通道。

返回:
  - error: 本轮网络或解析错误。
*/
func (f *BlocklistDE) Run(ctx context.Context, out chan<- AttackEvent) error {
	// time 参数 = unix 时间戳：取最近 90 秒的攻击报告。
	url := f.baseURL + "?time=" + strconv.FormatInt(time.Now().Add(-blocklistLookback*time.Second).Unix(), 10)
	data, err := fetch(ctx, http.MethodGet, url, nil, nil)
	if err != nil {
		return fmt.Errorf("请求 blocklist.de 失败: %w", err)
	}
	// 响应为纯文本：每行一个攻击源 IP（蜜罐网络实时上报的暴力破解来源）。
	ts := time.Now()
	emitted := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() && emitted < blocklistPerRound {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		ip := parseIPText(line)
		if ip == nil {
			continue
		}
		// 蜜罐实时攻击上报（暴力破解尝试），统一标记「中」。
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
		return fmt.Errorf("解析 blocklist.de 响应失败: %w", err)
	}
	if emitted > 0 {
		logger.Info("Blocklist.de: 本轮产出 %d 条事件", emitted)
	}
	return nil
}
