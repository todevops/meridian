// n9e 数据消费器：每 15 分钟（可配）全量拉取 targets 并走摄入管道。
package n9e

import (
	"context"
	"log"
	"time"

	"cmdb/server/internal/discovery"
	"cmdb/server/internal/reconcile"
)

// DefaultInterval 是默认拉取间隔（15 分钟，与方案文档建议一致）。
const DefaultInterval = 15 * time.Minute

// Consumer 是 n9e 数据消费器。
type Consumer struct {
	client   *Client
	pipeline *discovery.Pipeline
	interval time.Duration
}

// NewConsumer 创建消费器。interval <= 0 时使用默认间隔。
func NewConsumer(client *Client, pipeline *discovery.Pipeline, interval time.Duration) *Consumer {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Consumer{client: client, pipeline: pipeline, interval: interval}
}

// RunOnce 执行一轮拉取与摄入，返回发现的记录数（供测试与日志使用）。
func (c *Consumer) RunOnce(ctx context.Context) (int, error) {
	targets, err := c.client.ListTargets(ctx)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	records := make([]reconcile.Record, 0, len(targets))
	for _, t := range targets {
		if t.Ident == "" {
			continue // 无标识的 target 无法调和，跳过
		}
		records = append(records, MapTarget(t, now))
	}
	result := c.pipeline.Ingest(ctx, records)
	log.Printf("n9e 消费完成: 拉取 %d 个 target，摄入接受 %d 条、拒绝 %d 条",
		len(targets), result.Accepted, result.Rejected)
	return result.Accepted, nil
}

// Run 启动定时消费循环：立即执行一轮，之后按间隔执行，直到 ctx 取消。
func (c *Consumer) Run(ctx context.Context) {
	if _, err := c.RunOnce(ctx); err != nil {
		log.Printf("n9e 首轮消费失败: %v", err)
	}
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("n9e 消费器已停止")
			return
		case <-ticker.C:
			if _, err := c.RunOnce(ctx); err != nil {
				log.Printf("n9e 消费失败: %v", err)
			}
		}
	}
}
