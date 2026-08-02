package mocksys

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	mocks "meridian/mocks"
)

// readFixture 从内嵌 FS 读取 fixture 文件。
func readFixture(name string) ([]byte, error) {
	return fs.ReadFile(mocks.FS, "fixtures/"+name)
}

// writeJSON 以指定状态码输出 JSON 响应体。
func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"内部序列化失败"}`, http.StatusInternalServerError)
		return
	}
	writeRawJSON(w, code, body)
}

// writeRawJSON 原样输出已序列化的 JSON 字节。
func writeRawJSON(w http.ResponseWriter, code int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

// bearerToken 解析 Authorization: Bearer <token>，缺失或为空返回 ""。
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// apiToken 解析 Authorization: Token <token>（NetBox 风格），缺失或为空返回 ""。
func apiToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || scheme != "Token" {
		return ""
	}
	return strings.TrimSpace(token)
}

// intQuery 解析整型查询参数，缺省或非法时返回 def。
func intQuery(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// requestID 生成火山引擎 CloudControl 风格的随机 RequestId。
func requestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "00000000-0000-0000-0000-000000000000"
	}
	return strings.ToUpper(hex.EncodeToString(buf))
}
