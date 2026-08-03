package volc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockListResources 是 CloudControl ListResources 夹具：
// 一条 ECS（Configuration 为字符串内嵌 JSON）、一条 VKE、一条暂不建模的 VPC。
const mockListResources = `{
  "ResponseMetadata": {"RequestId": "req-1"},
  "Result": {
    "TotalCount": 3,
    "Resources": [
      {
        "ResourceType": "ECS",
        "ResourceId": "i-volc-001",
        "Configuration": "{\"InstanceName\":\"prod-app-01\",\"PrivateIpAddress\":\"10.50.0.8\",\"InstanceType\":\"ecs.g3i.xlarge\",\"ZoneId\":\"cn-beijing-a\",\"Status\":\"RUNNING\"}",
        "Tags": [{"Key": "env", "Value": "prod"}]
      },
      {
        "ResourceType": "VKE",
        "ResourceId": "cluster-vke-01",
        "Configuration": {"ClusterName": "prod-k8s", "Status": "Running"},
        "Tags": {"env": "prod"}
      },
      {
        "ResourceType": "VPC",
        "ResourceId": "vpc-001",
        "Configuration": {"VpcName": "prod-vpc"},
        "Tags": []
      }
    ]
  }
}`

func TestCollectMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("ListResources 应为 POST: %s", r.Method)
		}
		if r.URL.Query().Get("Action") != "ListResources" {
			t.Errorf("Action 不符: %q", r.URL.RawQuery)
		}
		w.Write([]byte(mockListResources))
	}))
	defer srv.Close()

	recs, err := New(srv.URL).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("应产出 3 条记录（ECS+VKE+VPC）: %d", len(recs))
	}

	host := recs[0]
	if host.ModelCandidate != "host" {
		t.Errorf("ECS 应映射为 host: %s", host.ModelCandidate)
	}
	a := host.Attributes
	if a["cloud_provider"] != "volc" || a["host_type"] != "cloud" {
		t.Errorf("云属性不符: %+v", a)
	}
	if a["cloud_instance_id"] != "i-volc-001" || a["ident"] != "prod-app-01" || a["ip"] != "10.50.0.8" {
		t.Errorf("ECS 字段映射不符: %+v", a)
	}
	if a["spec"] != "ecs.g3i.xlarge" || a["zone"] != "cn-beijing-a" || a["status"] != "RUNNING" {
		t.Errorf("ECS 规格字段不符: %+v", a)
	}
	if a["tags"] != "env=prod" {
		t.Errorf("ECS tags 不符: %+v", a["tags"])
	}

	vke := recs[1]
	if vke.ModelCandidate != "k8s_workload" {
		t.Errorf("VKE 应映射为 k8s_workload: %s", vke.ModelCandidate)
	}
	va := vke.Attributes
	if va["cluster"] != "prod-k8s" || va["cloud_provider"] != "volc" {
		t.Errorf("VKE 占位记录注记不符: %+v", va)
	}
	if note, _ := va["note"].(string); !strings.Contains(note, "占位") {
		t.Errorf("VKE 应有占位注记: %v", va["note"])
	}

	vpc := recs[2]
	if vpc.ModelCandidate != "cloud_vpc" {
		t.Errorf("VPC 应映射为 cloud_vpc: %s", vpc.ModelCandidate)
	}
	pa := vpc.Attributes
	if pa["vpc_id"] != "vpc-001" || pa["name"] != "prod-vpc" || pa["cloud_provider"] != "volc" {
		t.Errorf("VPC 字段映射不符: %+v", pa)
	}
}

func TestCollectConfigurationAsObject(t *testing.T) {
	// mock 可能直接给对象形式的 Configuration
	body := `{"ResponseMetadata":{},"Result":{"TotalCount":1,"Resources":[{"ResourceType":"ECS","ResourceId":"i-obj","Configuration":{"InstanceName":"obj-form","PrivateIpAddress":"10.0.0.9"},"Tags":{"team":"infra"}}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	recs, err := New(srv.URL).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if len(recs) != 1 || recs[0].Attributes["ip"] != "10.0.0.9" || recs[0].Attributes["ident"] != "obj-form" {
		t.Fatalf("对象形式 Configuration 映射失败: %+v", recs)
	}
	if recs[0].Attributes["tags"] != "team=infra" {
		t.Errorf("字典形式 Tags 解析失败: %+v", recs[0].Attributes["tags"])
	}
}

func TestCollectAPIError(t *testing.T) {
	body := `{"ResponseMetadata":{"Error":{"Code":"InvalidAction","Message":"boom"}},"Result":{}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	_, err := New(srv.URL).Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("ResponseMetadata.Error 应返回错误: %v", err)
	}
}

func TestCollectHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	if _, err := New(srv.URL).Collect(context.Background()); err == nil {
		t.Fatal("HTTP 错误应返回")
	}
}

func TestUnmarshalInvalidConfiguration(t *testing.T) {
	// Configuration 是非法内嵌 JSON 时不应 panic，按空配置处理
	var r Resource
	err := json.Unmarshal([]byte(`{"ResourceType":"ECS","ResourceId":"i-x","Configuration":"{broken"}`), &r)
	if err != nil {
		t.Fatalf("非法 Configuration 不应导致整体解码失败: %v", err)
	}
	if r.Configuration != nil {
		t.Errorf("非法 Configuration 应解析为 nil: %+v", r.Configuration)
	}
}
