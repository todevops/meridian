// 令牌桶限速与指数退避（pipeline 模式上报用）。
// 均为小体积自实现，不引入第三方依赖；时间源与睡眠函数可注入，便于单测。
package migrate

import (
	"context"
	"sync"
	"time"
)

// tokenBucket 是并发安全的令牌桶：按 rate 个/秒匀速补充，容量为 burst（默认等于 rate，至少 1）。
// 桶初始为空——吞吐节奏从首个令牌起算，保证小速率下的计时可预期。
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64 // 令牌补充速率（个/秒）
	burst  float64 // 桶容量
	tokens float64
	last   time.Time
	now    func() time.Time // 可注入（测试用），默认 time.Now
}

// newTokenBucket 创建令牌桶；rate <= 0 时按 1 个/秒兜底（调用方已做默认值收敛，这里防御）。
func newTokenBucket(rate float64) *tokenBucket {
	if rate <= 0 {
		rate = 1
	}
	burst := rate
	if burst < 1 {
		burst = 1
	}
	return &tokenBucket{rate: rate, burst: burst, last: time.Now(), now: time.Now}
}

// Wait 阻塞直到积攒 n 个令牌并一次性取走；n 大于桶容量时按容量分块逐块等待。
// ctx 取消时立即返回 ctx.Err()。
func (b *tokenBucket) Wait(ctx context.Context, n int) error {
	for n > 0 {
		chunk := n
		if chunk > int(b.burst) {
			chunk = int(b.burst)
		}
		if err := b.waitChunk(ctx, chunk); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

// waitChunk 等待并取走 chunk 个令牌（chunk 不超过桶容量）。
func (b *tokenBucket) waitChunk(ctx context.Context, chunk int) error {
	for {
		b.mu.Lock()
		now := b.now()
		b.tokens += now.Sub(b.last).Seconds() * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
		if b.tokens >= float64(chunk) {
			b.tokens -= float64(chunk)
			b.mu.Unlock()
			return nil
		}
		deficit := float64(chunk) - b.tokens
		b.mu.Unlock()

		wait := time.Duration(deficit / b.rate * float64(time.Second))
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// backoffPolicy 实现 429/5xx 的指数退避重试：
// 退避序列为 base、2×base、4×base，之后封顶于 8×base（对应生产参数 1s/2s/4s/8s），
// 最多重试 maxRetry 次。sleep 可注入（测试用）。
type backoffPolicy struct {
	base     time.Duration
	maxRetry int
	sleep    func(ctx context.Context, d time.Duration) error
}

// newBackoffPolicy 创建生产退避策略（base 1s）。
func newBackoffPolicy(maxRetry int) *backoffPolicy {
	return &backoffPolicy{base: time.Second, maxRetry: maxRetry, sleep: sleepCtx}
}

// delay 返回第 attempt 次重试（从 1 起）前的退避时长：base×2^(attempt-1)，封顶 8×base。
func (p *backoffPolicy) delay(attempt int) time.Duration {
	d := p.base
	for i := 1; i < attempt && d < 8*p.base; i++ {
		d *= 2
	}
	return d
}

// sleepCtx 睡眠 d 时长，ctx 取消时提前返回其错误。
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
