// Package mocks 提供内嵌的 mock fixture 数据文件，
// 使 mockd 二进制脱离工作目录也能直接运行。
package mocks

import "embed"

// FS 内嵌 fixtures/ 目录下全部 mock 数据文件。
//
//go:embed all:fixtures
var FS embed.FS
