// Command collector 是 CMDB 自研采集器入口：
// 从云厂商 / TSDB / LibreNMS / nmap 网扫等数据源拉取清单，映射为标准发现记录，
// 批量 POST 到 CMDB /api/v1/discovery-records（或 -dry-run 只打印不上报）。
//
// 用法：
//
//	collector -collector=all [-dry-run]
//	collector -collector=aliyun,librenms
//
// 环境变量（默认值指向 mock 平台约定端口）：
//
//	MERIDIAN_API_URL        CMDB API 地址（默认 http://localhost:8080）
//	MERIDIAN_TOKEN          CMDB Bearer 令牌（可选；优先级高于账密登录）
//	MERIDIAN_USERNAME       CMDB 登录用户名（与 MERIDIAN_PASSWORD 配合换 token）
//	MERIDIAN_PASSWORD       CMDB 登录密码
//	ALIYUN_API_URL      阿里云 ECS mock（默认 :19005）
//	VOLC_API_URL        火山 CloudControl mock（默认 :19006）
//	TSDB_API_URL        TSDB mock（默认 :19004）
//	LIBRENMS_API_URL    LibreNMS mock（默认 :19003）
//	LIBRENMS_API_TOKEN  LibreNMS X-Auth-Token（无默认，必填）
//	NMAP_FROM_FILE      nmap -oX 结果文件（设置后 ipscan 不再现扫）
//	NMAP_SCAN_TARGET    ipscan 现扫网段（如 192.168.1.0/24）
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"collectors/internal/aliyun"
	"collectors/internal/dbdiscover"
	"collectors/internal/ipscan"
	"collectors/internal/librenms"
	"collectors/internal/record"
	"collectors/internal/runner"
	"collectors/internal/volc"
)

// allCollectors 是 -collector=all 时的运行顺序。
var allCollectors = []string{"aliyun", "volc", "dbdiscover", "librenms", "ipscan"}

func main() {
	var (
		collectorFlag = flag.String("collector", "all", "采集器：aliyun|volc|dbdiscover|librenms|ipscan|all（支持逗号分隔多选）")
		dryRun        = flag.Bool("dry-run", false, "只打印发现记录，不上报 CMDB、不变更模型")
	)
	flag.Parse()

	cmdbAPI := record.NormalizeBaseURL(record.Getenv("MERIDIAN_API_URL", "http://localhost:8080"))

	// 解析 CMDB 认证令牌（MERIDIAN_TOKEN 或 MERIDIAN_USERNAME+MERIDIAN_PASSWORD 登录换取）。
	authToken, err := record.ResolveCMDBToken(context.Background(), cmdbAPI)
	if err != nil {
		log.Fatalf("解析 CMDB 认证令牌失败: %v", err)
	}

	names := allCollectors
	if *collectorFlag != "all" {
		names = strings.Split(*collectorFlag, ",")
	}
	cols := make([]runner.Collector, 0, len(names))
	for _, n := range names {
		c, err := build(strings.TrimSpace(n), cmdbAPI, authToken, *dryRun)
		if err != nil {
			log.Fatalf("初始化采集器失败: %v", err)
		}
		cols = append(cols, c)
	}

	var sink record.Sink = record.NewHTTPSink(cmdbAPI, authToken)
	if *dryRun {
		sink = record.NewDryRunSink(os.Stdout)
		log.Printf("[dry-run] 仅打印发现记录，不上报 %s", cmdbAPI)
	} else {
		if authToken == "" {
			log.Print("提示：未配置 MERIDIAN_TOKEN 或 MERIDIAN_USERNAME/MERIDIAN_PASSWORD，服务端启用认证时上报将被拒绝")
		}
		log.Printf("发现记录上报地址: %s/api/v1/discovery-records", cmdbAPI)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runner.Run(ctx, cols, sink, log.Printf); err != nil {
		log.Fatalf("采集运行存在失败: %v", err)
	}
	log.Print("全部采集器运行完成")
}

// build 按名称创建采集器，端点从环境变量读取（默认值指向 mock 平台约定端口）。
func build(name, cmdbAPI, authToken string, dryRun bool) (runner.Collector, error) {
	switch name {
	case "aliyun":
		return aliyun.New(record.Getenv("ALIYUN_API_URL", ":19005")), nil
	case "volc":
		return volc.New(record.Getenv("VOLC_API_URL", ":19006")), nil
	case "dbdiscover":
		return dbdiscover.New(record.Getenv("TSDB_API_URL", ":19004"), cmdbAPI, authToken, dryRun, log.Printf), nil
	case "librenms":
		return librenms.New(record.Getenv("LIBRENMS_API_URL", ":19003"), record.Getenv("LIBRENMS_API_TOKEN", "")), nil
	case "ipscan":
		return ipscan.New(record.Getenv("NMAP_FROM_FILE", ""), record.Getenv("NMAP_SCAN_TARGET", "")), nil
	default:
		return nil, fmt.Errorf("未知采集器 %q（可选 aliyun|volc|dbdiscover|librenms|ipscan|all）", name)
	}
}
