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

	"meridian/server/internal/auditrules"
	"meridian/server/internal/auth"
	"meridian/server/internal/config"
	"meridian/server/internal/credentials"
	"meridian/server/internal/db"
	"meridian/server/internal/discovery"
	"meridian/server/internal/httpapi"
	"meridian/server/internal/jssync"
	"meridian/server/internal/n9e"
	"meridian/server/internal/scheduler"
	"meridian/server/internal/stream"
	"meridian/server/internal/umodelgen"
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

	// 凭据主密钥（F-005）：CMDB_MASTER_KEY 缺省时用内置开发键并大字告警。
	credCipher, usingDefaultKey, err := credentials.LoadCipher()
	if err != nil {
		log.Fatalf("初始化凭据加解密失败: %v", err)
	}
	if usingDefaultKey {
		log.Println("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
		log.Println("!! 警告: CMDB_MASTER_KEY 未配置，凭据使用内置开发密钥加密    !!")
		log.Println("!! 生产环境必须显式配置 CMDB_MASTER_KEY 并妥善保管           !!")
		log.Println("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
	}

	// 采集任务调度器（F-033）：10 秒扫描到期任务，任务级互斥防重入。
	sched := scheduler.New(gdb, pipeline, credCipher, cfg.ExecAllowedDir, cfg.ExecTimeout)

	// 后台组件统一生命周期。
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	go sched.Run(bgCtx)

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

	// n9e 消费器：仅当 N9E_API_URL 与 N9E_API_TOKEN 均配置才启动；
	// 客户端同时供质量看板反向监控指标与退役联动摘除 target 使用。
	var n9eClient *n9e.Client
	if cfg.N9EAPIURL != "" && cfg.N9EAPIToken != "" {
		n9eClient = n9e.NewClient(cfg.N9EAPIURL, cfg.N9EAPIToken)
		consumer := n9e.NewConsumer(n9eClient, pipeline, cfg.N9EInterval)
		go consumer.Run(bgCtx)
		log.Printf("n9e 消费器已启动: api=%s 间隔=%s", cfg.N9EAPIURL, cfg.N9EInterval)
	} else {
		log.Println("N9E_API_URL / N9E_API_TOKEN 未配置，n9e 消费器跳过")
	}

	// 稽核规则引擎（F-081）：内置 6 条规则幂等种子 + 每日定时执行。
	if err := auditrules.SeedBuiltin(gdb); err != nil {
		log.Fatalf("种子内置稽核规则失败: %v", err)
	}
	go auditrules.NewEngine(gdb).RunDailyLoop(bgCtx)

	// UModel 生成器（F-073）：订阅调和 PostHook 事件式 upsert + 每日全量对账。
	umodelGen := umodelgen.NewFromEnv(gdb)
	pipeline.Engine().AddPostHook(umodelGen.Handle)
	go umodelGen.RunDailyLoop(bgCtx)

	// JumpServer 资产同步（F-071）：每日兜底对账（事件式同步由本服务内
	// POST /api/v1/integrations/jumpserver/sync 触发；未配置时每日通道记日志跳过）。
	go jssync.RunDailyLoop(bgCtx, gdb, credCipher)

	// HTTP 服务。
	r := httpapi.NewRouter(gdb, pipeline, authSvc, credCipher, sched, n9eClient, umodelGen)
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
