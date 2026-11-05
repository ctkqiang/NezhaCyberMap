/*
Package version 提供赛博威胁终端的版本信息与 User-Agent 构造。

该包被 cmd 入口与 internal/feed 共同引用，避免版本号散落多处。
*/
package version

import (
	"fmt"
	"runtime"
)

// 版本常量：遵循语义化版本规范，发布时人工递增。
const (
	// Version 为当前发布版本号。
	Version = "0.1.0"
	// UserAgent 为对外 HTTP 请求的标识，尊重数据源提供方的可识别性要求。
	UserAgent = "cybermap/" + Version + " (+https://github.com/ctkqiang/NezhaCyberMap)"
)

/*
String 返回完整的版本描述字符串（版本 + Go 运行时 + 平台）。

返回:
  - string: 例如 "cybermap v0.1.0 (go1.27.2 darwin/arm64)"。
*/
func String() string {
	return fmt.Sprintf("cybermap v%s (%s %s/%s)", Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
