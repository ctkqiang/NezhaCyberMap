package geoip

/*
本文件实现 MaxMind DB (mmdb) 二进制格式的自研读取器，零外部依赖。

仅支持 GeoLite2-City 数据集中与本项目相关的子集：
  - 元数据解析（node_count / record_size / binary_format_major_version）
  - IPv4 二叉搜索树查询（记录宽度 24 / 28 / 32 位）
  - 数据段解码：country.iso_code、location.latitude、location.longitude

限制（v0.1）：不支持 IPv6 查询；不支持带别名/指针深度嵌套之外的高级特性。
查询失败时调用方应回退 geoip.LookupStatic。
*/

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"os"
)

// mmdb 数据类型编码（控制字节高 3 位）。
const (
	mmdbTypePointer   = 1  // 指针（指向数据段内的绝对偏移）
	mmdbTypeString    = 2  // UTF-8 字符串
	mmdbTypeDouble    = 3  // 8 字节 IEEE754 浮点
	mmdbTypeBytes     = 4  // 原始字节串
	mmdbTypeUint16    = 5  // 2 字节无符号整数
	mmdbTypeUint32    = 6  // 4 字节无符号整数
	mmdbTypeMap       = 7  // 键值映射
	mmdbTypeInt32     = 8  // 4 字节有符号整数
	mmdbTypeUint64    = 9  // 8 字节无符号整数
	mmdbTypeUint128   = 10 // 16 字节无符号整数
	mmdbTypeArray     = 11 // 数组
	mmdbTypeDatacache = 12 // 数据缓存容器
	mmdbTypeEnd       = 13 // 结束标记
	mmdbTypeBoolean   = 14 // 布尔值（size 0/1）
	mmdbTypeFloat     = 15 // 4 字节 IEEE754 浮点
)

// mmdb 是已加载的 MaxMind DB 数据库句柄。
type mmdb struct {
	data       []byte // 整个文件内容
	nodeCount  uint64 // 搜索树节点总数
	recordSize uint8  // 记录宽度（位）：24 / 28 / 32
	nodeSize   uint8  // 单个节点字节数：recordSize*2/8
	dataStart  uint64 // 数据段起始偏移 = nodeCount*nodeSize + 16
}

/*
openMMDB 读取并解析 .mmdb 文件。

参数:
  - path (string): 数据库文件路径。

返回:
  - *mmdb: 就绪的数据库句柄。
  - error: 读取或格式校验失败时返回错误。
*/
func openMMDB(path string) (*mmdb, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("geoip: 读取数据库文件失败: %w", err)
	}
	if len(data) < 35 {
		return nil, fmt.Errorf("geoip: 数据库文件过小（%d 字节），格式非法", len(data))
	}
	// 文件尾部校验：末 14 字节为 \xAB\xCD\xEF + "MaxMind.com"（官方标记）。
	marker := data[len(data)-14:]
	if marker[0] != 0xAB || marker[1] != 0xCD || marker[2] != 0xEF ||
		string(marker[3:]) != "MaxMind.com" {
		return nil, fmt.Errorf("geoip: 文件缺少 MaxMind 标记，不是合法的 mmdb 文件")
	}
	// 元数据段指针位于标记前 16 字节（uint128，实际取值 < 2^64，取低 8 字节）。
	metaPtr := binary.BigEndian.Uint64(data[len(data)-22 : len(data)-14])
	if metaPtr >= uint64(len(data)) {
		return nil, fmt.Errorf("geoip: 元数据指针越界（%d >= %d）", metaPtr, len(data))
	}
	db := &mmdb{data: data}
	dec := &mmdbDecoder{buf: data, pos: int(metaPtr), dataStart: 0}
	if err := db.parseMetadata(dec); err != nil {
		return nil, err
	}
	if db.nodeCount == 0 || db.recordSize == 0 {
		return nil, fmt.Errorf("geoip: 元数据缺少 node_count / record_size")
	}
	switch db.recordSize {
	case 24, 28, 32:
	default:
		return nil, fmt.Errorf("geoip: 不支持的记录宽度 %d 位（仅支持 24/28/32）", db.recordSize)
	}
	db.nodeSize = db.recordSize * 2 / 8
	db.dataStart = db.nodeCount*uint64(db.nodeSize) + 16
	dec.dataStart = db.dataStart
	return db, nil
}

/*
Close 释放数据库文件占用的内存。

返回:
  - error: 恒为 nil，保留签名以兼容资源管理习惯。
*/
func (db *mmdb) Close() error {
	db.data = nil
	return nil
}

/*
Lookup 查询 IPv4 地址的地理位置。

参数:
  - ip (net.IP): 待查询 IP，仅支持 IPv4（IPv6 返回 false）。

返回:
  - res (Result): 国家与坐标。
  - ok (bool): 命中记录时为 true。
*/
func (db *mmdb) Lookup(ip net.IP) (res Result, ok bool) {
	if db == nil || db.data == nil {
		return res, false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return res, false // v0.1 不支持 IPv6 查询
	}
	pos, found := db.searchIPv4(ip4)
	if !found {
		return res, false
	}
	dec := &mmdbDecoder{buf: db.data, pos: pos, dataStart: db.dataStart}
	code, lat, lon, found := dec.readRecord()
	if !found {
		return res, false
	}
	res.CountryCode = code
	res.CountryName = code
	if name, _, _, okMeta := KnownCountry(code); okMeta {
		res.CountryName = name
	}
	res.Lat = lat
	res.Lon = lon
	return res, true
}

/*
searchIPv4 在二叉搜索树中查询 IPv4 地址对应的数据段偏移。

参数:
  - ip4 (net.IP): 4 字节 IPv4 地址。

返回:
  - offset (int): 数据段内的记录偏移（绝对文件偏移）。
  - ok (bool): 找到数据记录时为 true。
*/
func (db *mmdb) searchIPv4(ip4 net.IP) (offset int, ok bool) {
	node := uint64(0)
	for i := 0; i < 32; i++ {
		val := db.readNode(node)
		if val >= db.nodeCount {
			// 值不小于节点总数时为数据指针：数据偏移 = 值 - 节点数 + 数据段起始。
			off := val - db.nodeCount + db.dataStart
			if off >= uint64(len(db.data)) {
				return 0, false
			}
			return int(off), true
		}
		bit := (ip4[i/8] >> (7 - i%8)) & 1
		if bit == 0 {
			node = val*2 + 1
		} else {
			node = val*2 + 2
		}
	}
	return 0, false
}

/*
readNode 读取指定节点的记录值（recordSize 位大端整数）。

参数:
  - node (uint64): 节点编号。

返回:
  - uint64: 节点值。
*/
func (db *mmdb) readNode(node uint64) uint64 {
	off := node * uint64(db.nodeSize)
	switch db.recordSize {
	case 24:
		return uint64(db.data[off])<<16 |
			uint64(db.data[off+1])<<8 |
			uint64(db.data[off+2])
	case 28:
		return uint64(db.data[off])<<20 |
			uint64(db.data[off+1])<<12 |
			uint64(db.data[off+2])<<4 |
			uint64(db.data[off+3])>>4
	default: // 32
		return uint64(binary.BigEndian.Uint32(db.data[off:]))
	}
}

/*
parseMetadata 解析元数据段中的必需字段。

参数:
  - dec (*mmdbDecoder): 定位到元数据段起始的解码器。

返回:
  - error: 格式非法时返回错误。
*/
func (db *mmdb) parseMetadata(dec *mmdbDecoder) error {
	typ, size, err := dec.readControl()
	if err != nil {
		return err
	}
	if typ != mmdbTypeMap {
		return fmt.Errorf("geoip: 元数据段不是 map 类型（实际类型 %d）", typ)
	}
	var major uint16
	for i := 0; i < size; i++ {
		key, err := dec.readKey()
		if err != nil {
			return err
		}
		switch key {
		case "binary_format_major_version":
			if major, err = dec.readUint16Indirect(); err != nil {
				return err
			}
		case "binary_format_minor_version":
			// 次版本无需保存，读取并跳过即可。
			if _, err = dec.readUint16Indirect(); err != nil {
				return err
			}
		case "node_count":
			v, err := dec.readUint32Indirect()
			if err != nil {
				return err
			}
			db.nodeCount = uint64(v)
		case "record_size":
			v, err := dec.readUint16Indirect()
			if err != nil {
				return err
			}
			db.recordSize = uint8(v)
		default:
			if err := dec.skipValue(); err != nil {
				return err
			}
		}
	}
	if major != 2 {
		return fmt.Errorf("geoip: 不支持的二进制格式主版本 %d（仅支持 2）", major)
	}
	return nil
}

// mmdbDecoder 在数据库字节切片上顺序游走解码。
type mmdbDecoder struct {
	buf       []byte // 完整文件内容
	pos       int    // 当前读取位置（绝对偏移）
	dataStart uint64 // 数据段起始偏移（指针解引用时使用）
}

/*
readControl 读取一个控制字节，拆分类型与尺寸。

返回:
  - typ (uint8): 数据类型（mmdbType* 常量）。
  - size (int): 已扩展的尺寸值。
  - err (error): 越界时返回错误。
*/
func (d *mmdbDecoder) readControl() (typ uint8, size int, err error) {
	if d.pos >= len(d.buf) {
		return 0, 0, fmt.Errorf("geoip: 解码越界（偏移 %d）", d.pos)
	}
	c := d.buf[d.pos]
	d.pos++
	typ = c >> 5
	s := int(c & 0x1F)
	switch {
	case s < 29:
		size = s
	case s == 29:
		if d.pos >= len(d.buf) {
			return 0, 0, fmt.Errorf("geoip: 解码越界（偏移 %d）", d.pos)
		}
		size = 29 + int(d.buf[d.pos])
		d.pos++
	case s == 30:
		if d.pos+2 > len(d.buf) {
			return 0, 0, fmt.Errorf("geoip: 解码越界（偏移 %d）", d.pos)
		}
		size = 285 + int(binary.BigEndian.Uint16(d.buf[d.pos:]))
		d.pos += 2
	default: // 31
		if d.pos+3 > len(d.buf) {
			return 0, 0, fmt.Errorf("geoip: 解码越界（偏移 %d）", d.pos)
		}
		size = 65821 + int(d.buf[d.pos])<<16 + int(binary.BigEndian.Uint16(d.buf[d.pos+1:]))
		d.pos += 3
	}
	return typ, size, nil
}

/*
readKey 读取 map 条目的键（约定为字符串类型）。

返回:
  - key (string): 键文本。
  - err (error): 越界或类型不符时返回错误。
*/
func (d *mmdbDecoder) readKey() (key string, err error) {
	typ, size, err := d.readControl()
	if err != nil {
		return "", err
	}
	if typ != mmdbTypeString {
		return "", fmt.Errorf("geoip: map 键类型非法（%d），期望字符串", typ)
	}
	if d.pos+size > len(d.buf) {
		return "", fmt.Errorf("geoip: 解码越界（偏移 %d）", d.pos)
	}
	key = string(d.buf[d.pos : d.pos+size])
	d.pos += size
	return key, nil
}

/*
readUint16Indirect 读取一个 uint16 值（自动解引用指针）。

返回:
  - v (uint16): 数值。
  - err (error): 类型不符或越界时返回错误。
*/
func (d *mmdbDecoder) readUint16Indirect() (v uint16, err error) {
	typ, _, err := d.readControl()
	if err != nil {
		return 0, err
	}
	if typ == mmdbTypePointer {
		if err = d.jumpPointer(); err != nil {
			return 0, err
		}
		typ, _, err = d.readControl()
		if err != nil {
			return 0, err
		}
	}
	if typ != mmdbTypeUint16 {
		return 0, fmt.Errorf("geoip: 期望 uint16，实际类型 %d", typ)
	}
	if d.pos+2 > len(d.buf) {
		return 0, fmt.Errorf("geoip: 解码越界（偏移 %d）", d.pos)
	}
	v = binary.BigEndian.Uint16(d.buf[d.pos:])
	d.pos += 2
	return v, nil
}

/*
readUint32Indirect 读取一个 uint32 值（自动解引用指针）。

返回:
  - v (uint32): 数值。
  - err (error): 类型不符或越界时返回错误。
*/
func (d *mmdbDecoder) readUint32Indirect() (v uint32, err error) {
	typ, _, err := d.readControl()
	if err != nil {
		return 0, err
	}
	if typ == mmdbTypePointer {
		if err = d.jumpPointer(); err != nil {
			return 0, err
		}
		typ, _, err = d.readControl()
		if err != nil {
			return 0, err
		}
	}
	if typ != mmdbTypeUint32 {
		return 0, fmt.Errorf("geoip: 期望 uint32，实际类型 %d", typ)
	}
	if d.pos+4 > len(d.buf) {
		return 0, fmt.Errorf("geoip: 解码越界（偏移 %d）", d.pos)
	}
	v = binary.BigEndian.Uint32(d.buf[d.pos:])
	d.pos += 4
	return v, nil
}

/*
jumpPointer 消费当前指针并跳转到目标位置（调用前控制字节已读取）。

返回:
  - error: 越界时返回错误。
*/
func (d *mmdbDecoder) jumpPointer() error {
	c := d.buf[d.pos-1]
	indicator := (c >> 3) & 3 // 指针总字节数 = indicator + 2
	extra := int(indicator) + 1
	if d.pos+extra > len(d.buf) {
		return fmt.Errorf("geoip: 指针越界（偏移 %d）", d.pos)
	}
	val := uint64(c & 7)
	for i := 0; i < extra; i++ {
		val = val<<8 | uint64(d.buf[d.pos])
		d.pos++
	}
	target := val + d.dataStart
	if target >= uint64(len(d.buf)) {
		return fmt.Errorf("geoip: 指针目标越界（%d）", target)
	}
	d.pos = int(target)
	return nil
}

/*
skipValue 跳过当前位置的任意类型值（调用前控制字节已读取）。

返回:
  - error: 越界时返回错误。
*/
func (d *mmdbDecoder) skipValue() error {
	typ, size, err := d.readControl()
	if err != nil {
		return err
	}
	switch typ {
	case mmdbTypePointer:
		c := d.buf[d.pos-1]
		return d.jumpPointerConsume(c)
	case mmdbTypeString, mmdbTypeBytes, mmdbTypeDatacache:
		return d.advance(size)
	case mmdbTypeDouble, mmdbTypeUint64, mmdbTypeUint128:
		return d.advance(8)
	case mmdbTypeUint16:
		return d.advance(2)
	case mmdbTypeUint32, mmdbTypeInt32, mmdbTypeFloat:
		return d.advance(4)
	case mmdbTypeBoolean, mmdbTypeEnd:
		return nil
	case mmdbTypeMap:
		for i := 0; i < size; i++ {
			if _, err := d.readKey(); err != nil {
				return err
			}
			if err := d.skipValue(); err != nil {
				return err
			}
		}
		return nil
	case mmdbTypeArray:
		for i := 0; i < size; i++ {
			if err := d.skipValue(); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("geoip: 未知数据类型 %d", typ)
	}
}

/*
jumpPointerConsume 跳过指针类型的剩余字节（指针已被消费时使用）。

参数:
  - c (byte): 已读取的指针控制字节。

返回:
  - error: 越界时返回错误。
*/
func (d *mmdbDecoder) jumpPointerConsume(c byte) error {
	extra := int((c>>3)&3) + 1
	return d.advance(extra)
}

/*
advance 前移 n 字节并做边界检查。

参数:
  - n (int): 前进字节数。

返回:
  - error: 越界时返回错误。
*/
func (d *mmdbDecoder) advance(n int) error {
	if d.pos+n > len(d.buf) {
		return fmt.Errorf("geoip: 解码越界（偏移 %d + %d）", d.pos, n)
	}
	d.pos += n
	return nil
}

/*
readRecord 在当前位置解析一条 GeoLite2-City 记录（map）。

返回:
  - code (string): ISO 国家代码。
  - lat, lon (float64): 经纬度。
  - ok (bool): 成功解析出完整信息时为 true。
*/
func (d *mmdbDecoder) readRecord() (code string, lat, lon float64, ok bool) {
	typ, size, err := d.readControl()
	if err != nil || typ != mmdbTypeMap {
		return "", 0, 0, false
	}
	for i := 0; i < size; i++ {
		key, err := d.readKey()
		if err != nil {
			return "", 0, 0, false
		}
		switch key {
		case "country", "registered_country":
			if code == "" {
				if c, okC := d.readCountry(); okC {
					code = c
				}
			}
		case "location":
			if l, lo, okL := d.readLocation(); okL {
				lat, lon, ok = l, lo, true
			}
		default:
			if err := d.skipValue(); err != nil {
				return "", 0, 0, false
			}
		}
	}
	if code == "" || !ok {
		return "", 0, 0, false
	}
	return code, lat, lon, true
}

/*
readCountry 解析国家子 map 中的 iso_code 字段。

返回:
  - code (string): ISO 国家代码。
  - ok (bool): 找到 iso_code 时为 true。
*/
func (d *mmdbDecoder) readCountry() (code string, ok bool) {
	typ, size, err := d.readControl()
	if err != nil {
		return "", false
	}
	if typ == mmdbTypePointer {
		if err := d.jumpPointer(); err != nil {
			return "", false
		}
		typ, size, err = d.readControl()
		if err != nil {
			return "", false
		}
	}
	if typ != mmdbTypeMap {
		return "", false
	}
	for i := 0; i < size; i++ {
		key, err := d.readKey()
		if err != nil {
			return "", false
		}
		if key == "iso_code" {
			c, okC := d.readStringIndirect()
			return c, okC
		}
		if err := d.skipValue(); err != nil {
			return "", false
		}
	}
	return "", false
}

/*
readLocation 解析 location 子 map 中的经纬度字段。

返回:
  - lat, lon (float64): 经纬度。
  - ok (bool): 找到坐标时为 true。
*/
func (d *mmdbDecoder) readLocation() (lat, lon float64, ok bool) {
	typ, size, err := d.readControl()
	if err != nil {
		return 0, 0, false
	}
	if typ == mmdbTypePointer {
		if err := d.jumpPointer(); err != nil {
			return 0, 0, false
		}
		typ, size, err = d.readControl()
		if err != nil {
			return 0, 0, false
		}
	}
	if typ != mmdbTypeMap {
		return 0, 0, false
	}
	var hasLat, hasLon bool
	for i := 0; i < size; i++ {
		key, err := d.readKey()
		if err != nil {
			return 0, 0, false
		}
		switch key {
		case "latitude":
			if v, errD := d.readDoubleIndirect(); errD == nil {
				lat, hasLat = v, true
			}
		case "longitude":
			if v, errD := d.readDoubleIndirect(); errD == nil {
				lon, hasLon = v, true
			}
		default:
			if err := d.skipValue(); err != nil {
				return 0, 0, false
			}
		}
	}
	return lat, lon, hasLat && hasLon
}

/*
readStringIndirect 读取一个字符串值（自动解引用指针）。

返回:
  - s (string): 字符串内容。
  - ok (bool): 成功时为 true。
*/
func (d *mmdbDecoder) readStringIndirect() (s string, ok bool) {
	typ, size, err := d.readControl()
	if err != nil {
		return "", false
	}
	if typ == mmdbTypePointer {
		if err := d.jumpPointer(); err != nil {
			return "", false
		}
		typ, size, err = d.readControl()
		if err != nil {
			return "", false
		}
	}
	if typ != mmdbTypeString || d.pos+size > len(d.buf) {
		return "", false
	}
	s = string(d.buf[d.pos : d.pos+size])
	d.pos += size
	return s, true
}

/*
readDoubleIndirect 读取一个 double 值（自动解引用指针）。

返回:
  - v (float64): 数值。
  - err (error): 类型不符或越界时返回错误。
*/
func (d *mmdbDecoder) readDoubleIndirect() (v float64, err error) {
	typ, _, err := d.readControl()
	if err != nil {
		return 0, err
	}
	if typ == mmdbTypePointer {
		if err := d.jumpPointer(); err != nil {
			return 0, err
		}
		typ, _, err = d.readControl()
		if err != nil {
			return 0, err
		}
	}
	if typ != mmdbTypeDouble {
		return 0, fmt.Errorf("geoip: 期望 double，实际类型 %d", typ)
	}
	if d.pos+8 > len(d.buf) {
		return 0, fmt.Errorf("geoip: 解码越界（偏移 %d）", d.pos)
	}
	v = math.Float64frombits(binary.BigEndian.Uint64(d.buf[d.pos:]))
	d.pos += 8
	return v, nil
}
