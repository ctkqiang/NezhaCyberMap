/*
Package logger 实现零外部依赖的分级日志输出。

所有运行时状态一律通过本包输出到 stderr，禁止使用 log / fmt.Println 直接打印。
日志级别与视觉前缀（见 agent.md 第 6 节）：
  - Info    → [+]   绿色，标准状态变更（feed 已连接、事件批次到达）
  - Warn    → [-]   黄色，非致命问题（feed 超时、单条事件解析失败）
  - Error   → [!]   红色，局部组件故障（GeoIP 数据库损坏）
  - Fatal   → [FATAL] 粗体红色，致命崩溃（打印后直接退出进程）
  - Success → [★]   粗体绿色，关键情报（高风险攻击事件、新 feed 上线）
  - Debug   → [·]   灰色，底层字节流追踪（仅在 --debug 时启用）

颜色通过手写 ANSI 转义序列实现，不引入任何第三方库。
*/
package logger

import (
	"fmt"
	"io"
	"os"
	"sync"
)

const (
	// ANSI 转义序列常量（16 色，兼容绝大多数终端与 Termux）。
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiGray   = "\x1b[90m"
)

// 全局状态：debug 开关与输出目标。仅通过 Set 系列函数修改，日志行本身无状态。
var (
	debugMu sync.Mutex
	debugOn bool
	out     io.Writer = os.Stderr
)

/*
SetDebug 启用或关闭调试日志。

参数:
  - on (bool): true 时启用 [·] 调试输出。
*/
func SetDebug(on bool) {
	debugMu.Lock()
	defer debugMu.Unlock()
	debugOn = on
}

/*
SetOutput 重定向日志输出目标（默认 os.Stderr）。

参数:
  - w (io.Writer): 新的输出目标，可为任意 Writer。
*/
func SetOutput(w io.Writer) {
	out = w
}

/*
Debug 输出调试日志（灰色 [·]）。仅在 SetDebug(true) 后可见。

参数:
  - format (string): 格式化模板。
  - args (...any): 填充参数。
*/
func Debug(format string, args ...any) {
	debugMu.Lock()
	on := debugOn
	debugMu.Unlock()
	if !on {
		return
	}
	writeLine(ansiGray, "[·]", format, args...)
}

/*
Info 输出标准状态日志（绿色 [+]）。

参数:
  - format (string): 格式化模板。
  - args (...any): 填充参数。
*/
func Info(format string, args ...any) {
	writeLine(ansiGreen, "[+]", format, args...)
}

/*
Warn 输出警告日志（黄色 [-]）。

参数:
  - format (string): 格式化模板。
  - args (...any): 填充参数。
*/
func Warn(format string, args ...any) {
	writeLine(ansiYellow, "[-]", format, args...)
}

/*
Error 输出错误日志（红色 [!]）。

参数:
  - format (string): 格式化模板。
  - args (...any): 填充参数。
*/
func Error(format string, args ...any) {
	writeLine(ansiRed, "[!]", format, args...)
}

/*
Success 输出关键情报日志（粗体绿色 [★]）。

参数:
  - format (string): 格式化模板。
  - args (...any): 填充参数。
*/
func Success(format string, args ...any) {
	writeLine(ansiBold+ansiGreen, "[★]", format, args...)
}

/*
Fatal 输出致命错误日志（粗体红色 [FATAL]）并退出进程。

参数:
  - format (string): 格式化模板。
  - args (...any): 填充参数。
*/
func Fatal(format string, args ...any) {
	writeLine(ansiBold+ansiRed, "[FATAL]", format, args...)
	os.Exit(1)
}

/*
writeLine 将单行日志写入输出目标。

参数:
  - color (string): 前缀着色用的 ANSI 序列。
  - prefix (string): 级别前缀（如 "[+]"）。
  - format (string): 消息格式化模板。
  - args (...any): 填充参数。
*/
func writeLine(color, prefix, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(out, "%s%s%s %s%s\n", color, prefix, ansiReset, msg, ansiReset)
}
