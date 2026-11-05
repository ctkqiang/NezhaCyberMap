/*
Package web 静态资源嵌入：前端文件随二进制分发（单文件部署）。
*/
package web

import "embed"

// Static 是内嵌的前端静态资源（index.html / style.css / app.js / world-data.js）。
//
//go:embed static
var Static embed.FS
