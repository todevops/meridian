// 环境变量与 HTTP 公共工具。
package record

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Getenv 读取环境变量，去除首尾空白；为空时返回默认值。
func Getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// NormalizeBaseURL 把 ":19005"、"localhost:19005" 等简写规范化为 http:// 开头的 base URL，
// 并去掉末尾斜杠，便于与环境变量约定（默认值只给端口号）对接。
func NormalizeBaseURL(v string) string {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if v == "" {
		return v
	}
	if strings.HasPrefix(v, ":") {
		return "http://localhost" + v
	}
	if !strings.Contains(v, "://") {
		return "http://" + v
	}
	return v
}

// DoJSON 发起 JSON HTTP 请求并把响应体解码到 out（out 为 nil 时只校验状态码）。
// 非 2xx 状态码返回包含状态码与响应摘要的错误。
func DoJSON(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("序列化请求体失败: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 %s %s 失败: %w", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("读取 %s %s 响应失败: %w", method, url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("请求 %s %s 返回状态码 %d: %s", method, url, resp.StatusCode, truncate(string(data), 200))
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("解析 %s %s 响应 JSON 失败: %w", method, url, err)
		}
	}
	return nil
}

// truncate 截断字符串用于错误摘要。
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
