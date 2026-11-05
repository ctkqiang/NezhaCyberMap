package feed

/*
本文件实现 ThreatFox 数据源（abuse.ch）：POST 查询近 N 天 IOC，
筛选 ip:port 类指标，按 confidence_level 映射严重度。

API 文档：https://threatfox.abuse.ch/api/
轮询建议：60 秒（该 API 有查询频率限制，勿低于 30 秒）。
*/

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/pkg/geoip"
	"github.com/ctkqiang/NezhaCyberMap/pkg/logger"
)

// threatFoxAPI 是 ThreatFox 的 API 根地址（测试中可覆盖）。
const threatFoxAPI = "https://threatfox-api.abuse.ch/api/v1/"

/*
ThreatFox 是 threatfox-api.abuse.ch 的客户端。

字段:
  - resolver: 地理定位解析器。
  - baseURL: API 根地址（默认 threatFoxAPI，测试时注入 httptest 地址）。
  - apiKey: abuse.ch API 密钥（可选；缺省时 API 返回 401，连续失败后自动降级）。
*/
type ThreatFox struct {
	resolver *geoip.Resolver
	baseURL  string
	apiKey   string
}

// threatFoxRequest 是查询请求体。
type threatFoxRequest struct {
	Query string `json:"query"`
	Days  int    `json:"days"`
}

// threatFoxResponse 是 API 响应结构（仅声明需要的字段）。
type threatFoxResponse struct {
	QueryStatus string          `json:"query_status"`
	Data        []threatFoxItem `json:"data"`
}

// threatFoxItem 是单条 IOC 记录。
type threatFoxItem struct {
	IOC              string `json:"ioc"`
	IOCType          string `json:"ioc_type"`
	ThreatType       string `json:"threat_type"`
	ConfidenceLevel  int    `json:"confidence_level"`
	MalwarePrintable string `json:"malware_printable"`
}

/*
NewThreatFox 创建 ThreatFox 数据源。

参数:
  - resolver (*geoip.Resolver): 地理定位解析器。
  - apiKey (string): abuse.ch API 密钥（可为空）。

返回:
  - *ThreatFox: 就绪的数据源实例。
*/
func NewThreatFox(resolver *geoip.Resolver, apiKey string) *ThreatFox {
	return &ThreatFox{resolver: resolver, baseURL: threatFoxAPI, apiKey: apiKey}
}

/*
authHeaders 返回请求所需的认证头（配置了 API 密钥时携带 Auth-Key）。

返回:
  - map[string]string: 认证头表，未配置密钥时返回 nil。
*/
func (f *ThreatFox) authHeaders() map[string]string {
	if f.apiKey == "" {
		return nil
	}
	return map[string]string{"Auth-Key": f.apiKey}
}

/*
Name 返回数据源显示名。

返回:
  - string: "ThreatFox"。
*/
func (f *ThreatFox) Name() string { return "ThreatFox" }

/*
Run 执行一轮抓取：查询近 1 天 IOC，解析 ip:port 指标并产出事件。

参数:
  - ctx (context.Context): 取消信号。
  - out (chan<- AttackEvent): 事件输出通道。

返回:
  - error: 本轮网络或解析错误。
*/
func (f *ThreatFox) Run(ctx context.Context, out chan<- AttackEvent) error {
	reqBody, err := marshal(threatFoxRequest{Query: "get_iocs", Days: 1})
	if err != nil {
		return fmt.Errorf("构造查询失败: %w", err)
	}
	data, err := fetch(ctx, http.MethodPost, f.baseURL, reqBody, f.authHeaders())
	if err != nil {
		return fmt.Errorf("请求 ThreatFox API 失败: %w", classify(err))
	}
	var resp threatFoxResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("解析 ThreatFox 响应失败: %w", err)
	}
	if resp.QueryStatus != "ok" {
		return fmt.Errorf("ThreatFox 查询状态异常: %s", resp.QueryStatus)
	}
	ts := time.Now()
	emitted := 0
	for i := range resp.Data {
		item := &resp.Data[i]
		ev, ok := f.convert(item, ts)
		if !ok {
			continue
		}
		if !sendEvent(ctx, out, ev) {
			return nil
		}
		emitted++
	}
	if emitted > 0 {
		logger.Info("ThreatFox: 本轮产出 %d 条事件", emitted)
	}
	return nil
}

/*
convert 将单条 IOC 转换为 AttackEvent。

参数:
  - item (*threatFoxItem): API 返回的单条 IOC 记录。
  - ts (time.Time): 事件时间戳。

返回:
  - AttackEvent: 转换结果。
  - ok (bool): 指标类型与 IP 合法时为 true。
*/
func (f *ThreatFox) convert(item *threatFoxItem, ts time.Time) (AttackEvent, bool) {
	if item.IOCType != "ip:port" && item.IOCType != "ip" {
		return AttackEvent{}, false
	}
	host := item.IOC
	if i := strings.LastIndex(host, ":"); i > 0 && item.IOCType == "ip:port" {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return AttackEvent{}, false
	}
	// 严重度映射：置信度 >=90 严重，>=75 高，>=50 中，其余低。
	sev := SeverityLow
	switch {
	case item.ConfidenceLevel >= 90:
		sev = SeverityCritical
	case item.ConfidenceLevel >= 75:
		sev = SeverityHigh
	case item.ConfidenceLevel >= 50:
		sev = SeverityMedium
	}
	raw := item.IOC
	if item.MalwarePrintable != "" {
		raw += " [" + item.MalwarePrintable + "]"
	}
	return buildEvent(f.resolver, f.Name(), ip, sev, raw, ts)
}
