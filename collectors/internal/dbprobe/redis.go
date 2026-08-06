// redis 实例直连探测：go-redis INFO server/replication。
package dbprobe

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisClient 抽象 redis 客户端，便于测试/fixture 注入内存实现。
type redisClient interface {
	Info(ctx context.Context, section string) (string, error)
	Close() error
}

// goRedisClient 是生产实现（go-redis）。
type goRedisClient struct{ c *redis.Client }

func (g *goRedisClient) Info(ctx context.Context, section string) (string, error) {
	return g.c.Info(ctx, section).Result()
}

func (g *goRedisClient) Close() error { return g.c.Close() }

// dialRedis 生产拨号：只读账号，超时收紧。
func dialRedis(addr, username, password string) redisClient {
	return &goRedisClient{c: redis.NewClient(&redis.Options{
		Addr:        addr,
		Username:    username,
		Password:    password,
		DialTimeout: 5 * time.Second,
		ReadTimeout: 5 * time.Second,
	})}
}

// probeRedis 直连 redis 实例，补采版本、主库地址与自身角色（master/slave）。
func probeRedis(ctx context.Context, cli redisClient) (version, masterAddr string, selfMaster bool, err error) {
	server, err := cli.Info(ctx, "server")
	if err != nil {
		return "", "", false, fmt.Errorf("INFO server 失败: %w", err)
	}
	repl, err := cli.Info(ctx, "replication")
	if err != nil {
		return "", "", false, fmt.Errorf("INFO replication 失败: %w", err)
	}
	version = parseInfo(server)["redis_version"]
	rk := parseInfo(repl)
	switch rk["role"] {
	case "slave", "replica":
		host, port := rk["master_host"], rk["master_port"]
		if host == "" {
			return version, "", false, nil // 复制断开：保守不报拓扑
		}
		if port == "" {
			port = "6379"
		}
		masterAddr = net.JoinHostPort(host, port)
	case "master":
		selfMaster = rk["connected_slaves"] != "" && rk["connected_slaves"] != "0"
	default:
		return version, "", false, fmt.Errorf("INFO replication 未返回 role 字段")
	}
	return version, masterAddr, selfMaster, nil
}

// parseInfo 解析 INFO 输出为键值表（跳过 # 段标题与空行）。
func parseInfo(s string) map[string]string {
	kv := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return kv
}
