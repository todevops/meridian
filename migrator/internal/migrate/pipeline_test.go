// 管道模式单测：端到端（进程内夹具）跑两遍验证 netbox_id 调和幂等，
// 分批边界、429/5xx 退避重试与重试耗尽、报告 pipeline 段统计。
package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"migrator/internal/cmdb"
	"migrator/internal/netbox"
)

// runPipelineMigration 起夹具并以管道模式执行迁移。
func runPipelineMigration(t *testing.T, fake *fakeCMDB, nbURL, cmURL string) *Report {
	t.Helper()
	m := New(netbox.NewClient(nbURL, "token"), cmdb.NewClient(cmURL))
	report, err := m.RunWithOptions(context.Background(), nbURL, cmURL, Options{Mode: ModePipeline})
	if err != nil {
		t.Fatalf("管道迁移致命错误: %v", err)
	}
	return report
}

// TestPipelineHappyPathAndIdempotency 验证管道模式全量映射与幂等：
// 第一遍全量新建，第二遍按 netbox_id 调和命中存量 CI（零新建）。
func TestPipelineHappyPathAndIdempotency(t *testing.T) {
	nbStub := newNetboxFixture(t, false)
	t.Cleanup(nbStub.Close)
	fake := newFakeCMDB()
	cmStub := httptest.NewServer(fake.handler())
	t.Cleanup(cmStub.Close)

	// 第一遍：9 条记录（2 站点+1 机架+2 设备+2 VLAN+2 VM），各实体一批。
	r1 := runPipelineMigration(t, fake, nbStub.URL, cmStub.URL)
	if r1.Mode != ModePipeline || r1.Pipeline == nil {
		t.Fatalf("报告缺少管道段: %+v", r1)
	}
	if r1.Pipeline.Batches != 5 || r1.Pipeline.Records != 9 {
		t.Errorf("管道统计异常: %+v", r1.Pipeline)
	}
	if r1.Pipeline.Accepted != 9 || r1.Pipeline.Rejected != 0 || r1.Pipeline.Backoffs != 0 {
		t.Errorf("第一遍摄入异常: %+v", r1.Pipeline)
	}
	if len(fake.cis) != 9 {
		t.Fatalf("第一遍应建档 9 个 CI，实际 %d", len(fake.cis))
	}
	// 每条记录都带留痕与标准信封字段（经调和落库后的 CI 属性体现）。
	for _, ci := range fake.cis {
		attrs := ci["attributes"].(map[string]any)
		if attrs["netbox_id"] == nil || attrs["netbox_id"] == "" {
			t.Errorf("CI 缺少 netbox_id 留痕: %v", attrs)
		}
		if ci["source"] != cmdb.MigrationSource {
			t.Errorf("CI source 应为 %s，实际 %v", cmdb.MigrationSource, ci["source"])
		}
	}
	// IPAM 仍走 direct：前缀树与兜底不变。
	if fake.findPrefix("10.1.2.0/24") == nil || fake.findPrefix("192.168.9.0/24") == nil {
		t.Error("管道模式下 IPAM 应仍走 direct 建前缀")
	}
	// 摘要包含管道段。
	if !strings.Contains(r1.Summary(), "管道上报") {
		t.Errorf("摘要缺少管道段:\n%s", r1.Summary())
	}

	// 第二遍：净新建为零（全部按 netbox_id 命中存量 CI），accepted 仍为 9。
	r2 := runPipelineMigration(t, fake, nbStub.URL, cmStub.URL)
	if r2.Pipeline.Accepted != 9 || r2.Pipeline.Rejected != 0 {
		t.Errorf("第二遍摄入异常: %+v", r2.Pipeline)
	}
	if len(fake.cis) != 9 {
		t.Fatalf("第二遍应零新建（仍 9 个 CI），实际 %d", len(fake.cis))
	}
}

// ingestSpy 记录每批上报大小的夹具服务器。
type ingestSpy struct {
	mu         sync.Mutex
	batchSizes []int
	failTimes  int  // 前 N 次请求返回 failStatus
	failStatus int  // 失败状态码（429/503）
	calls      int
}

func (s *ingestSpy) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls++
		fail := s.calls <= s.failTimes
		status := s.failStatus
		s.mu.Unlock()
		if fail {
			respondErr(w, status, "RATE_LIMITED", "稍后再试")
			return
		}
		var body struct {
			Records []cmdb.DiscoveryRecord `json:"records"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.batchSizes = append(s.batchSizes, len(body.Records))
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(cmdb.IngestResult{Accepted: len(body.Records), Errors: []cmdb.RecordError{}})
	})
}

// newTestUploader 构造测试用上传器（高限速免等待；退避睡眠仅记录不真实等待）。
func newTestUploader(url string, maxRetry int, delays *[]time.Duration, stats *PipelineReport) *batchUploader {
	return &batchUploader{
		client:  cmdb.NewClient(url),
		limiter: newTokenBucket(100000),
		backoff: &backoffPolicy{
			base:     time.Millisecond,
			maxRetry: maxRetry,
			sleep: func(_ context.Context, d time.Duration) error {
				*delays = append(*delays, d)
				return nil
			},
		},
		stats: stats,
	}
}

// makeRecords 构造 n 条合法记录。
func makeRecords(n int) []cmdb.DiscoveryRecord {
	records := make([]cmdb.DiscoveryRecord, 0, n)
	for i := 0; i < n; i++ {
		records = append(records, toRecord("room", map[string]any{"netbox_id": fmt.Sprintf("%d", i)}, time.Now()))
	}
	return records
}

// TestBatchBoundaries 验证分批边界：5 条按 2 分批 → [2,2,1]。
func TestBatchBoundaries(t *testing.T) {
	spy := &ingestSpy{}
	srv := httptest.NewServer(spy.handler())
	t.Cleanup(srv.Close)

	stats := &PipelineReport{}
	ent := &EntityReport{Entity: "x"}
	u := newTestUploader(srv.URL, 0, &[]time.Duration{}, stats)
	u.upload(context.Background(), ent, makeRecords(5), 2)

	want := []int{2, 2, 1}
	if fmt.Sprint(spy.batchSizes) != fmt.Sprint(want) {
		t.Errorf("分批边界异常: 期望 %v，实际 %v", want, spy.batchSizes)
	}
	if stats.Batches != 3 || stats.Records != 5 || stats.Accepted != 5 || stats.Rejected != 0 {
		t.Errorf("统计异常: %+v", stats)
	}
	if ent.Succeeded != 5 || ent.Failed != 0 {
		t.Errorf("实体计数异常: %+v", ent)
	}
}

// TestRetryThenSuccess 验证 429 指数退避后成功：退避序列 base/2×base，统计退避次数与重试条数。
func TestRetryThenSuccess(t *testing.T) {
	spy := &ingestSpy{failTimes: 2, failStatus: http.StatusTooManyRequests}
	srv := httptest.NewServer(spy.handler())
	t.Cleanup(srv.Close)

	stats := &PipelineReport{}
	ent := &EntityReport{Entity: "x"}
	delays := []time.Duration{}
	u := newTestUploader(srv.URL, 5, &delays, stats)
	u.upload(context.Background(), ent, makeRecords(3), 200)

	if spy.calls != 3 {
		t.Errorf("应调用 3 次（2 次 429 + 1 次成功），实际 %d", spy.calls)
	}
	if len(delays) != 2 || delays[0] != time.Millisecond || delays[1] != 2*time.Millisecond {
		t.Errorf("退避序列异常: %v", delays)
	}
	if stats.Backoffs != 2 || stats.RetriedRecords != 6 || stats.Accepted != 3 {
		t.Errorf("重试统计异常: %+v", stats)
	}
}

// TestRetryExhausted 验证持续 503 重试耗尽：整批记失败后不再调用。
func TestRetryExhausted(t *testing.T) {
	spy := &ingestSpy{failTimes: 100, failStatus: http.StatusServiceUnavailable}
	srv := httptest.NewServer(spy.handler())
	t.Cleanup(srv.Close)

	stats := &PipelineReport{}
	ent := &EntityReport{Entity: "x"}
	u := newTestUploader(srv.URL, 2, &[]time.Duration{}, stats)
	u.upload(context.Background(), ent, makeRecords(3), 200)

	if spy.calls != 3 { // 首次 + 2 次重试
		t.Errorf("应调用 3 次（maxRetry=2），实际 %d", spy.calls)
	}
	if stats.Rejected != 3 || stats.Accepted != 0 || ent.Failed != 3 {
		t.Errorf("耗尽后计数异常: stats=%+v ent=%+v", stats, ent)
	}
}

// TestNonRetryableError 验证 4xx（非 429）不重试。
func TestNonRetryableError(t *testing.T) {
	spy := &ingestSpy{failTimes: 100, failStatus: http.StatusBadRequest}
	srv := httptest.NewServer(spy.handler())
	t.Cleanup(srv.Close)

	stats := &PipelineReport{}
	ent := &EntityReport{Entity: "x"}
	u := newTestUploader(srv.URL, 5, &[]time.Duration{}, stats)
	u.upload(context.Background(), ent, makeRecords(2), 200)

	if spy.calls != 1 {
		t.Errorf("400 不应重试，实际调用 %d 次", spy.calls)
	}
	if stats.Backoffs != 0 || stats.Rejected != 2 {
		t.Errorf("统计异常: %+v", stats)
	}
}

// TestIsRetryable 验证可重试错误分类。
func TestIsRetryable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&cmdb.APIError{StatusCode: 429}, true},
		{&cmdb.APIError{StatusCode: 500}, true},
		{&cmdb.APIError{StatusCode: 503}, true},
		{&cmdb.APIError{StatusCode: 400}, false},
		{&cmdb.APIError{StatusCode: 404}, false},
		{fmt.Errorf("网络错误"), false},
	}
	for _, c := range cases {
		if got := isRetryable(c.err); got != c.want {
			t.Errorf("isRetryable(%v) = %v，期望 %v", c.err, got, c.want)
		}
	}
}

// TestPipelineRejectedRecords 验证服务端拒绝的记录计入失败明细（含 netbox_id 回查）。
func TestPipelineRejectedRecords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cmdb.IngestResult{
			Accepted: 1,
			Rejected: 1,
			Errors:   []cmdb.RecordError{{Index: 1, Message: "缺少必填字段: source"}},
		})
	}))
	t.Cleanup(srv.Close)

	stats := &PipelineReport{}
	ent := &EntityReport{Entity: "x"}
	u := newTestUploader(srv.URL, 0, &[]time.Duration{}, stats)
	u.upload(context.Background(), ent, makeRecords(2), 200)

	if ent.Succeeded != 1 || ent.Failed != 1 || len(ent.Failures) != 1 {
		t.Fatalf("拒绝计数异常: %+v", ent)
	}
	if ent.Failures[0].NetboxID != "1" || !strings.Contains(ent.Failures[0].Error, "source") {
		t.Errorf("拒绝明细异常: %+v", ent.Failures[0])
	}
	if stats.Accepted != 1 || stats.Rejected != 1 {
		t.Errorf("统计异常: %+v", stats)
	}
}
