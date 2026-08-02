// Command migrate 是 NetBox → CMDB 一次性迁移工具（方案 13.1 节）。
//
// 环境变量：
//
//	NETBOX_API_URL  NetBox API 地址（必填，如 http://localhost:19092 或 mock :19002）
//	NETBOX_TOKEN    NetBox API Token（必填，非空）
//	MERIDIAN_API_URL    CMDB API 地址（默认 http://localhost:8081）
//	MERIDIAN_TOKEN      CMDB Bearer 令牌（可选；与账号密码二选一）
//	MERIDIAN_USERNAME   CMDB 登录账号（可选，需与 MERIDIAN_PASSWORD 成对；服务端启用认证时使用）
//	MERIDIAN_PASSWORD   CMDB 登录密码（可选）
//	REPORT_PATH     迁移报告输出路径（默认 ./migration-report.json）
//
// 退出码：0 = 迁移跑完（单条失败见报告）；1 = 致命错误（配置缺失/登录失败/模型确保失败/报告写入失败）。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"migrator/internal/cmdb"
	"migrator/internal/migrate"
	"migrator/internal/netbox"
)

func main() {
	netboxURL := os.Getenv("NETBOX_API_URL")
	netboxToken := os.Getenv("NETBOX_TOKEN")
	cmdbURL := getEnv("MERIDIAN_API_URL", "http://localhost:8081")
	reportPath := getEnv("REPORT_PATH", "./migration-report.json")

	if netboxURL == "" || netboxToken == "" {
		log.Fatal("缺少必填环境变量：NETBOX_API_URL 与 NETBOX_TOKEN 均不能为空")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cmClient := cmdb.NewClient(cmdbURL)
	// CMDB 认证可选：直连令牌优先，否则账号密码登录；都未配置则匿名（适用于未启用认证的服务端）。
	if token := os.Getenv("MERIDIAN_TOKEN"); token != "" {
		cmClient.SetToken(token)
	} else if user, pass := os.Getenv("MERIDIAN_USERNAME"), os.Getenv("MERIDIAN_PASSWORD"); user != "" && pass != "" {
		if err := cmClient.Login(ctx, user, pass); err != nil {
			log.Fatalf("CMDB 认证失败: %v", err)
		}
		log.Printf("CMDB 登录成功（账号 %s）", user)
	}

	log.Printf("开始迁移：NetBox=%s → CMDB=%s", netboxURL, cmdbURL)
	m := migrate.New(netbox.NewClient(netboxURL, netboxToken), cmClient)
	report, err := m.Run(ctx, netboxURL, cmdbURL)

	// 无论是否致命错误，只要产生了报告就落盘并打印摘要（保留已完成部分的证据）。
	if report != nil {
		if werr := report.WriteJSON(reportPath); werr != nil {
			log.Printf("写入迁移报告失败: %v", werr)
			if err == nil {
				err = werr
			}
		} else {
			fmt.Print(report.Summary())
			fmt.Printf("报告已写入 %s\n", reportPath)
		}
	}
	if err != nil {
		log.Fatalf("迁移未完成: %v", err)
	}
	if ctx.Err() != nil {
		log.Fatal("迁移被中断")
	}
}

// getEnv 读取环境变量，未设置时返回默认值。
func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
