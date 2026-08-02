// n9e 客户端、字段映射与消费器单测（httptest mock server，不依赖真实 n9e）。
package n9e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"cmdb/server/internal/discovery"
	"cmdb/server/internal/store"
)

// targetsFixture 是模拟 n9e targets API 的响应体。
const targetsFixture = `{
  "dat": {
    "list": [
      {
        "id": 1, "ident": "web-01", "host_ip": "10.0.0.1",
        "os": "Rocky Linux 9.4", "cpu_num": 8, "arch": "x86_64",
        "agent_version": "v0.3.81", "update_at": 1735689600,
        "group_name": "电商业务组", "tags": "env=prod role=web", "host_tags": ["idc=bj1", "env=prod"]
      },
      {
        "id": 2, "ident": "db-01", "host_ip": "10.0.0.2",
        "os": "Ubuntu 22.04", "cpu_num": 16, "arch": "x86_64",
        "agent_version": "v0.3.81", "update_at": 1735689600,
        "group_name": "电商业务组", "tags": "", "host_tags": ""
      }
    ],
    "total": 2
  },
  "err": ""
}`

// newMockServer 启动记录请求头的 mock n9e 服务。
func newMockServer(t *testing.T, status int, body string, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/n9e/targets" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestListTargets(t *testing.T) {
	var gotAuth string
	srv := newMockServer(t, http.StatusOK, targetsFixture, &gotAuth)
	defer srv.Close()

	client := NewClient(srv.URL, "test-token")
	targets, err := client.ListTargets(context.Background())
	if err != nil {
		t.Fatalf("拉取 targets 失败: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("鉴权头不符: %q", gotAuth)
	}
	if len(targets) != 2 {
		t.Fatalf("期望 2 个 target，实际 %d", len(targets))
	}
	if targets[0].Ident != "web-01" || targets[0].HostIP != "10.0.0.1" || targets[0].CPUNum != 8 {
		t.Fatalf("target 解析不符: %+v", targets[0])
	}
}

func TestListTargetsError(t *testing.T) {
	var gotAuth string
	// 非 200 状态码。
	srv := newMockServer(t, http.StatusInternalServerError, `{}`, &gotAuth)
	defer srv.Close()
	if _, err := NewClient(srv.URL, "t").ListTargets(context.Background()); err == nil {
		t.Fatal("状态码 500 应返回错误")
	}

	// 业务错误字段。
	srv2 := newMockServer(t, http.StatusOK, `{"dat": {}, "err": "token invalid"}`, &gotAuth)
	defer srv2.Close()
	if _, err := NewClient(srv2.URL, "t").ListTargets(context.Background()); err == nil {
		t.Fatal("err 字段非空应返回错误")
	}
}

func TestMapTarget(t *testing.T) {
	target := Target{
		Ident: "web-01", HostIP: "10.0.0.1", OS: "Rocky Linux 9.4",
		CPUNum: 8, Arch: "x86_64", AgentVersion: "v0.3.81",
		UpdateAt: 1735689600, GroupName: "电商业务组",
		Tags: "env=prod role=web", HostTags: "idc=bj1 env=prod",
	}
	rec := MapTarget(target, time.Now())

	if rec.Source != "n9e" || rec.Collector != "n9e-target-puller" || rec.ModelCandidate != "host" {
		t.Fatalf("记录来源标识不符: %+v", rec)
	}
	a := rec.Attributes
	if a["ident"] != "web-01" || a["ip"] != "10.0.0.1" || a["os"] != "Rocky Linux 9.4" {
		t.Fatalf("基础字段映射不符: %v", a)
	}
	if a["cpu_num"] != 8 || a["arch"] != "x86_64" || a["agent_version"] != "v0.3.81" {
		t.Fatalf("规格字段映射不符: %v", a)
	}
	// UpdateAt(1735689600) = 2025-01-01T00:00:00Z。
	if a["last_seen_at"] != "2025-01-01T00:00:00Z" {
		t.Fatalf("last_seen_at 映射不符: %v", a["last_seen_at"])
	}
	if a["biz_group"] != "电商业务组" {
		t.Fatalf("biz_group 映射不符: %v", a["biz_group"])
	}
	// tags 合并去重为空格分隔字符串：env=prod 只出现一次。
	if a["tags"] != "env=prod role=web idc=bj1" {
		t.Fatalf("tags 合并去重不符: %v", a["tags"])
	}
}

// TestConsumerRunOnce 验证消费器端到端摄入：mock n9e → 映射 → 管道 → CI 建档。
func TestConsumerRunOnce(t *testing.T) {
	var gotAuth string
	srv := newMockServer(t, http.StatusOK, targetsFixture, &gotAuth)
	defer srv.Close()

	// 内存库 + 主机模型（调和键 ident/ip）。
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	model := store.Model{
		Name: "主机", Code: "host",
		Attributes: datatypes.NewJSONType([]store.AttributeDefinition{
			{Name: "主机标识", Code: "ident", Type: "string"},
			{Name: "内网IP", Code: "ip", Type: "ip"},
			{Name: "操作系统", Code: "os", Type: "string"},
		}),
		ReconcileKeys: datatypes.NewJSONType([]string{"ident", "ip"}),
	}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}

	consumer := NewConsumer(NewClient(srv.URL, "test-token"), discovery.NewPipeline(db), time.Minute)
	accepted, err := consumer.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("消费失败: %v", err)
	}
	if accepted != 2 {
		t.Fatalf("期望接受 2 条，实际 %d", accepted)
	}
	var ciCount int64
	db.Model(&store.CI{}).Where("model_id = ?", model.ID).Count(&ciCount)
	if ciCount != 2 {
		t.Fatalf("期望建档 2 个 CI，实际 %d", ciCount)
	}

	// 再跑一轮：同数据应为 update/无变更，不产生重复 CI。
	if _, err := consumer.RunOnce(context.Background()); err != nil {
		t.Fatalf("二轮消费失败: %v", err)
	}
	db.Model(&store.CI{}).Where("model_id = ?", model.ID).Count(&ciCount)
	if ciCount != 2 {
		t.Fatalf("重复消费不应产生重复 CI，实际 %d", ciCount)
	}
}
