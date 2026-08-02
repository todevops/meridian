// CMDB 会话令牌解析：优先静态 MERIDIAN_TOKEN，否则用用户名密码登录换取。
package record

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// ResolveCMDBToken 按环境变量解析 CMDB Bearer 令牌：
// MERIDIAN_TOKEN 非空直接返回；否则若 MERIDIAN_USERNAME 与 MERIDIAN_PASSWORD 均非空，
// 调 POST {apiURL}/api/v1/auth/login 登录换取 token；三者皆空返回空串（无认证环境）。
func ResolveCMDBToken(ctx context.Context, apiURL string) (string, error) {
	if t := strings.TrimSpace(os.Getenv("MERIDIAN_TOKEN")); t != "" {
		return t, nil
	}
	user := strings.TrimSpace(os.Getenv("MERIDIAN_USERNAME"))
	pass := os.Getenv("MERIDIAN_PASSWORD")
	if user == "" || pass == "" {
		return "", nil
	}
	var out struct {
		Token string `json:"token"`
	}
	body := map[string]string{"username": user, "password": pass}
	if err := DoJSON(ctx, &http.Client{Timeout: 15 * time.Second}, http.MethodPost,
		NormalizeBaseURL(apiURL)+"/api/v1/auth/login", nil, body, &out); err != nil {
		return "", fmt.Errorf("CMDB 登录失败: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("CMDB 登录响应缺少 token 字段")
	}
	return out.Token, nil
}
