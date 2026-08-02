// 发现记录出口：HTTP 上报或 dry-run 打印。
package record

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Sink 是发现记录的出口。
type Sink interface {
	Submit(ctx context.Context, records []Record) error
}

// HTTPSink 通过 POST /api/v1/discovery-records 批量上报发现记录。
type HTTPSink struct {
	apiURL string
	token  string // CMDB 会话令牌（Bearer）；无认证环境可为空串
	client *http.Client
}

// NewHTTPSink 创建指向上报 CMDB API 的出口。apiURL 形如 http://localhost:8080。
// token 为 CMDB 会话令牌（登录接口签发），服务端启用认证时必填，无认证环境传空串。
func NewHTTPSink(apiURL, token string) *HTTPSink {
	return &HTTPSink{
		apiURL: NormalizeBaseURL(apiURL),
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// batchRequest 与契约 DiscoveryRecordBatchRequest 对应。
type batchRequest struct {
	Records []Record `json:"records"`
}

// batchResponse 与契约 DiscoveryRecordBatchResponse 对应。
type batchResponse struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
	Errors   []struct {
		Index   int    `json:"index"`
		Message string `json:"message"`
	} `json:"errors"`
}

// Submit 批量上报；空批次直接跳过（契约要求 records 至少一条）。
// 服务端拒收（rejected>0）时返回带明细的错误。
func (s *HTTPSink) Submit(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	headers := map[string]string{}
	if s.token != "" {
		headers["Authorization"] = "Bearer " + s.token
	}
	var resp batchResponse
	if err := DoJSON(ctx, s.client, http.MethodPost, s.apiURL+"/api/v1/discovery-records", headers, batchRequest{Records: records}, &resp); err != nil {
		return err
	}
	if resp.Rejected > 0 {
		msgs := make([]string, 0, len(resp.Errors))
		for _, e := range resp.Errors {
			msgs = append(msgs, fmt.Sprintf("第 %d 条: %s", e.Index, e.Message))
		}
		return fmt.Errorf("CMDB 拒收 %d 条记录（已接收 %d 条）: %s", resp.Rejected, resp.Accepted, strings.Join(msgs, "; "))
	}
	return nil
}

// DryRunSink 只把发现记录打印到 w，不做任何网络写入。
type DryRunSink struct {
	w io.Writer
}

// NewDryRunSink 创建 dry-run 出口。
func NewDryRunSink(w io.Writer) *DryRunSink {
	return &DryRunSink{w: w}
}

// Submit 以缩进 JSON 打印本批记录。
func (s *DryRunSink) Submit(_ context.Context, records []Record) error {
	b, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化发现记录失败: %w", err)
	}
	_, err = fmt.Fprintf(s.w, "[dry-run] 本批 %d 条发现记录：\n%s\n", len(records), b)
	return err
}
