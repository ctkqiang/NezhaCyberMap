package feed

/*
本文件测试各真实 feed 的 HTTP 解析逻辑：使用 httptest.Server 提供静态响应，
禁止访问真实外部 API（agent.md 第 10 节）。
*/

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ctkqiang/NezhaCyberMap/pkg/geoip"
)

// collectRun 执行一轮 Run 并收集全部事件。
func collectRun(t *testing.T, f Feed) []AttackEvent {
	t.Helper()
	ch := make(chan AttackEvent, 64)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.Run(ctx, ch); err != nil {
		t.Fatalf("%s.Run 失败: %v", f.Name(), err)
	}
	close(ch)
	var evs []AttackEvent
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

// TestThreatFoxParse 验证 ThreatFox JSON 解析与严重度映射。
func TestThreatFoxParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("方法 = %s，期望 POST", r.Method)
		}
		if ua := r.Header.Get("User-Agent"); ua == "" {
			t.Error("缺少 User-Agent 请求头")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"query_status": "ok",
			"data": [
				{"ioc": "45.55.1.2:443", "ioc_type": "ip:port", "threat_type": "botnet_cc", "confidence_level": 95, "malware_printable": "Cobalt Strike"},
				{"ioc": "185.220.101.5:8080", "ioc_type": "ip:port", "threat_type": "botnet_cc", "confidence_level": 60, "malware_printable": ""},
				{"ioc": "http://evil.example/x.exe", "ioc_type": "url", "threat_type": "payload_delivery", "confidence_level": 80, "malware_printable": ""},
				{"ioc": "192.168.1.1:80", "ioc_type": "ip:port", "threat_type": "scan", "confidence_level": 50, "malware_printable": ""}
			]
		}`))
	}))
	defer srv.Close()

	f := &ThreatFox{resolver: geoip.NewResolver(), baseURL: srv.URL}
	evs := collectRun(t, f)

	// URL 类型与私有 IP 应被过滤，仅剩 2 条 ip:port 事件。
	if len(evs) != 2 {
		t.Fatalf("事件数 = %d，期望 2（URL 与私有 IP 应被过滤）", len(evs))
	}
	if evs[0].Severity != SeverityCritical {
		t.Errorf("置信度 95 → 严重度 = %v，期望 Critical", evs[0].Severity)
	}
	if evs[0].SourceCode != "US" {
		t.Errorf("45.55.1.2 国家 = %s，期望 US", evs[0].SourceCode)
	}
	if evs[1].Severity != SeverityMedium {
		t.Errorf("置信度 60 → 严重度 = %v，期望 Medium", evs[1].Severity)
	}
	if evs[1].SourceCode != "RU" {
		t.Errorf("185.220.101.5 国家 = %s，期望 RU", evs[1].SourceCode)
	}
}

// TestThreatFoxHTTPError 验证 HTTP 错误路径。
func TestThreatFoxHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f := &ThreatFox{resolver: geoip.NewResolver(), baseURL: srv.URL}
	ch := make(chan AttackEvent, 8)
	if err := f.Run(context.Background(), ch); err == nil {
		t.Error("HTTP 500: 期望返回错误")
	}
}

// TestAuthErrorClassified 验证 401/403 被分类为 ErrAuthRequired。
func TestAuthErrorClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	f := &ThreatFox{resolver: geoip.NewResolver(), baseURL: srv.URL}
	err := f.Run(context.Background(), make(chan AttackEvent, 8))
	if err == nil {
		t.Fatal("HTTP 401: 期望返回错误")
	}
	if !errors.Is(err, ErrAuthRequired) {
		t.Errorf("401 错误未分类为 ErrAuthRequired: %v", err)
	}
}

// TestFeodoParse 验证 Feodo 纯文本清单解析。
func TestFeodoParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("# 推荐 C2 封锁清单\n185.220.101.1\n# 注释行\n91.240.1.2\n10.0.0.1\n"))
	}))
	defer srv.Close()

	f := &FeodoTracker{resolver: geoip.NewResolver(), baseURL: srv.URL}
	evs := collectRun(t, f)
	// 私有地址 10.0.0.1 被过滤，注释被跳过。
	if len(evs) != 2 {
		t.Fatalf("事件数 = %d，期望 2", len(evs))
	}
	for _, ev := range evs {
		if ev.Severity != SeverityHigh {
			t.Errorf("Feodo 事件严重度 = %v，期望 High", ev.Severity)
		}
	}
	if evs[0].SourceCode != "RU" || evs[1].SourceCode != "RU" {
		t.Errorf("Feodo 国家 = %s, %s，期望 RU, RU", evs[0].SourceCode, evs[1].SourceCode)
	}
}

// TestURLhausParse 验证 URLhaus JSON 解析（IP 型 host，无需 DNS）。
func TestURLhausParse(t *testing.T) {
	var gotAuthKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthKey = r.Header.Get("Auth-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"payloads": [
				{"host": "http://185.220.101.5/x.exe", "url_status": "online", "tags": ["exe", "emotet"], "threat": "malware_download"},
				{"host": "http://45.33.1.2/doc.html", "url_status": "online", "tags": ["html"], "threat": ""},
				{"host": "http://192.168.0.9/a.bin", "url_status": "online", "tags": ["exe"], "threat": ""}
			]
		}`))
	}))
	defer srv.Close()

	f := &URLhaus{resolver: geoip.NewResolver(), baseURL: srv.URL, apiKey: "test-key-123"}
	evs := collectRun(t, f)
	// 私有 IP 被过滤。
	if len(evs) != 2 {
		t.Fatalf("事件数 = %d，期望 2", len(evs))
	}
	if evs[0].Severity != SeverityHigh {
		t.Errorf("exe 标签事件严重度 = %v，期望 High", evs[0].Severity)
	}
	if evs[1].Severity != SeverityMedium {
		t.Errorf("无恶意标签事件严重度 = %v，期望 Medium", evs[1].Severity)
	}
	// Auth-Key 请求头必须携带。
	if gotAuthKey != "test-key-123" {
		t.Errorf("Auth-Key 头 = %q，期望 test-key-123", gotAuthKey)
	}
}

// TestBlocklistDEParse 验证 blocklist.de 攻击日志解析（纯文本 IP 列表）。
func TestBlocklistDEParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "time=") {
			t.Errorf("缺少 time 参数: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte("185.220.101.5\n45.33.1.2\n10.0.0.5\n"))
	}))
	defer srv.Close()

	f := &BlocklistDE{resolver: geoip.NewResolver(), baseURL: srv.URL}
	evs := collectRun(t, f)
	if len(evs) != 2 { // 私有地址被过滤
		t.Fatalf("事件数 = %d，期望 2", len(evs))
	}
	for _, ev := range evs {
		if ev.Severity != SeverityMedium {
			t.Errorf("blocklist.de 事件严重度 = %v，期望 Medium", ev.Severity)
		}
	}
	if evs[0].SourceCode != "RU" || evs[1].SourceCode != "US" {
		t.Errorf("国家 = %s, %s，期望 RU, US", evs[0].SourceCode, evs[1].SourceCode)
	}
}

// TestCINSParse 验证 CINS 评分清单解析与严重度映射。
func TestCINSParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("# CINS Army 清单\n185.220.101.5 95\n45.33.1.2 60\n91.240.1.2 30\n"))
	}))
	defer srv.Close()

	f := &CINS{resolver: geoip.NewResolver(), baseURL: srv.URL}
	evs := collectRun(t, f)
	if len(evs) != 3 {
		t.Fatalf("事件数 = %d，期望 3", len(evs))
	}
	if evs[0].Severity != SeverityHigh { // 95 → 高
		t.Errorf("评分 95 严重度 = %v，期望 High", evs[0].Severity)
	}
	if evs[1].Severity != SeverityMedium { // 60 → 中
		t.Errorf("评分 60 严重度 = %v，期望 Medium", evs[1].Severity)
	}
	if evs[2].Severity != SeverityMedium { // 30 → 中（低于阈值时按威胁清单基准）
		t.Errorf("评分 30 严重度 = %v，期望 Medium（清单基准）", evs[2].Severity)
	}
}

// TestCINSPlainList 验证纯 IP 格式（无评分列）按基准严重度处理。
func TestCINSPlainList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("185.220.101.5\n45.33.1.2\n"))
	}))
	defer srv.Close()

	f := &CINS{resolver: geoip.NewResolver(), baseURL: srv.URL}
	evs := collectRun(t, f)
	if len(evs) != 2 {
		t.Fatalf("事件数 = %d，期望 2", len(evs))
	}
	for _, ev := range evs {
		if ev.Severity != SeverityMedium {
			t.Errorf("纯 IP 清单事件严重度 = %v，期望 Medium", ev.Severity)
		}
	}
}

// TestEmergingThreatsParse 验证 ET 清单解析（全部高严重度）。
func TestEmergingThreatsParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("# Emerging Threats\n185.220.101.5\n# 注释\n45.33.1.2\n"))
	}))
	defer srv.Close()

	f := &EmergingThreats{resolver: geoip.NewResolver(), baseURL: srv.URL}
	evs := collectRun(t, f)
	if len(evs) != 2 {
		t.Fatalf("事件数 = %d，期望 2", len(evs))
	}
	for _, ev := range evs {
		if ev.Severity != SeverityHigh {
			t.Errorf("ET 事件严重度 = %v，期望 High", ev.Severity)
		}
	}
}

// TestTORExitsParse 验证 TOR 出口清单解析（中等严重度）。
func TestTORExitsParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("185.220.101.5\n91.240.1.2\n192.168.0.1\n"))
	}))
	defer srv.Close()

	f := &TORExits{resolver: geoip.NewResolver(), baseURL: srv.URL}
	evs := collectRun(t, f)
	if len(evs) != 2 { // 私有地址被过滤
		t.Fatalf("事件数 = %d，期望 2", len(evs))
	}
	for _, ev := range evs {
		if ev.Severity != SeverityMedium {
			t.Errorf("TOR 事件严重度 = %v，期望 Medium", ev.Severity)
		}
	}
}

// TestMockDeterministic 验证模拟数据源的确定性（两次运行序列一致）。
func TestMockDeterministic(t *testing.T) {
	run := func() []AttackEvent {
		m := NewMock()
		ch := make(chan AttackEvent, 16)
		for i := 0; i < 6; i++ {
			if err := m.Run(context.Background(), ch); err != nil {
				t.Fatalf("Mock.Run 失败: %v", err)
			}
		}
		close(ch)
		var evs []AttackEvent
		for ev := range ch {
			ev.Timestamp = time.Time{} // 时间戳不同，排除后比较
			evs = append(evs, ev)
		}
		return evs
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("两次运行事件数不同: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].RawIndicator != b[i].RawIndicator ||
			a[i].SourceCode != b[i].SourceCode ||
			a[i].Severity != b[i].Severity {
			t.Fatalf("事件 %d 两次运行不一致: %+v vs %+v", i, a[i], b[i])
		}
	}
}
