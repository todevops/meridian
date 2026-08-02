// Package stream 实现 NATS JetStream 订阅通道：
// NATS_URL 可连接时订阅 discovery.records 主题，消息走同一发现摄入管道；
// 连接失败时返回错误由调用方降级（日志跳过），不影响 HTTP 摄入。
package stream

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

// DiscoverySubject 是发现记录的主题名。
const DiscoverySubject = "discovery.records"

// PayloadHandler 处理一条消息负载。
type PayloadHandler func(ctx context.Context, payload []byte)

// Subscriber 是 NATS 订阅通道。
type Subscriber struct {
	nc  *nats.Conn
	sub *nats.Subscription
}

// StartNATSSubscriber 连接 NATS 并订阅发现记录主题。
// 优先建立 JetStream 持久消费（durable），失败则回退为核心 NATS 订阅；
// 连接不可达时返回错误，调用方应记录日志并跳过（不阻断服务启动）。
func StartNATSSubscriber(ctx context.Context, url string, handler PayloadHandler) (*Subscriber, error) {
	if url == "" {
		return nil, fmt.Errorf("NATS_URL 未配置")
	}
	nc, err := nats.Connect(url,
		nats.Name("cmdb-discovery-consumer"),
		nats.Timeout(3*time.Second),
		nats.RetryOnFailedConnect(false),
	)
	if err != nil {
		return nil, fmt.Errorf("连接 NATS 失败: %w", err)
	}

	s := &Subscriber{nc: nc}
	cb := func(msg *nats.Msg) {
		handler(ctx, msg.Data)
		// JetStream 消息需要显式 ack；核心 NATS 消息上 Ack 为空操作。
		_ = msg.Ack()
	}

	// 优先 JetStream 持久订阅（队列组 + durable，支持多副本与堆积追平）。
	js, jsErr := nc.JetStream()
	if jsErr == nil {
		sub, err := js.QueueSubscribe(DiscoverySubject, "cmdb-workers", cb,
			nats.Durable("cmdb-discovery"),
			nats.ManualAck(),
			nats.AckWait(30*time.Second),
		)
		if err == nil {
			s.sub = sub
			log.Printf("NATS JetStream 订阅已建立: subject=%s durable=cmdb-discovery", DiscoverySubject)
			return s, nil
		}
		log.Printf("JetStream 订阅失败（%v），回退为核心 NATS 订阅", err)
	}

	// 回退：核心 NATS 队列订阅。
	sub, err := nc.QueueSubscribe(DiscoverySubject, "cmdb-workers", cb)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("订阅主题 %s 失败: %w", DiscoverySubject, err)
	}
	s.sub = sub
	log.Printf("NATS 核心订阅已建立: subject=%s", DiscoverySubject)
	return s, nil
}

// Close 退订并关闭连接。
func (s *Subscriber) Close() {
	if s.sub != nil {
		_ = s.sub.Unsubscribe()
	}
	if s.nc != nil {
		s.nc.Close()
	}
}
