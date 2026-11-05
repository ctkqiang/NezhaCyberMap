/*
Package geoip 提供 IP 地址 → 国家/经纬度 的地理定位能力，零外部依赖。

实现分两层：
 1. mmdb.go 中自实现的 MaxMind DB (GeoLite2-City) 二进制读取器，仅解析
    country.iso_code 与 location.latitude / location.longitude 子集。
 2. 本文件中的静态回退表：当未加载 .mmdb 文件、或 IP 在数据库中查无记录
    （例如 IPv6 尚未支持）时，用内嵌的粗粒度 CIDR → 国家映射兜底。

静态表为演示级精度，重点覆盖常见 VPS / 云厂商 / 蜜罐攻击源网段，仅供
离线演示使用；生产级定位请通过 --geoip-db 加载官方 GeoLite2-City。
*/
package geoip

import (
	"net"
	"sort"
)

/*
Result 表示一次地理定位的结果。

字段:
  - CountryCode: ISO 3166-1 alpha-2 国家代码（如 "CN"）。
  - CountryName: 中文国家名（如 "中国"）。
  - Lat: 国家中心纬度，范围 [-90, 90]。
  - Lon: 国家中心经度，范围 [-180, 180]。
*/
type Result struct {
	CountryCode string
	CountryName string
	Lat         float64
	Lon         float64
}

/*
Resolver 是地理定位的统一入口：优先使用已加载的 MaxMind DB，失败时回退静态表。

字段:
  - db: 已加载的 mmdb 数据库句柄，nil 表示未加载。
*/
type Resolver struct {
	db *mmdb
}

// 静态表条目：CIDR 文本 + 国家信息。
type staticEntry struct {
	cidr    string
	code    string
	name    string
	lat     float64
	lon     float64
	network *net.IPNet // 解析后的网段，init 时填充
}

// 国家元信息表：ISO 代码 → 中文名与中心坐标（用于静态表与数据库缺省值）。
var countryMeta = map[string]struct {
	name string
	lat  float64
	lon  float64
}{
	"US": {"美国", 39.83, -98.58},
	"CA": {"加拿大", 56.13, -106.35},
	"CN": {"中国", 35.86, 104.20},
	"JP": {"日本", 36.20, 138.25},
	"KR": {"韩国", 35.91, 127.77},
	"RU": {"俄罗斯", 61.52, 105.32},
	"DE": {"德国", 51.17, 10.45},
	"NL": {"荷兰", 52.13, 5.29},
	"FR": {"法国", 46.23, 2.21},
	"GB": {"英国", 55.38, -3.44},
	"FI": {"芬兰", 61.92, 25.75},
	"SG": {"新加坡", 1.35, 103.82},
	"AU": {"澳大利亚", -25.27, 133.78},
	"IN": {"印度", 20.59, 78.96},
	"BR": {"巴西", -14.24, -51.93},
	"CL": {"智利", -35.68, -71.54},
	"TR": {"土耳其", 38.96, 35.24},
	"UA": {"乌克兰", 48.38, 31.17},
	"SE": {"瑞典", 60.13, 18.64},
	"IT": {"意大利", 41.87, 12.57},
	"ES": {"西班牙", 40.46, -3.75},
	"PL": {"波兰", 51.92, 19.15},
	"ID": {"印度尼西亚", -0.79, 113.92},
	"VN": {"越南", 14.06, 108.28},
	"TH": {"泰国", 15.87, 100.99},
	"MY": {"马来西亚", 4.21, 101.98},
	"IR": {"伊朗", 32.43, 53.69},
	"KP": {"朝鲜", 40.34, 127.51},
	"IL": {"以色列", 31.05, 34.85},
	"ZA": {"南非", -30.56, 22.94},
	"MX": {"墨西哥", 23.63, -102.55},
	"AR": {"阿根廷", -38.42, -63.62},
	"HK": {"中国香港", 22.32, 114.17},
	"TW": {"中国台湾", 23.70, 120.96},
	"CH": {"瑞士", 46.82, 8.23},
	"AT": {"奥地利", 47.52, 14.55},
	"RO": {"罗马尼亚", 45.94, 24.97},
	"BG": {"保加利亚", 42.73, 25.49},
	"CZ": {"捷克", 49.82, 15.47},
	"IE": {"爱尔兰", 53.41, -8.24},
	"NO": {"挪威", 60.47, 8.47},
	"DK": {"丹麦", 56.26, 9.50},
	"BE": {"比利时", 50.50, 4.47},
	"LU": {"卢森堡", 49.82, 6.13},
	"PT": {"葡萄牙", 39.40, -8.22},
	"GR": {"希腊", 39.07, 21.82},
	"HU": {"匈牙利", 47.16, 19.50},
	"LT": {"立陶宛", 55.17, 23.88},
	"LV": {"拉脱维亚", 56.88, 24.60},
	"EE": {"爱沙尼亚", 58.60, 25.01},
	"NZ": {"新西兰", -40.90, 174.89},
	"PH": {"菲律宾", 12.88, 121.77},
	"PK": {"巴基斯坦", 30.38, 69.35},
	"BD": {"孟加拉国", 23.69, 90.36},
	"NG": {"尼日利亚", 9.08, 8.68},
	"EG": {"埃及", 26.82, 30.80},
	"KE": {"肯尼亚", -0.02, 37.91},
	"KZ": {"哈萨克斯坦", 48.02, 66.92},
	"SA": {"沙特阿拉伯", 23.89, 45.08},
	"AE": {"阿联酋", 23.42, 53.85},
	"AF": {"阿富汗", 33.94, 67.71},
	"IQ": {"伊拉克", 33.22, 43.68},
	"MM": {"缅甸", 21.91, 95.96},
	"KH": {"柬埔寨", 12.57, 104.99},
	"LA": {"老挝", 19.86, 102.50},
	"MN": {"蒙古", 46.86, 103.85},
	"NP": {"尼泊尔", 28.39, 84.12},
	"LK": {"斯里兰卡", 7.87, 80.77},
	"GH": {"加纳", 7.95, -1.02},
	"VE": {"委内瑞拉", 6.42, -66.59},
	"CO": {"哥伦比亚", 4.57, -74.30},
	"PE": {"秘鲁", -9.19, -75.02},
	"EC": {"厄瓜多尔", -1.83, -78.18},
	"PY": {"巴拉圭", -23.44, -58.44},
	"UY": {"乌拉圭", -32.52, -55.77},
	"BO": {"玻利维亚", -16.29, -63.59},
	"DO": {"多米尼加", 18.74, -70.16},
	"PA": {"巴拿马", 8.54, -80.78},
	"CR": {"哥斯达黎加", 9.75, -83.75},
	"GT": {"危地马拉", 15.78, -90.23},
	"CU": {"古巴", 21.52, -77.78},
	"PR": {"波多黎各", 18.22, -66.59},
	"IS": {"冰岛", 64.96, -19.02},
	"MD": {"摩尔多瓦", 47.41, 28.37},
	"BY": {"白俄罗斯", 53.71, 27.95},
	"GE": {"格鲁吉亚", 42.32, 43.36},
	"AM": {"亚美尼亚", 40.07, 45.04},
	"AZ": {"阿塞拜疆", 40.14, 47.58},
	"CY": {"塞浦路斯", 35.13, 33.43},
	"RS": {"塞尔维亚", 44.02, 21.01},
	"HR": {"克罗地亚", 45.10, 15.20},
	"SK": {"斯洛伐克", 48.67, 19.70},
	"SI": {"斯洛文尼亚", 46.15, 15.00},
	"AL": {"阿尔巴尼亚", 41.15, 20.17},
	"MK": {"北马其顿", 41.61, 21.75},
	"BA": {"波黑", 43.92, 17.68},
	"ME": {"黑山", 42.71, 19.37},
	"MT": {"马耳他", 35.94, 14.38},
	"LB": {"黎巴嫩", 33.85, 35.86},
	"JO": {"约旦", 30.59, 36.24},
	"SY": {"叙利亚", 34.80, 38.99},
	"YE": {"也门", 15.55, 48.52},
	"OM": {"阿曼", 21.51, 55.92},
	"QA": {"卡塔尔", 25.35, 51.18},
	"KW": {"科威特", 29.31, 47.48},
	"BH": {"巴林", 26.07, 50.55},
}

/*
staticTable 是演示级 CIDR 回退表：粗粒度 /8 国家级网段 + 常见云厂商 /16 网段。

说明：部分 /8 网段在实际分配中由多国共享，此处按主要归属国标注；
该表仅供离线演示，真实数据请使用 GeoLite2-City。
*/
var staticTable = []staticEntry{
	// ── 国家级粗粒度网段（按主要归属国标注）──
	{cidr: "1.0.0.0/8", code: "AU"},
	{cidr: "2.0.0.0/8", code: "RU"},
	{cidr: "3.0.0.0/8", code: "US"},
	{cidr: "4.0.0.0/8", code: "US"},
	{cidr: "7.0.0.0/8", code: "US"},
	{cidr: "8.0.0.0/8", code: "US"},
	{cidr: "9.0.0.0/8", code: "US"},
	{cidr: "11.0.0.0/8", code: "US"},
	{cidr: "12.0.0.0/8", code: "US"},
	{cidr: "13.0.0.0/8", code: "US"},
	{cidr: "14.0.0.0/8", code: "CN"},
	{cidr: "15.0.0.0/8", code: "US"},
	{cidr: "16.0.0.0/8", code: "US"},
	{cidr: "17.0.0.0/8", code: "US"},
	{cidr: "18.0.0.0/8", code: "US"},
	{cidr: "20.0.0.0/8", code: "US"},
	{cidr: "23.0.0.0/8", code: "US"},
	{cidr: "24.0.0.0/8", code: "US"},
	{cidr: "26.0.0.0/8", code: "US"},
	{cidr: "27.0.0.0/8", code: "CN"},
	{cidr: "32.0.0.0/8", code: "US"},
	{cidr: "34.0.0.0/8", code: "US"},
	{cidr: "35.0.0.0/8", code: "US"},
	{cidr: "36.0.0.0/8", code: "CN"},
	{cidr: "37.0.0.0/8", code: "RU"},
	{cidr: "38.0.0.0/8", code: "US"},
	{cidr: "39.0.0.0/8", code: "CN"},
	{cidr: "40.0.0.0/8", code: "US"},
	{cidr: "42.0.0.0/8", code: "CN"},
	{cidr: "43.0.0.0/8", code: "JP"},
	{cidr: "44.0.0.0/8", code: "US"},
	{cidr: "46.0.0.0/8", code: "RU"},
	{cidr: "47.0.0.0/8", code: "US"},
	{cidr: "49.0.0.0/8", code: "CN"},
	{cidr: "50.0.0.0/8", code: "US"},
	{cidr: "51.0.0.0/8", code: "GB"},
	{cidr: "52.0.0.0/8", code: "US"},
	{cidr: "54.0.0.0/8", code: "US"},
	{cidr: "55.0.0.0/8", code: "US"},
	{cidr: "56.0.0.0/8", code: "US"},
	{cidr: "57.0.0.0/8", code: "FR"},
	{cidr: "58.0.0.0/8", code: "CN"},
	{cidr: "59.0.0.0/8", code: "CN"},
	{cidr: "60.0.0.0/8", code: "CN"},
	{cidr: "61.0.0.0/8", code: "CN"},
	{cidr: "62.0.0.0/8", code: "DE"},
	{cidr: "63.0.0.0/8", code: "US"},
	{cidr: "64.0.0.0/8", code: "US"},
	{cidr: "65.0.0.0/8", code: "US"},
	{cidr: "66.0.0.0/8", code: "US"},
	{cidr: "67.0.0.0/8", code: "US"},
	{cidr: "68.0.0.0/8", code: "US"},
	{cidr: "69.0.0.0/8", code: "US"},
	{cidr: "70.0.0.0/8", code: "US"},
	{cidr: "71.0.0.0/8", code: "US"},
	{cidr: "72.0.0.0/8", code: "US"},
	{cidr: "73.0.0.0/8", code: "US"},
	{cidr: "74.0.0.0/8", code: "US"},
	{cidr: "75.0.0.0/8", code: "US"},
	{cidr: "76.0.0.0/8", code: "US"},
	{cidr: "77.0.0.0/8", code: "RU"},
	{cidr: "78.0.0.0/8", code: "RU"},
	{cidr: "79.0.0.0/8", code: "RU"},
	{cidr: "80.0.0.0/8", code: "RU"},
	{cidr: "81.0.0.0/8", code: "GB"},
	{cidr: "82.0.0.0/8", code: "GB"},
	{cidr: "83.0.0.0/8", code: "NL"},
	{cidr: "84.0.0.0/8", code: "SE"},
	{cidr: "85.0.0.0/8", code: "DE"},
	{cidr: "86.0.0.0/8", code: "GB"},
	{cidr: "87.0.0.0/8", code: "GB"},
	{cidr: "88.0.0.0/8", code: "TR"},
	{cidr: "89.0.0.0/8", code: "RU"},
	{cidr: "90.0.0.0/8", code: "TR"},
	{cidr: "91.0.0.0/8", code: "RU"},
	{cidr: "92.0.0.0/8", code: "RU"},
	{cidr: "93.0.0.0/8", code: "RU"},
	{cidr: "94.0.0.0/8", code: "RU"},
	{cidr: "95.0.0.0/8", code: "RU"},
	{cidr: "96.0.0.0/8", code: "US"},
	{cidr: "97.0.0.0/8", code: "US"},
	{cidr: "98.0.0.0/8", code: "US"},
	{cidr: "99.0.0.0/8", code: "CA"},
	{cidr: "100.0.0.0/8", code: "US"},
	{cidr: "101.0.0.0/8", code: "CN"},
	{cidr: "104.0.0.0/8", code: "US"},
	{cidr: "105.0.0.0/8", code: "ZA"},
	{cidr: "106.0.0.0/8", code: "CN"},
	{cidr: "107.0.0.0/8", code: "US"},
	{cidr: "108.0.0.0/8", code: "US"},
	{cidr: "109.0.0.0/8", code: "RU"},
	{cidr: "110.0.0.0/8", code: "CN"},
	{cidr: "111.0.0.0/8", code: "CN"},
	{cidr: "112.0.0.0/8", code: "CN"},
	{cidr: "113.0.0.0/8", code: "CN"},
	{cidr: "114.0.0.0/8", code: "CN"},
	{cidr: "115.0.0.0/8", code: "CN"},
	{cidr: "116.0.0.0/8", code: "CN"},
	{cidr: "117.0.0.0/8", code: "CN"},
	{cidr: "118.0.0.0/8", code: "CN"},
	{cidr: "119.0.0.0/8", code: "CN"},
	{cidr: "120.0.0.0/8", code: "CN"},
	{cidr: "121.0.0.0/8", code: "CN"},
	{cidr: "122.0.0.0/8", code: "CN"},
	{cidr: "123.0.0.0/8", code: "CN"},
	{cidr: "125.0.0.0/8", code: "KR"},
	{cidr: "126.0.0.0/8", code: "JP"},
	{cidr: "128.0.0.0/8", code: "US"},
	{cidr: "129.0.0.0/8", code: "US"},
	{cidr: "130.0.0.0/8", code: "US"},
	{cidr: "131.0.0.0/8", code: "US"},
	{cidr: "132.0.0.0/8", code: "US"},
	{cidr: "133.0.0.0/8", code: "JP"},
	{cidr: "134.0.0.0/8", code: "US"},
	{cidr: "136.0.0.0/8", code: "US"},
	{cidr: "137.0.0.0/8", code: "US"},
	{cidr: "138.0.0.0/8", code: "US"},
	{cidr: "139.0.0.0/8", code: "AU"},
	{cidr: "140.0.0.0/8", code: "US"},
	{cidr: "141.0.0.0/8", code: "DE"},
	{cidr: "142.0.0.0/8", code: "CA"},
	{cidr: "143.0.0.0/8", code: "US"},
	{cidr: "144.0.0.0/8", code: "AU"},
	{cidr: "145.0.0.0/8", code: "NL"},
	{cidr: "146.0.0.0/8", code: "US"},
	{cidr: "147.0.0.0/8", code: "US"},
	{cidr: "148.0.0.0/8", code: "US"},
	{cidr: "149.0.0.0/8", code: "US"},
	{cidr: "150.0.0.0/8", code: "JP"},
	{cidr: "151.0.0.0/8", code: "IT"},
	{cidr: "152.0.0.0/8", code: "US"},
	{cidr: "153.0.0.0/8", code: "JP"},
	{cidr: "154.0.0.0/8", code: "ZA"},
	{cidr: "155.0.0.0/8", code: "US"},
	{cidr: "156.0.0.0/8", code: "US"},
	{cidr: "157.0.0.0/8", code: "JP"},
	{cidr: "158.0.0.0/8", code: "US"},
	{cidr: "159.0.0.0/8", code: "US"},
	{cidr: "160.0.0.0/8", code: "JP"},
	{cidr: "161.0.0.0/8", code: "US"},
	{cidr: "162.0.0.0/8", code: "US"},
	{cidr: "163.0.0.0/8", code: "CN"},
	{cidr: "164.0.0.0/8", code: "US"},
	{cidr: "165.0.0.0/8", code: "SG"},
	{cidr: "166.0.0.0/8", code: "US"},
	{cidr: "167.0.0.0/8", code: "US"},
	{cidr: "168.0.0.0/8", code: "KR"},
	{cidr: "169.0.0.0/8", code: "US"},
	{cidr: "170.0.0.0/8", code: "US"},
	{cidr: "171.0.0.0/8", code: "CN"},
	{cidr: "172.0.0.0/8", code: "US"},
	{cidr: "173.0.0.0/8", code: "US"},
	{cidr: "174.0.0.0/8", code: "US"},
	{cidr: "175.0.0.0/8", code: "SG"},
	{cidr: "176.0.0.0/8", code: "RU"},
	{cidr: "177.0.0.0/8", code: "BR"},
	{cidr: "178.0.0.0/8", code: "RU"},
	{cidr: "179.0.0.0/8", code: "BR"},
	{cidr: "180.0.0.0/8", code: "CN"},
	{cidr: "181.0.0.0/8", code: "AR"},
	{cidr: "182.0.0.0/8", code: "CN"},
	{cidr: "183.0.0.0/8", code: "CN"},
	{cidr: "184.0.0.0/8", code: "US"},
	{cidr: "185.0.0.0/8", code: "RU"},
	{cidr: "186.0.0.0/8", code: "BR"},
	{cidr: "187.0.0.0/8", code: "BR"},
	{cidr: "188.0.0.0/8", code: "RU"},
	{cidr: "189.0.0.0/8", code: "BR"},
	{cidr: "190.0.0.0/8", code: "BR"},
	{cidr: "191.0.0.0/8", code: "BR"},
	{cidr: "192.0.0.0/8", code: "US"},
	{cidr: "193.0.0.0/8", code: "FR"},
	{cidr: "194.0.0.0/8", code: "DE"},
	{cidr: "195.0.0.0/8", code: "RU"},
	{cidr: "196.0.0.0/8", code: "ZA"},
	{cidr: "197.0.0.0/8", code: "ZA"},
	{cidr: "198.0.0.0/8", code: "US"},
	{cidr: "199.0.0.0/8", code: "US"},
	{cidr: "200.0.0.0/8", code: "BR"},
	{cidr: "201.0.0.0/8", code: "BR"},
	{cidr: "202.0.0.0/8", code: "JP"},
	{cidr: "203.0.0.0/8", code: "AU"},
	{cidr: "204.0.0.0/8", code: "US"},
	{cidr: "205.0.0.0/8", code: "US"},
	{cidr: "206.0.0.0/8", code: "US"},
	{cidr: "207.0.0.0/8", code: "US"},
	{cidr: "208.0.0.0/8", code: "US"},
	{cidr: "209.0.0.0/8", code: "US"},
	{cidr: "210.0.0.0/8", code: "JP"},
	{cidr: "211.0.0.0/8", code: "JP"},
	{cidr: "212.0.0.0/8", code: "RU"},
	{cidr: "213.0.0.0/8", code: "RU"},
	{cidr: "214.0.0.0/8", code: "US"},
	{cidr: "215.0.0.0/8", code: "US"},
	{cidr: "216.0.0.0/8", code: "US"},
	{cidr: "217.0.0.0/8", code: "RU"},
	{cidr: "218.0.0.0/8", code: "JP"},
	{cidr: "219.0.0.0/8", code: "JP"},
	{cidr: "220.0.0.0/8", code: "KR"},
	{cidr: "221.0.0.0/8", code: "CN"},
	{cidr: "222.0.0.0/8", code: "CN"},
	{cidr: "223.0.0.0/8", code: "CN"},

	// ── 常见云厂商 / VPS 网段（蜜罐攻击流量高发区）──
	// DigitalOcean
	{cidr: "45.55.0.0/16", code: "US"},
	{cidr: "64.225.0.0/16", code: "US"},
	{cidr: "67.205.128.0/18", code: "US"},
	{cidr: "104.248.0.0/16", code: "US"},
	{cidr: "128.199.0.0/16", code: "SG"},
	{cidr: "134.209.0.0/16", code: "US"},
	{cidr: "137.184.0.0/16", code: "US"},
	{cidr: "138.68.0.0/16", code: "US"},
	{cidr: "138.197.0.0/16", code: "DE"},
	{cidr: "139.59.0.0/16", code: "DE"},
	{cidr: "142.93.0.0/16", code: "NL"},
	{cidr: "143.110.0.0/16", code: "IN"},
	{cidr: "143.198.0.0/16", code: "US"},
	{cidr: "146.190.0.0/16", code: "US"},
	{cidr: "147.182.0.0/16", code: "US"},
	{cidr: "157.230.0.0/16", code: "US"},
	{cidr: "159.65.0.0/16", code: "US"},
	{cidr: "159.89.0.0/16", code: "US"},
	{cidr: "161.35.0.0/16", code: "US"},
	{cidr: "164.90.0.0/16", code: "US"},
	{cidr: "164.92.0.0/16", code: "DE"},
	{cidr: "165.22.0.0/16", code: "SG"},
	{cidr: "165.227.0.0/16", code: "US"},
	{cidr: "165.232.0.0/16", code: "IN"},
	{cidr: "167.71.0.0/16", code: "US"},
	{cidr: "167.99.0.0/16", code: "DE"},
	{cidr: "167.172.0.0/16", code: "SG"},
	{cidr: "170.64.0.0/16", code: "AU"},
	{cidr: "174.138.0.0/16", code: "US"},
	{cidr: "178.62.0.0/16", code: "GB"},
	{cidr: "178.128.0.0/16", code: "SG"},
	{cidr: "188.166.0.0/16", code: "NL"},
	{cidr: "206.189.0.0/16", code: "US"},
	{cidr: "209.97.128.0/18", code: "GB"},
	{cidr: "207.154.192.0/18", code: "DE"},
	{cidr: "46.101.0.0/16", code: "DE"},
	// Vultr
	{cidr: "45.32.0.0/16", code: "US"},
	{cidr: "45.63.0.0/16", code: "US"},
	{cidr: "45.76.0.0/16", code: "US"},
	{cidr: "45.77.0.0/16", code: "US"},
	{cidr: "64.176.0.0/16", code: "US"},
	{cidr: "66.42.0.0/16", code: "US"},
	{cidr: "70.34.0.0/16", code: "US"},
	{cidr: "78.141.192.0/18", code: "DE"},
	{cidr: "95.179.128.0/17", code: "NL"},
	{cidr: "104.156.0.0/16", code: "US"},
	{cidr: "104.207.128.0/18", code: "US"},
	{cidr: "107.191.0.0/16", code: "US"},
	{cidr: "108.61.0.0/16", code: "US"},
	{cidr: "136.244.64.0/18", code: "US"},
	{cidr: "139.180.0.0/16", code: "SG"},
	{cidr: "140.82.0.0/16", code: "US"},
	{cidr: "144.202.0.0/16", code: "US"},
	{cidr: "149.28.0.0/16", code: "US"},
	{cidr: "155.138.0.0/16", code: "US"},
	{cidr: "158.247.192.0/18", code: "JP"},
	{cidr: "167.179.64.0/18", code: "JP"},
	{cidr: "192.248.128.0/17", code: "SG"},
	{cidr: "198.13.32.0/19", code: "JP"},
	{cidr: "199.247.0.0/16", code: "DE"},
	{cidr: "200.25.0.0/20", code: "CL"},
	{cidr: "202.182.96.0/20", code: "JP"},
	{cidr: "207.148.0.0/16", code: "SG"},
	{cidr: "208.167.224.0/19", code: "US"},
	{cidr: "216.238.64.0/18", code: "US"},
	{cidr: "217.69.0.0/16", code: "DE"},
	{cidr: "217.163.0.0/16", code: "FR"},
	// Hetzner
	{cidr: "5.9.0.0/16", code: "DE"},
	{cidr: "5.75.128.0/17", code: "DE"},
	{cidr: "23.88.0.0/16", code: "DE"},
	{cidr: "46.4.0.0/16", code: "DE"},
	{cidr: "49.12.0.0/16", code: "DE"},
	{cidr: "65.21.0.0/16", code: "FI"},
	{cidr: "65.108.0.0/16", code: "FI"},
	{cidr: "78.46.0.0/16", code: "DE"},
	{cidr: "78.47.0.0/16", code: "DE"},
	{cidr: "85.10.192.0/18", code: "DE"},
	{cidr: "88.99.0.0/16", code: "DE"},
	{cidr: "88.198.0.0/16", code: "DE"},
	{cidr: "91.107.128.0/17", code: "DE"},
	{cidr: "94.130.0.0/16", code: "DE"},
	{cidr: "95.216.0.0/16", code: "FI"},
	{cidr: "116.202.0.0/16", code: "DE"},
	{cidr: "116.203.0.0/16", code: "DE"},
	{cidr: "128.140.0.0/17", code: "DE"},
	{cidr: "135.181.0.0/16", code: "FI"},
	{cidr: "136.243.0.0/16", code: "DE"},
	{cidr: "138.201.0.0/16", code: "DE"},
	{cidr: "142.132.128.0/17", code: "DE"},
	{cidr: "144.76.0.0/16", code: "DE"},
	{cidr: "148.251.0.0/16", code: "DE"},
	{cidr: "157.90.0.0/16", code: "DE"},
	{cidr: "159.69.0.0/16", code: "DE"},
	{cidr: "162.55.0.0/16", code: "DE"},
	{cidr: "167.233.0.0/16", code: "DE"},
	{cidr: "168.119.0.0/16", code: "DE"},
	{cidr: "176.9.0.0/16", code: "DE"},
	{cidr: "188.34.0.0/16", code: "DE"},
	{cidr: "195.201.0.0/16", code: "DE"},
	{cidr: "213.133.96.0/19", code: "DE"},
	{cidr: "213.239.192.0/18", code: "DE"},
	// OVH
	{cidr: "37.187.0.0/16", code: "FR"},
	{cidr: "46.105.0.0/16", code: "FR"},
	{cidr: "51.68.0.0/16", code: "FR"},
	{cidr: "51.75.0.0/16", code: "FR"},
	{cidr: "51.77.0.0/16", code: "FR"},
	{cidr: "51.89.0.0/16", code: "GB"},
	{cidr: "54.36.0.0/16", code: "FR"},
	{cidr: "54.37.0.0/16", code: "FR"},
	{cidr: "54.38.0.0/16", code: "FR"},
	{cidr: "79.137.0.0/16", code: "FR"},
	{cidr: "87.98.128.0/17", code: "FR"},
	{cidr: "91.121.0.0/16", code: "FR"},
	{cidr: "92.222.0.0/16", code: "FR"},
	{cidr: "94.23.0.0/16", code: "FR"},
	{cidr: "137.74.0.0/16", code: "FR"},
	{cidr: "139.99.0.0/16", code: "SG"},
	{cidr: "141.94.0.0/16", code: "FR"},
	{cidr: "141.95.0.0/16", code: "FR"},
	{cidr: "145.239.0.0/16", code: "GB"},
	{cidr: "146.59.0.0/16", code: "FR"},
	{cidr: "147.135.0.0/16", code: "US"},
	{cidr: "149.202.0.0/16", code: "FR"},
	{cidr: "151.80.0.0/16", code: "FR"},
	{cidr: "152.228.128.0/17", code: "FR"},
	{cidr: "158.69.0.0/16", code: "CA"},
	{cidr: "164.132.0.0/16", code: "FR"},
	{cidr: "167.114.0.0/16", code: "CA"},
	{cidr: "176.31.0.0/16", code: "FR"},
	{cidr: "178.32.0.0/16", code: "FR"},
	{cidr: "178.33.0.0/16", code: "FR"},
	{cidr: "185.15.0.0/16", code: "FR"},
	{cidr: "188.165.0.0/16", code: "FR"},
	{cidr: "192.95.0.0/16", code: "CA"},
	{cidr: "192.99.0.0/16", code: "CA"},
	{cidr: "193.70.0.0/16", code: "FR"},
	{cidr: "198.27.64.0/18", code: "CA"},
	{cidr: "198.50.128.0/17", code: "CA"},
	{cidr: "198.100.144.0/20", code: "CA"},
	{cidr: "198.245.48.0/20", code: "CA"},
	{cidr: "213.32.0.0/16", code: "FR"},
	{cidr: "213.186.32.0/19", code: "FR"},
	{cidr: "217.182.0.0/16", code: "FR"},
	// Linode（扫描流量高频来源）
	{cidr: "45.33.0.0/16", code: "US"},
	{cidr: "45.56.0.0/16", code: "US"},
	{cidr: "45.79.0.0/16", code: "US"},
	{cidr: "50.116.0.0/16", code: "US"},
	{cidr: "66.228.0.0/16", code: "US"},
	{cidr: "69.164.0.0/16", code: "US"},
	{cidr: "72.14.0.0/16", code: "US"},
	{cidr: "96.126.0.0/16", code: "US"},
	{cidr: "97.107.0.0/16", code: "US"},
	{cidr: "104.237.0.0/16", code: "US"},
	{cidr: "106.10.0.0/16", code: "JP"},
	{cidr: "139.162.0.0/16", code: "JP"},
	{cidr: "172.104.0.0/16", code: "JP"},
	{cidr: "172.105.0.0/16", code: "JP"},
	{cidr: "176.58.0.0/16", code: "GB"},
	{cidr: "178.79.0.0/16", code: "GB"},
	{cidr: "185.3.0.0/16", code: "GB"},
	{cidr: "212.71.0.0/16", code: "GB"},
	{cidr: "213.219.0.0/16", code: "GB"},
	// 阿里云 / 腾讯云（境外攻击流量常见来源）
	{cidr: "8.130.0.0/16", code: "CN"},
	{cidr: "47.74.0.0/16", code: "JP"},
	{cidr: "47.75.0.0/16", code: "HK"},
	{cidr: "47.76.0.0/16", code: "HK"},
	{cidr: "47.88.0.0/16", code: "US"},
	{cidr: "47.89.0.0/16", code: "US"},
	{cidr: "47.90.0.0/16", code: "US"},
	{cidr: "47.91.0.0/16", code: "HK"},
	{cidr: "47.236.0.0/16", code: "SG"},
	{cidr: "47.237.0.0/16", code: "SG"},
	{cidr: "47.238.0.0/16", code: "SG"},
	{cidr: "47.243.0.0/16", code: "HK"},
	{cidr: "47.246.0.0/16", code: "SG"},
	{cidr: "8.210.0.0/16", code: "HK"},
	{cidr: "43.128.0.0/16", code: "SG"},
	{cidr: "43.129.0.0/16", code: "HK"},
	{cidr: "43.130.0.0/16", code: "SG"},
	{cidr: "43.132.0.0/16", code: "HK"},
	{cidr: "43.133.0.0/16", code: "SG"},
	{cidr: "43.134.0.0/16", code: "SG"},
	{cidr: "43.135.0.0/16", code: "SG"},
	{cidr: "43.152.0.0/16", code: "HK"},
	{cidr: "43.153.0.0/16", code: "SG"},
	{cidr: "43.154.0.0/16", code: "SG"},
	{cidr: "43.155.0.0/16", code: "HK"},
	{cidr: "43.156.0.0/16", code: "SG"},
	{cidr: "43.157.0.0/16", code: "SG"},
	{cidr: "43.159.0.0/16", code: "JP"},
	{cidr: "43.160.0.0/16", code: "JP"},
	{cidr: "43.198.0.0/16", code: "HK"},
	{cidr: "43.199.0.0/16", code: "HK"},
	{cidr: "43.200.0.0/16", code: "KR"},
	{cidr: "43.201.0.0/16", code: "KR"},
	{cidr: "43.206.0.0/16", code: "JP"},
	{cidr: "43.207.0.0/16", code: "JP"},
	{cidr: "150.109.0.0/16", code: "HK"},
	{cidr: "129.226.0.0/16", code: "SG"},
	{cidr: "119.28.0.0/16", code: "HK"},
	{cidr: "119.29.0.0/16", code: "CN"},
	{cidr: "81.69.0.0/16", code: "CN"},
	{cidr: "81.70.0.0/16", code: "CN"},
	{cidr: "81.71.0.0/16", code: "CN"},
	{cidr: "49.51.0.0/16", code: "US"},
	{cidr: "49.52.0.0/16", code: "CN"},
}

// init 解析静态表中的 CIDR 并按掩码长度降序排序（最长前缀优先匹配）。
func init() {
	for i := range staticTable {
		_, n, err := net.ParseCIDR(staticTable[i].cidr)
		if err != nil {
			// 静态表为编译期常量，解析失败属于编程错误，直接 panic 暴露问题。
			panic("geoip: 静态表 CIDR 解析失败: " + staticTable[i].cidr)
		}
		staticTable[i].network = n
		if meta, ok := countryMeta[staticTable[i].code]; ok {
			staticTable[i].name = meta.name
			staticTable[i].lat = meta.lat
			staticTable[i].lon = meta.lon
		} else {
			staticTable[i].name = staticTable[i].code
		}
	}
	sort.SliceStable(staticTable, func(i, j int) bool {
		mi, _ := staticTable[i].network.Mask.Size()
		mj, _ := staticTable[j].network.Mask.Size()
		return mi > mj
	})
}

/*
NewResolver 创建一个空的地理定位解析器（仅静态表，未加载数据库）。

返回:
  - *Resolver: 就绪的解析器实例。
*/
func NewResolver() *Resolver {
	return &Resolver{}
}

/*
LoadMMDB 加载 MaxMind DB 数据库文件。加载后优先使用数据库查询，
数据库查无记录（或解析失败）时自动回退静态表。

参数:
  - path (string): GeoLite2-City.mmdb 等数据库文件的本地路径。

返回:
  - error: 文件不存在、格式非法时返回错误。
*/
func (r *Resolver) LoadMMDB(path string) error {
	db, err := openMMDB(path)
	if err != nil {
		return err
	}
	r.db = db
	return nil
}

/*
Lookup 查询 IP 地址的地理位置。

参数:
  - ip (net.IP): 待查询的 IP 地址，nil 时视为查无结果。

返回:
  - res (Result): 国家与坐标信息。
  - ok (bool): 找到记录时为 true。
*/
func (r *Resolver) Lookup(ip net.IP) (res Result, ok bool) {
	if ip == nil {
		return res, false
	}
	if r.db != nil {
		if res, ok = r.db.Lookup(ip); ok {
			return res, true
		}
	}
	return LookupStatic(ip)
}

/*
LookupStatic 使用内置静态表查询 IP 地址。

参数:
  - ip (net.IP): 待查询的 IP 地址。

返回:
  - res (Result): 国家与坐标信息。
  - ok (bool): 命中静态表时为 true。
*/
func LookupStatic(ip net.IP) (res Result, ok bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return res, false // 静态表不含 IPv6 条目
	}
	// 私有 / 回环 / 未指定地址不参与地理定位（蜜罐事件在 feed 层已过滤）。
	if ip4.IsPrivate() || ip4.IsLoopback() || ip4.IsUnspecified() {
		return res, false
	}
	for i := range staticTable {
		if staticTable[i].network.Contains(ip4) {
			return Result{
				CountryCode: staticTable[i].code,
				CountryName: staticTable[i].name,
				Lat:         staticTable[i].lat,
				Lon:         staticTable[i].lon,
			}, true
		}
	}
	return res, false
}

/*
KnownCountry 返回国家代码对应的中文名与中心坐标。

参数:
  - code (string): ISO 3166-1 alpha-2 国家代码，如 "CN"。

返回:
  - name (string): 中文国家名。
  - lat, lon (float64): 中心坐标。
  - ok (bool): 元信息表中有该代码时为 true。
*/
func KnownCountry(code string) (name string, lat, lon float64, ok bool) {
	meta, ok := countryMeta[code]
	if !ok {
		return code, 0, 0, false
	}
	return meta.name, meta.lat, meta.lon, true
}
