package feed

/*
本文件实现 URLhaus 数据源（abuse.ch）：拉取近期恶意软件投放载荷列表，
从 host 字段提取 IP（域名则先经 2 秒超时的 DNS 解析），按标签映射严重度。

API 文档：https://urlhaus-api.abuse.ch/
轮询建议：300 秒（载荷列表更新频率不高）。
*/

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/pkg/geoip"
	"github.com/ctkqiang/NezhaCyberMap/pkg/logger"
)

// urlhausAPI 是 URLhaus 载荷接口地址（测试中可覆盖）。
const urlhausAPI = "https://urlhaus-api.abuse.ch/v1/payloads/recent/"

/*
URLhaus 是 urlhaus-api.abuse.ch 的客户端。

字段:
  - resolver: 地理定位解析器。
  - baseURL: 接口地址（默认 urlhausAPI，测试时注入 httptest 地址）。
  - apiKey: abuse.ch API 密钥（必填：abuse.ch 全系 API 均需 Auth-Key 认证，
    可在 https://abuse.ch/#api-key 免费申请，ThreatFox / URLhaus 共用）。
*/
type URLhaus struct {
	resolver *geoip.Resolver
	baseURL  string
	apiKey   string
}

// urlhausResponse 是 API 响应结构（仅声明需要的字段）。
type urlhausResponse struct {
	Payloads []urlhausPayload `json:"payloads"`
}

// urlhausPayload 是单条恶意载荷记录。
type urlhausPayload struct {
	Host      string   `json:"host"`
	URLStatus string   `json:"url_status"`
	Tags      []string `json:"tags"`
	Threat    string   `json:"threat"`
}

/*
NewURLhaus 创建 URLhaus 数据源。

参数:
  - resolver (*geoip.Resolver): 地理定位解析器。
  - apiKey (string): abuse.ch API 密钥。

返回:
  - *URLhaus: 就绪的数据源实例。
*/
func NewURLhaus(resolver *geoip.Resolver, apiKey string) *URLhaus {
	return &URLhaus{resolver: resolver, baseURL: urlhausAPI, apiKey: apiKey}
}

/*
Name 返回数据源显示名。

返回:
  - string: "URLhaus"。
*/
func (f *URLhaus) Name() string { return "URLhaus" }

/*
Run 执行一轮抓取：拉取近期载荷，解析 host 字段并产出事件。

参数:
  - ctx (context.Context): 取消信号。
  - out (chan<- AttackEvent): 事件输出通道。

返回:
  - error: 本轮网络或解析错误。
*/
func (f *URLhaus) Run(ctx context.Context, out chan<- AttackEvent) error {
	headers := map[string]string(nil)
	if f.apiKey != "" {
		headers = map[string]string{"Auth-Key": f.apiKey}
	}
	data, err := fetch(ctx, http.MethodGet, f.baseURL, nil, headers)
	if err != nil {
		return fmt.Errorf("请求 URLhaus API 失败: %w", classify(err))
	}
	var resp urlhausResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("解析 URLhaus 响应失败: %w", err)
	}
	ts := time.Now()
	emitted := 0
	for i := range resp.Payloads {
		payload := &resp.Payloads[i]
		ev, ok := f.convert(ctx, payload, ts)
		if !ok {
			continue
		}
		if !sendEvent(ctx, out, ev) {
			return nil
		}
		emitted++
	}
	if emitted > 0 {
		logger.Info("URLhaus: 本轮产出 %d 条事件", emitted)
	}
	return nil
}

/*
convert 将单条载荷记录转换为 AttackEvent。

参数:
  - ctx (context.Context): 取消信号（用于 DNS 解析超时）。
  - payload (*urlhausPayload): API 返回的单条载荷记录。
  - ts (time.Time): 事件时间戳。

返回:
  - AttackEvent: 转换结果。
  - ok (bool): 提取到可定位的 IP 时为 true。
*/
func (f *URLhaus) convert(ctx context.Context, payload *urlhausPayload, ts time.Time) (AttackEvent, bool) {
	host := payload.Host
	if parsed, err := url.Parse(payload.Host); err == nil && parsed.Host != "" {
		host = parsed.Hostname()
	}
	ip := resolveHost(ctx, host)
	if ip == nil {
		return AttackEvent{}, false
	}
	// 严重度映射：标签含可执行文件 / C2 特征 → 高，其余为中。
	sev := SeverityMedium
	for _, tag := range payload.Tags {
		switch strings.ToLower(tag) {
		case "exe", "dll", "elf", "botnet_cc", "emotet", "cobaltstrike", "agenttesla":
			sev = SeverityHigh
		}
	}
	raw := payload.Host
	if payload.Threat != "" {
		raw += " [" + payload.Threat + "]"
	}
	return buildEvent(f.resolver, f.Name(), ip, sev, raw, ts)
}
