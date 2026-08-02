// 限速与退避单测：令牌桶节奏（小速率真实计时）、退避序列与封顶、分批边界。
package migrate

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestTokenBucketPacing 验证小速率下的令牌节奏：空桶起步，10 个令牌 @20/s 约需 0.5s。
func TestTokenBucketPacing(t *testing.T) {
	b := newTokenBucket(20)
	start := time.Now()
	if err := b.Wait(context.Background(), 10); err != nil {
		t.Fatalf("Wait 失败: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 400*time.Millisecond {
		t.Errorf("节奏过快：10 令牌 @20/s 耗时 %v，应 ≥0.4s", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("节奏过慢：耗时 %v，应远小于 3s", elapsed)
	}
}

// TestTokenBucketChunking 验证单次等待数大于桶容量时按容量分块等待。
func TestTokenBucketChunking(t *testing.T) {
	b := newTokenBucket(5) // 容量 5
	start := time.Now()
	if err := b.Wait(context.Background(), 12); err != nil { // 12 = 5+5+2 三块
		t.Fatalf("Wait 失败: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 2*time.Second { // 12 令牌 @5/s ≈ 2.4s
		t.Errorf("分块等待节奏异常：耗时 %v，应 ≥2s", elapsed)
	}
}

// TestTokenBucketCancel 验证 ctx 取消在等待中立即传播。
func TestTokenBucketCancel(t *testing.T) {
	b := newTokenBucket(1) // 1/s，等 10 个令牌需 ~10s
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := b.Wait(ctx, 10)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应返回 ctx 取消错误，实际 %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("取消未及时传播：耗时 %v", time.Since(start))
	}
}

// TestBackoffDelaySequence 验证退避序列 base/2/4/8 并封顶 8×base。
func TestBackoffDelaySequence(t *testing.T) {
	p := &backoffPolicy{base: time.Second, maxRetry: 6, sleep: sleepCtx}
	want := []time.Duration{1, 2, 4, 8, 8, 8}
	for attempt := 1; attempt <= 6; attempt++ {
		if got := p.delay(attempt); got != want[attempt-1]*time.Second {
			t.Errorf("第 %d 次退避应为 %v，实际 %v", attempt, want[attempt-1]*time.Second, got)
		}
	}
}

// TestOptionsNormalize 验证参数默认值与范围收敛。
// 注：MaxRetry 零值语义为"不重试"（CLI 经 flag 默认值提供 5）。
func TestOptionsNormalize(t *testing.T) {
	o := Options{}.Normalize()
	if o.Mode != ModeDirect || o.BatchSize != DefaultBatchSize || o.Rate != DefaultRate || o.MaxRetry != 0 {
		t.Errorf("默认值异常: %+v", o)
	}
	clamped := Options{Mode: ModePipeline, BatchSize: 1000, Rate: -1, MaxRetry: -3}.Normalize()
	if clamped.BatchSize != MaxBatchSize || clamped.Rate != DefaultRate || clamped.MaxRetry != 0 {
		t.Errorf("范围收敛异常: %+v", clamped)
	}
	low := Options{BatchSize: 10}.Normalize()
	if low.BatchSize != MinBatchSize {
		t.Errorf("批量下限收敛异常: %+v", low)
	}
}
