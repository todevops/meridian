// Command server 是 CMDB 服务端入口：
// 装配数据库（PG/SQLite 自动选择 + 自动迁移）、HTTP API、
// NATS 发现订阅通道（可降级）与 n9e 数据消费器（按需启动），并支持优雅关闭。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"cmdb/server/internal/auth"
	"cmdb/server/internal/config"
	"cmdb/server/internal/db"
	"cmdb/server/internal/discovery"
	"cmdb/server/internal/httpapi"
	"cmdb/server/internal/n9e"
	"cmdb/server/internal/stream"
)

func main() {
	cfg := config.Load()

	// 数据库：PG_DSN 非空用 PostgreSQL，否则用 SQLite（本地开发）。
	gdb, err := db.Init(cfg.PGDSN, cfg.DBSQLitePath)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	if cfg.PGDSN != "" {
		log.Println("数据库驱动: postgres")
	} else {
		log.Printf("数据库驱动: sqlite（%s）", cfg.DBSQLitePath)
	}

	// 认证服务：JWT + Casbin（策略落业务库），并幂等种子内置角色与账号。
	if cfg.JWTSecret == "" {
		cfg.JWTSecret = "cmdb-dev-secret-change-me"
		log.Println("警告: JWT_SECRET 未配置，使用开发默认密钥（生产环境必须显式配置）")
	}
	authSvc, err := auth.NewService(gdb, cfg.JWTSecret, cfg.TokenTTLHours)
	if err != nil {
		log.Fatalf("初始化认证服务失败: %v", err)
	}
	if err := authSvc.Seed(cfg.AdminInitialPassword, cfg.CollectorInitialPassword); err != nil {
		log.Fatalf("种子认证数据失败: %v", err)
	}

	// 发现摄入管道：HTTP 与 NATS 通道共用。
	pipeline := discovery.NewPipeline(gdb)

	// 后台组件统一生命周期。
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()

	// NATS JetStream 订阅通道：连不上则日志跳过，不影响 HTTP 摄入。
	if sub, err := stream.StartNATSSubscriber(bgCtx, cfg.NATSURL, func(ctx context.Context, payload []byte) {
		result := pipeline.IngestPayload(ctx, payload)
		if result.Rejected > 0 {
			log.Printf("NATS 消息摄入: 接受 %d 条、拒绝 %d 条（%v）", result.Accepted, result.Rejected, result.Errors)
		}
	}); err != nil {
		log.Printf("NATS 不可用，订阅通道跳过（HTTP 摄入不受影响）: %v", err)
	} else {
		defer sub.Close()
	}

	// n9e 消费器：仅当 N9E_API_URL 与 N9E_API_TOKEN 均配置才启动。
	if cfg.N9EAPIURL != "" && cfg.N9EAPIToken != "" {
		consumer := n9e.NewConsumer(n9e.NewClient(cfg.N9EAPIURL, cfg.N9EAPIToken), pipeline, cfg.N9EInterval)
		go consumer.Run(bgCtx)
		log.Printf("n9e 消费器已启动: api=%s 间隔=%s", cfg.N9EAPIURL, cfg.N9EInterval)
	} else {
		log.Println("N9E_API_URL / N9E_API_TOKEN 未配置，n9e 消费器跳过")
	}

	// HTTP 服务。
	r := httpapi.NewRouter(gdb, pipeline, authSvc)
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: r}

	// 捕获 SIGINT/SIGTERM 实现优雅关闭。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("HTTP 服务监听于 %s", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("收到退出信号，开始优雅关闭...")
	bgCancel() // 先停后台组件（NATS/n9e）

	// 最多等待 10 秒完成在途请求。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("优雅关闭失败: %v", err)
	}
	log.Println("服务已退出")
}
