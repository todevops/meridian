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
//	K8S_API_URL         K8s apiserver（默认 :19009）
//	K8S_TOKEN           K8s Bearer Token（默认 dev-k8s-token）
//	K8S_CLUSTER_NAME    集群名（默认 volc-prod-k8s）
//	K8S_INSECURE        跳过 TLS 证书校验（默认 true）
//	K8S_KUBECONFIG      kubeconfig 文件路径（设置后优先于 url+token）
//	NMAP_FROM_FILE      nmap -oX 结果文件（设置后 ipscan 不再现扫）
//	NMAP_SCAN_TARGET    ipscan 现扫网段（如 192.168.1.0/24）
//	DBPROBE_CRED_FILE     dbprobe 本地凭据文件（JSON，0600；必填，不在 all 内）
//	DBPROBE_FIXTURE_FILE  dbprobe fixture 应答文件（设置后不发起真实数据库连接）
//	VSPHERE_URL         vCenter SDK（默认 :19007，简写自动补 https:// 与 /sdk）
//	VSPHERE_USERNAME    vCenter 用户名（vcsim 默认 user）
//	VSPHERE_PASSWORD    vCenter 密码（vcsim 默认 pass）
//	VSPHERE_INSECURE    跳过 TLS 证书校验（默认 true）
//
// 成功上报后于 stdout 末行打印 CMDB_PRODUCED=<总条数>（2A 任务调度器据此统计产出，
// dry-run 同样打印便于联调）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"collectors/internal/aliyun"
	"collectors/internal/dbdiscover"
	"collectors/internal/dbprobe"
	"collectors/internal/ipscan"
	"collectors/internal/k8s"
	"collectors/internal/librenms"
	"collectors/internal/record"
	"collectors/internal/runner"
	"collectors/internal/volc"
	"collectors/internal/vsphere"
)

// allCollectors 是 -collector=all 时的运行顺序。
var allCollectors = []string{"aliyun", "volc", "dbdiscover", "librenms", "ipscan", "vsphere", "k8s"}

func main() {
	var (
		collectorFlag = flag.String("collector", "all", "采集器：aliyun|volc|dbdiscover|librenms|ipscan|vsphere|k8s|dbprobe|all（支持逗号分隔多选；dbprobe 需显式指定，不在 all 内）")
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

	if err := runner.Run(ctx, cols, sink, log.Printf, os.Stdout); err != nil {
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
		return ipscan.New(record.Getenv("NMAP_FROM_FILE", ""), record.Getenv("NMAP_SCAN_TARGET", ""), cmdbAPI, authToken, log.Printf), nil
	case "vsphere":
		insecure := true // 默认跳过证书校验（对接 vcsim/自签名 vCenter）
		if v := strings.TrimSpace(os.Getenv("VSPHERE_INSECURE")); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("VSPHERE_INSECURE 取值非法 %q: %w", v, err)
			}
			insecure = b
		}
		return vsphere.New(record.Getenv("VSPHERE_URL", ":19007"),
			record.Getenv("VSPHERE_USERNAME", ""), record.Getenv("VSPHERE_PASSWORD", ""), insecure, log.Printf)
	case "k8s":
		insecure := true // 默认跳过证书校验（对接 fake apiserver/自签名集群）
		if v := strings.TrimSpace(os.Getenv("K8S_INSECURE")); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("K8S_INSECURE 取值非法 %q: %w", v, err)
			}
			insecure = b
		}
		return k8s.New(record.Getenv("K8S_API_URL", ":19009"),
			record.Getenv("K8S_TOKEN", "dev-k8s-token"),
			record.Getenv("K8S_CLUSTER_NAME", "volc-prod-k8s"),
			record.Getenv("K8S_KUBECONFIG", ""), insecure)
	case "dbprobe":
		credFile := record.Getenv("DBPROBE_CRED_FILE", "")
		if credFile == "" {
			return nil, fmt.Errorf("dbprobe 需要 DBPROBE_CRED_FILE（本地凭据文件，0600）")
		}
		c := dbprobe.New(cmdbAPI, authToken, credFile, dryRun, log.Printf)
		if fx := record.Getenv("DBPROBE_FIXTURE_FILE", ""); fx != "" {
			store, err := dbprobe.LoadFixture(fx)
			if err != nil {
				return nil, fmt.Errorf("加载 dbprobe fixture 失败: %w", err)
			}
			c.UseFixture(store)
			log.Printf("dbprobe fixture 模式：%s（不发起真实数据库连接）", fx)
		}
		return c, nil
	default:
		return nil, fmt.Errorf("未知采集器 %q（可选 aliyun|volc|dbdiscover|librenms|ipscan|vsphere|k8s|dbprobe|all）", name)
	}
}
