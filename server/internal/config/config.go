// Package config 从环境变量加载服务端配置。
package config

import (
	"os"
	"strconv"
	"time"
)

// Config 保存服务端运行所需的全部配置项。
type Config struct {
	// HTTPAddr 为 HTTP 服务监听地址，默认 :8080，可用 HTTP_ADDR 覆盖。
	HTTPAddr string
	// PGDSN 为 PostgreSQL 连接串（PG_DSN）；非空时使用 PostgreSQL。
	PGDSN string
	// DBSQLitePath 为 SQLite 文件路径（DB_SQLITE_PATH），默认 ./cmdb-dev.db；
	// 仅在 PG_DSN 为空时生效（本地开发验证）。
	DBSQLitePath string
	// RedisAddr 为 Redis 地址（REDIS_ADDR），默认 localhost:6379。
	RedisAddr string
	// NATSURL 为 NATS 连接地址（NATS_URL），默认 nats://localhost:4222；
	// 连接不可达时订阅通道自动跳过，不影响 HTTP 摄入。
	NATSURL string
	// N9EAPIURL 为 n9e-webapi 地址（N9E_API_URL）。
	N9EAPIURL string
	// N9EAPIToken 为 n9e API 访问令牌（N9E_API_TOKEN）。
	N9EAPIToken string
	// N9EInterval 为 n9e 拉取间隔（N9E_INTERVAL_SECONDS 秒），默认 15 分钟。
	N9EInterval time.Duration
	// JWTSecret 为会话令牌签名密钥（JWT_SECRET）；缺省为固定开发值并打印警告，生产必须显式配置。
	JWTSecret string
	// TokenTTLHours 为会话令牌有效期（TOKEN_TTL_HOURS 小时），默认 24。
	TokenTTLHours int
	// AdminInitialPassword 为内置 admin 账号初始密码（ADMIN_INITIAL_PASSWORD），默认 admin123；
	// 仅在首次启动种子 admin 时生效。
	AdminInitialPassword string
	// CollectorInitialPassword 为内置 collector 采集服务账号初始密码
	// （COLLECTOR_INITIAL_PASSWORD），默认 collector123；仅在首次种子时生效。
	CollectorInitialPassword string
}

// Load 读取环境变量并填充默认值。
func Load() Config {
	return Config{
		HTTPAddr:                 getEnv("HTTP_ADDR", ":8080"),
		PGDSN:                    os.Getenv("PG_DSN"),
		DBSQLitePath:             getEnv("DB_SQLITE_PATH", "./cmdb-dev.db"),
		RedisAddr:                getEnv("REDIS_ADDR", "localhost:6379"),
		NATSURL:                  getEnv("NATS_URL", "nats://localhost:4222"),
		N9EAPIURL:                os.Getenv("N9E_API_URL"),
		N9EAPIToken:              os.Getenv("N9E_API_TOKEN"),
		N9EInterval:              getDuration("N9E_INTERVAL_SECONDS", 15*time.Minute),
		JWTSecret:                getEnv("JWT_SECRET", ""),
		TokenTTLHours:            getInt("TOKEN_TTL_HOURS", 24),
		AdminInitialPassword:     getEnv("ADMIN_INITIAL_PASSWORD", "admin123"),
		CollectorInitialPassword: getEnv("COLLECTOR_INITIAL_PASSWORD", "collector123"),
	}
}

// getEnv 读取环境变量，未设置时返回默认值。
func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getDuration 读取秒数形式的环境变量并转为时长，未设置或非法时返回默认值。
func getDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return def
}

// getInt 读取整数环境变量，未设置或非法时返回默认值。
func getInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
