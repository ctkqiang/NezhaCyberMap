package geoip

/*
本文件测试静态回退表与 Resolver 的查询逻辑。真实 MaxMind 数据库解析
的测试见 mmdb_test.go（用合成二进制验证格式解析）。
*/

import (
	"net"
	"testing"
)

// TestLookupStaticKnown 验证静态表对已知网段的定位。
func TestLookupStaticKnown(t *testing.T) {
	cases := []struct {
		ip   string
		code string
	}{
		{"8.8.8.8", "US"},       // 8.0.0.0/8
		{"14.215.177.38", "CN"}, // 14.0.0.0/8
		{"112.85.42.1", "CN"},   // 112.0.0.0/8
		{"128.199.1.1", "SG"},   // DigitalOcean 新加坡
		{"45.55.1.2", "US"},     // DigitalOcean 美国
		{"185.220.101.5", "RU"}, // 185.0.0.0/8
		{"91.240.1.1", "RU"},    // 91.0.0.0/8
		{"177.92.1.1", "BR"},    // 177.0.0.0/8
		{"5.9.1.1", "DE"},       // Hetzner
		{"43.132.1.1", "HK"},    // 腾讯云香港
		{"47.74.1.1", "JP"},     // 阿里云东京
		{"51.68.1.1", "FR"},     // OVH 法国
		{"1.1.1.1", "AU"},       // 1.0.0.0/8
	}
	for _, c := range cases {
		res, ok := LookupStatic(net.ParseIP(c.ip))
		if !ok {
			t.Errorf("LookupStatic(%s): 期望命中，实际未命中", c.ip)
			continue
		}
		if res.CountryCode != c.code {
			t.Errorf("LookupStatic(%s): 国家代码 = %s，期望 %s", c.ip, res.CountryCode, c.code)
		}
		if res.CountryName == "" || res.Lat == 0 && res.Lon == 0 {
			t.Errorf("LookupStatic(%s): 元信息缺失: %+v", c.ip, res)
		}
	}
}

// TestLookupStaticUnknown 验证静态表对未知 / 私有 / IPv6 地址返回 false。
func TestLookupStaticUnknown(t *testing.T) {
	cases := []string{
		"192.168.1.1", // 私有地址
		"10.0.0.1",    // 私有地址
		"127.0.0.1",   // 回环
		"25.1.1.1",    // 未收录网段
		"2001:db8::1", // IPv6（静态表不支持）
	}
	for _, ip := range cases {
		if _, ok := LookupStatic(net.ParseIP(ip)); ok {
			t.Errorf("LookupStatic(%s): 期望未命中，实际命中", ip)
		}
	}
}

// TestResolverNilIP 验证 Resolver 对 nil IP 的安全处理。
func TestResolverNilIP(t *testing.T) {
	r := NewResolver()
	if _, ok := r.Lookup(nil); ok {
		t.Error("Lookup(nil): 期望未命中，实际命中")
	}
}

// TestKnownCountry 验证国家元信息查询。
func TestKnownCountry(t *testing.T) {
	name, lat, lon, ok := KnownCountry("CN")
	if !ok || name != "中国" || lat == 0 || lon == 0 {
		t.Errorf("KnownCountry(CN) = %q %v %v %v，期望 中国/有效坐标/true", name, lat, lon, ok)
	}
	if _, _, _, ok := KnownCountry("ZZ"); ok {
		t.Error("KnownCountry(ZZ): 期望 false，实际 true")
	}
}
