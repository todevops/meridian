package aliyun

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockECSArray 是 mock 简化形状：顶层数组。
const mockECSArray = `[
  {
    "InstanceId": "i-bp1abc",
    "InstanceName": "prod-web-01",
    "Status": "Running",
    "InstanceType": "ecs.g7.xlarge",
    "PrivateIpAddress": ["10.20.0.11"],
    "ZoneId": "cn-beijing-h",
    "Tags": {"env": "prod", "app": "gateway"}
  },
  {
    "InstanceId": "i-bp2def",
    "InstanceName": "prod-db-01",
    "Status": "Stopped",
    "InstanceType": "ecs.r7.large",
    "PrivateIpAddress": {"IpAddress": ["10.20.0.21"]},
    "ZoneId": "cn-beijing-h",
    "Tags": {"Tag": [{"TagKey": "env", "TagValue": "prod"}]}
  }
]`

// mockECSOfficial 是官方包装形状：{"Instances":{"Instance":[...]}}。
const mockECSOfficial = `{
  "TotalCount": 1,
  "Instances": {
    "Instance": [
      {
        "InstanceId": "i-bp3ghi",
        "InstanceName": "test-cache-01",
        "Status": "Running",
        "InstanceType": "ecs.c7.large",
        "PrivateIpAddress": "10.30.0.5",
        "ZoneId": "cn-hangzhou-i",
        "Tags": [{"Key": "env", "Value": "test"}]
      }
    ]
  }
}`

func newFixtureServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 采集器会依次拉取 ECS/VPC/RDS/SLB；非 ECS 产品返回空清单。
		if got := r.URL.Query().Get("Action"); got != "DescribeInstances" {
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte(body))
	}))
}

func TestCollectMockArrayShape(t *testing.T) {
	srv := newFixtureServer(t, mockECSArray)
	defer srv.Close()

	recs, err := New(srv.URL).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("应产出 2 条记录: %d", len(recs))
	}

	r := recs[0]
	if r.Source != "aliyun" || r.Collector != "aliyun-cloud-collector" {
		t.Errorf("source/collector 不符: %s/%s", r.Source, r.Collector)
	}
	if r.ModelCandidate != "host" {
		t.Errorf("model_candidate 应为 host: %s", r.ModelCandidate)
	}
	a := r.Attributes
	if a["host_type"] != "cloud" || a["cloud_provider"] != "aliyun" {
		t.Errorf("云属性不符: %v / %v", a["host_type"], a["cloud_provider"])
	}
	if a["cloud_instance_id"] != "i-bp1abc" || a["ident"] != "prod-web-01" {
		t.Errorf("标识字段不符: %v / %v", a["cloud_instance_id"], a["ident"])
	}
	if a["ip"] != "10.20.0.11" || a["spec"] != "ecs.g7.xlarge" || a["zone"] != "cn-beijing-h" || a["status"] != "Running" {
		t.Errorf("映射字段不符: %+v", a)
	}
	// tags 已规范化为字符串
	if a["tags"] != "app=gateway,env=prod" {
		t.Errorf("tags 不符: %+v", a["tags"])
	}

	// 第二条验证官方 IpAddress 包装与 Tag 列表形状
	a2 := recs[1].Attributes
	if a2["ip"] != "10.20.0.21" {
		t.Errorf("IpAddress 包装解析失败: %v", a2["ip"])
	}
	if a2["tags"] != "env=prod" {
		t.Errorf("Tag 列表解析失败: %+v", a2["tags"])
	}
}

func TestCollectOfficialWrappedShape(t *testing.T) {
	srv := newFixtureServer(t, mockECSOfficial)
	defer srv.Close()

	recs, err := New(srv.URL).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("应产出 1 条记录: %d", len(recs))
	}
	a := recs[0].Attributes
	if a["ip"] != "10.30.0.5" || a["cloud_instance_id"] != "i-bp3ghi" {
		t.Errorf("官方包装形状映射失败: %+v", a)
	}
	if a["tags"] != "env=test" {
		t.Errorf("Key/Value 标签解析失败: %+v", a["tags"])
	}
}

func TestCollectHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := New(srv.URL).Collect(context.Background()); err == nil {
		t.Fatal("服务端 500 应返回错误")
	}
}

func TestCollectInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{broken`))
	}))
	defer srv.Close()

	_, err := New(srv.URL).Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "解析") {
		t.Fatalf("非法 JSON 应返回解析错误: %v", err)
	}
}

func TestCollectSkipsInstanceWithoutID(t *testing.T) {
	srv := newFixtureServer(t, `[{"InstanceName":"no-id","PrivateIpAddress":["1.1.1.1"]}]`)
	defer srv.Close()

	recs, err := New(srv.URL).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("无 InstanceId 的实例应被跳过: %d", len(recs))
	}
}

// 按 Action 路由的多产品 fixture 服务器（ECS/VPC/RDS/SLB）。
func newMultiProductServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("Action") {
		case "DescribeInstances":
			w.Write([]byte(`[]`))
		case "DescribeVpcs":
			w.Write([]byte(`{"Vpcs":{"Vpc":[{"VpcId":"vpc-001","VpcName":"prod-vpc","CidrBlock":"10.30.0.0/16","RegionId":"cn-beijing","Status":"Available"}]}}`))
		case "DescribeDBInstances":
			w.Write([]byte(`{"Items":{"DBInstance":[{"DBInstanceId":"rm-001","DBInstanceDescription":"order-mysql","Engine":"MySQL","EngineVersion":"8.0","DBInstanceClass":"mysql.n2.medium.1","RegionId":"cn-beijing","ZoneId":"cn-beijing-a","DBInstanceStatus":"Running"}]}}`))
		case "DescribeLoadBalancers":
			w.Write([]byte(`{"LoadBalancers":{"LoadBalancer":[{"LoadBalancerId":"lb-001","LoadBalancerName":"front-slb","Address":"10.30.0.100","LoadBalancerSpec":"slb.s2.small","RegionId":"cn-beijing","LoadBalancerStatus":"active"}]}}`))
		default:
			t.Errorf("未知 Action: %q", r.URL.Query().Get("Action"))
		}
	}))
}

func TestCollectCloudAssets(t *testing.T) {
	srv := newMultiProductServer(t)
	defer srv.Close()

	recs, err := New(srv.URL).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("应产出 3 条记录（VPC+RDS+SLB）: %d", len(recs))
	}

	vpc := recs[0]
	if vpc.ModelCandidate != "cloud_vpc" {
		t.Errorf("VPC 应映射为 cloud_vpc: %s", vpc.ModelCandidate)
	}
	if a := vpc.Attributes; a["vpc_id"] != "vpc-001" || a["cidr"] != "10.30.0.0/16" || a["cloud_provider"] != "aliyun" {
		t.Errorf("VPC 字段映射不符: %+v", a)
	}

	rds := recs[1]
	if rds.ModelCandidate != "cloud_rds" {
		t.Errorf("RDS 应映射为 cloud_rds: %s", rds.ModelCandidate)
	}
	if a := rds.Attributes; a["db_instance_id"] != "rm-001" || a["engine"] != "MySQL" || a["engine_version"] != "8.0" {
		t.Errorf("RDS 字段映射不符: %+v", a)
	}

	slb := recs[2]
	if slb.ModelCandidate != "cloud_slb" {
		t.Errorf("SLB 应映射为 cloud_slb: %s", slb.ModelCandidate)
	}
	if a := slb.Attributes; a["slb_id"] != "lb-001" || a["vip"] != "10.30.0.100" {
		t.Errorf("SLB 字段映射不符: %+v", a)
	}
}
