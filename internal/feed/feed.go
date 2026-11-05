/*
Package feed 定义威胁情报数据源接口与统一事件模型。

数据源分为两类：
 1. 真实 feed：ThreatFox、Feodo Tracker、URLhaus（均为 abuse.ch 公开 API）。
 2. 模拟 feed：确定性伪随机生成器，供离线演示与降级兜底。

所有 HTTP 请求必须通过本包的 httpClient 执行（5 秒超时、统一 User-Agent、
context 级联），禁止使用无超时的 http.Get。
*/
package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/pkg/geoip"
	"github.com/ctkqiang/NezhaCyberMap/pkg/logger"
	"github.com/ctkqiang/NezhaCyberMap/pkg/version"
)

// Severity 表示攻击事件的严重程度。
type Severity uint8

// 严重程度分级：低 / 中 / 高 / 严重。
const (
	SeverityLow Severity = iota
	SeverityMedium
	SeverityHigh
	SeverityCritical
)

// severityNames 是严重度的中文名称表（与 Severity 枚举一一对应）。
var severityNames = [4]string{"低", "中", "高", "严重"}

/*
Name 返回严重程度的中文名。

返回:
  - string: "低" / "中" / "高" / "严重"。
*/
func (s Severity) Name() string {
	if int(s) >= 0 && int(s) < len(severityNames) {
		return severityNames[s]
	}
	return "未知"
}

/*
AttackEvent 是一条已解析的攻击事件（攻击源 → 蜜罐目标）。

字段:
  - Timestamp: 事件时间（真实 feed 为抓取时间）。
  - SourceIP: 攻击源 IP（可为 nil，仅模拟数据保证非 nil）。
  - SourceCode: 攻击源国家代码（如 "RU"）。
  - SourceCountry: 攻击源国家中文名。
  - SourceLat / SourceLon: 攻击源坐标。
  - TargetCode / TargetCountry: 蜜罐节点国家代码与中文名。
  - TargetLat / TargetLon: 蜜罐节点坐标。
  - Severity: 严重程度。
  - FeedName: 来源数据源名（如 "ThreatFox"）。
  - RawIndicator: 原始威胁指标（IOC 文本），供日志视图展示。
*/
type AttackEvent struct {
	Timestamp     time.Time
	SourceIP      net.IP
	SourceCode    string
	SourceCountry string
	SourceLat     float64
	SourceLon     float64
	TargetCode    string
	TargetCountry string
	TargetLat     float64
	TargetLon     float64
	Severity      Severity
	FeedName      string
	RawIndicator  string
}

/*
Feed 是威胁情报数据源接口。Run 执行一轮抓取（单次 HTTP 请求 + 解析），
将解析出的事件写入 out，返回本轮错误。轮询间隔与重试由 ingest 层负责。

参数:
  - ctx: 取消信号（超时、退出时级联取消）。
  - out: 事件输出通道，写入时遵循背压（select + ctx.Done）。
*/
type Feed interface {
	Name() string
	Run(ctx context.Context, out chan<- AttackEvent) error
}

// httpClient 是全包共享的 HTTP 客户端：5 秒总超时（agent.md 硬性约束）。
var httpClient = &http.Client{
	Timeout: 5 * time.Second,
}

// dnsTimeout 是蜜罐域名解析的硬上限（URLhaus 的 host 字段可能为域名）。
const dnsTimeout = 2 * time.Second

/*
fetch 执行一次 HTTP 请求并返回响应体。所有真实 feed 的唯一网络出口。

参数:
  - ctx (context.Context): 取消信号。
  - method (string): HTTP 方法（GET / POST）。
  - url (string): 目标地址。
  - body ([]byte): 请求体（GET 时为 nil）。
  - headers (map[string]string): 附加请求头（可为 nil）。

返回:
  - data ([]byte): 响应体字节。
  - err (error): 网络 / 状态码错误。
*/
func fetch(ctx context.Context, method, url string, body []byte, headers map[string]string) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("User-Agent", version.UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 上限 8 MiB
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode}
	}
	return data, nil
}

/*
classify 将抓取错误分类：认证错误包装为 ErrAuthRequired（可被 errors.Is 识别）。

参数:
  - err (error): 原始错误。

返回:
  - error: 分类后的错误（认证错误被包装，其余原样返回）。
*/
func classify(err error) error {
	if isAuthError(err) {
		return fmt.Errorf("%w: %v", ErrAuthRequired, err)
	}
	return err
}

/*
resolveHost 将主机名解析为 IPv4 地址（带 2 秒硬超时）。

参数:
  - ctx (context.Context): 父级取消信号。
  - host (string): 主机名或 IP 文本。

返回:
  - net.IP: 解析结果，失败返回 nil。
*/
func resolveHost(ctx context.Context, host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	rctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIP(rctx, "ip4", host)
	if err != nil {
		logger.Debug("域名解析失败 %s: %v", host, err)
		return nil
	}
	for _, a := range addrs {
		if v4 := a.To4(); v4 != nil {
			return v4
		}
	}
	return nil
}

// honeypot 描述一个我们「运营」的蜜罐传感器节点。
type honeypot struct {
	code string
	name string
	lat  float64
	lon  float64
}

// honeypots 是内置蜜罐节点清单（目标侧）：分布全球的仿真传感器。
var honeypots = []honeypot{
	{"US", "美国·华盛顿", 39.04, -77.48},
	{"DE", "德国·法兰克福", 50.11, 8.68},
	{"SG", "新加坡", 1.35, 103.82},
	{"JP", "日本·东京", 35.68, 139.69},
	{"AU", "澳大利亚·悉尼", -33.87, 151.21},
	{"CN", "中国·上海", 31.23, 121.47},
	{"BR", "巴西·圣保罗", -23.55, -46.63},
	{"ZA", "南非·约翰内斯堡", -26.20, 28.04},
	{"CA", "加拿大·多伦多", 43.65, -79.38},
	{"GB", "英国·伦敦", 51.51, -0.13},
	{"NL", "荷兰·阿姆斯特丹", 52.37, 4.90},
	{"IN", "印度·孟买", 19.08, 72.88},
}

/*
pickHoneypot 依据威胁指标哈希确定性地选择一个蜜罐节点作为目标。

同一指标永远命中同一节点，保证「事件 → 目标」的对应关系可复现，
也避免在渲染路径中引入随机性。

参数:
  - indicator (string): 威胁指标文本（IP / URL）。

返回:
  - honeypot: 选中的蜜罐节点。
*/
func pickHoneypot(indicator string) honeypot {
	h := fnv.New32a()
	_, _ = h.Write([]byte(indicator))
	return honeypots[int(h.Sum32())%len(honeypots)]
}

/*
buildEvent 由已解析的攻击源 IP 与严重度组装标准 AttackEvent。

参数:
  - res (*geoip.Resolver): 地理定位解析器（DB + 静态表）。
  - name (string): 数据源名称。
  - ip (net.IP): 攻击源 IP。
  - sev (Severity): 严重程度。
  - raw (string): 原始指标文本。
  - ts (time.Time): 事件时间戳。

返回:
  - AttackEvent: 组装完成的事件（目标节点已通过 pickHoneypot 确定）。
  - ok (bool): IP 通过过滤（非私有、可地理定位）时为 true。
*/
func buildEvent(res *geoip.Resolver, name string, ip net.IP, sev Severity, raw string, ts time.Time) (AttackEvent, bool) {
	if ip == nil || ip.To4() == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() {
		return AttackEvent{}, false
	}
	loc, ok := res.Lookup(ip)
	if !ok {
		logger.Debug("IP %s 无法地理定位，跳过", ip.String())
		return AttackEvent{}, false
	}
	target := pickHoneypot(raw)
	return AttackEvent{
		Timestamp:     ts,
		SourceIP:      ip,
		SourceCode:    loc.CountryCode,
		SourceCountry: loc.CountryName,
		SourceLat:     loc.Lat,
		SourceLon:     loc.Lon,
		TargetCode:    target.code,
		TargetCountry: target.name,
		TargetLat:     target.lat,
		TargetLon:     target.lon,
		Severity:      sev,
		FeedName:      name,
		RawIndicator:  raw,
	}, true
}

/*
parseIPText 从自由文本中提取第一个合法 IPv4 地址（用于纯文本类数据源）。

参数:
  - line (string): 待扫描文本行。

返回:
  - net.IP: 提取的 IPv4 地址，未找到返回 nil。
*/
func parseIPText(line string) net.IP {
	fields := strings.Fields(line)
	for _, f := range fields {
		f = strings.Trim(f, `"'()[]{};,\:`)
		if ip := net.ParseIP(f); ip != nil && ip.To4() != nil {
			return ip
		}
	}
	return nil
}

/*
sendEvent 以背压方式将事件写入输出通道：TUI 消费不及时时阻塞等待，
收到 ctx 取消时立即返回。禁止 default 丢弃（模拟数据速率控制除外）。

参数:
  - ctx (context.Context): 取消信号。
  - out (chan<- AttackEvent): 输出通道。
  - ev (AttackEvent): 待发送事件。

返回:
  - bool: 发送成功返回 true，ctx 取消返回 false。
*/
func sendEvent(ctx context.Context, out chan<- AttackEvent, ev AttackEvent) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// StatusError 表示 HTTP 非 2xx 响应（认证错误可被 errors.As 识别）。
type StatusError struct {
	Code int
}

// Error 实现 error 接口。
func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.Code)
}

// marshal 是 encoding/json.Marshal 的包内别名，集中 JSON 处理入口。
func marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// ErrAuthRequired 表示数据源需要 API 密钥（HTTP 401/403）。
// 密钥只能在启动时注入，故摄入层遇到该错误后不再重试。
var ErrAuthRequired = errors.New("需要 abuse.ch API 密钥（--abuse-key）")

/*
isAuthError 判断错误是否为 HTTP 401/403 认证失败。

参数:
  - err (error): 待判断错误。

返回:
  - bool: 是认证错误返回 true。
*/
func isAuthError(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden)
}
