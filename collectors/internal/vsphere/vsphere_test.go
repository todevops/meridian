package vsphere

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/mo"

	"collectors/internal/record"
)

// startVCSim 起一个内存 vCenter（vcsim）夹具：默认 VPX 模型含
// 1 个集群（3 台主机）+ 1 台独立主机 + 若干虚拟机（以模型实际数量为准）。
func startVCSim(t *testing.T) (*simulator.Model, *simulator.Server) {
	t.Helper()
	model := simulator.VPX()
	if err := model.Create(); err != nil {
		t.Fatalf("创建 vcsim 模型失败: %v", err)
	}
	t.Cleanup(model.Remove)
	srv := model.Service.NewServer()
	t.Cleanup(srv.Close)
	return model, srv
}

// recordsByModel 把记录按 model_candidate 分组。
func recordsByModel(recs []record.Record) map[string][]record.Record {
	out := map[string][]record.Record{}
	for _, r := range recs {
		out[r.ModelCandidate] = append(out[r.ModelCandidate], r)
	}
	return out
}

func TestCollectAgainstVCSim(t *testing.T) {
	model, srv := startVCSim(t)
	counts := model.Count()

	c, err := New(srv.URL.String(), "user", "pass", true, nil)
	if err != nil {
		t.Fatalf("创建采集器失败: %v", err)
	}
	recs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}

	byModel := recordsByModel(recs)
	clusters, hosts, vms := byModel["esxi_cluster"], byModel["esxi_host"], byModel["virtual_machine"]

	// 三类对象映射齐全（数量与 vcsim 模型一致）
	if len(clusters) != counts.Cluster || len(hosts) != counts.Host || len(vms) != counts.Machine {
		t.Fatalf("记录数量不符: cluster=%d host=%d vm=%d（模型 %d/%d/%d）",
			len(clusters), len(hosts), len(vms), counts.Cluster, counts.Host, counts.Machine)
	}
	for _, r := range recs {
		if r.Source != Source || r.Collector != CollectorName {
			t.Errorf("记录头不符: %+v", r)
		}
		if r.OccurredAt.IsZero() {
			t.Errorf("occurred_at 不应为零值: %+v", r)
		}
	}

	// 集群：name/moid/host_count
	cl := clusters[0].Attributes
	if cl["name"] == "" || cl["moid"] == "" {
		t.Errorf("集群 name/moid 缺失: %+v", cl)
	}
	if cl["host_count"] != 3 {
		t.Errorf("集群 host_count 应为 3: %+v", cl)
	}
	clusterMOID := cl["moid"].(string)

	// 主机：hardware_uuid 主键非空；集群内 3 台带 parent_cluster_moid
	hostUUIDs := map[string]bool{}
	withParent := 0
	for _, h := range hosts {
		uuid, _ := h.Attributes["hardware_uuid"].(string)
		if uuid == "" {
			t.Errorf("主机 hardware_uuid 缺失: %+v", h.Attributes)
		}
		hostUUIDs[uuid] = true
		if h.Attributes["parent_cluster_moid"] == clusterMOID {
			withParent++
		}
	}
	if withParent != 3 {
		t.Errorf("应有 3 台主机携带 parent_cluster_moid=%s: %d", clusterMOID, withParent)
	}

	// 虚拟机：instance_uuid 主键非空；parent_host_uuid 解析为主机 hardware_uuid
	for _, vm := range vms {
		if vm.Attributes["instance_uuid"] == "" {
			t.Errorf("VM instance_uuid 缺失: %+v", vm.Attributes)
		}
		pu, _ := vm.Attributes["parent_host_uuid"].(string)
		if !hostUUIDs[pu] {
			t.Errorf("VM parent_host_uuid 未解析到已采集主机: %+v", vm.Attributes)
		}
		if vm.Attributes["power_state"] == "" {
			t.Errorf("VM power_state 缺失: %+v", vm.Attributes)
		}
	}
}

func TestCollectPoweredOffVMHasNoIP(t *testing.T) {
	model := simulator.VPX()
	if err := model.Create(); err != nil {
		t.Fatalf("创建 vcsim 模型失败: %v", err)
	}
	t.Cleanup(model.Remove)
	srv := model.Service.NewServer()
	t.Cleanup(srv.Close)

	// 关停一台 VM（经 govmomi 对象 API 走 vcsim 任务）
	ctx := context.Background()
	u, _ := url.Parse(srv.URL.String())
	u.User = url.UserPassword("user", "pass")
	cli, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		t.Fatalf("登录 vcsim 失败: %v", err)
	}
	refs := model.Map().All("VirtualMachine")
	target := refs[0].Reference()
	vm := object.NewVirtualMachine(cli.Client, target)
	task, err := vm.PowerOff(ctx)
	if err != nil {
		t.Fatalf("关电失败: %v", err)
	}
	if err := task.Wait(ctx); err != nil {
		t.Fatalf("等待关电任务失败: %v", err)
	}
	var offVM mo.VirtualMachine
	if err := vm.Properties(ctx, target, []string{"name"}, &offVM); err != nil {
		t.Fatalf("读取 VM 名称失败: %v", err)
	}

	c, err := New(srv.URL.String(), "user", "pass", true, nil)
	if err != nil {
		t.Fatalf("创建采集器失败: %v", err)
	}
	recs, err := c.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect 失败: %v", err)
	}

	var found *record.Record
	for i, r := range recs {
		if r.ModelCandidate == "virtual_machine" && r.Attributes["name"] == offVM.Name {
			found = &recs[i]
		}
	}
	if found == nil {
		t.Fatalf("未找到被关电 VM %s 的记录", offVM.Name)
	}
	if found.Attributes["power_state"] != "poweredOff" {
		t.Errorf("power_state 应为 poweredOff: %+v", found.Attributes)
	}
	if _, hasIP := found.Attributes["ip"]; hasIP {
		t.Errorf("关电 VM 不应携带 ip 属性: %+v", found.Attributes)
	}
	// instance_uuid 主键不受关电影响
	if found.Attributes["instance_uuid"] == "" {
		t.Errorf("关电 VM instance_uuid 主键缺失: %+v", found.Attributes)
	}
}

func TestMapVM(t *testing.T) {
	now := time.Now()

	// instance_uuid 主键 + 完整属性
	rec, ok := MapVM(VMInfo{
		Name:         "vm-1",
		InstanceUUID: "uuid-1",
		IP:           "10.0.0.11",
		OS:           "Ubuntu Linux",
		VCPU:         4,
		MemMB:        8192,
		PowerState:   "poweredOn",
	}, "host-uuid-1", now)
	if !ok {
		t.Fatal("有 instance_uuid 应映射成功")
	}
	a := rec.Attributes
	if a["instance_uuid"] != "uuid-1" || a["ip"] != "10.0.0.11" || a["parent_host_uuid"] != "host-uuid-1" {
		t.Errorf("属性映射不符: %+v", a)
	}
	if a["vcpu"] != int32(4) || a["memory_gb"] != int64(8) {
		t.Errorf("规格属性不符: %+v", a)
	}

	// 关电 VM 无 IP/OS：对应属性省略而非空串
	rec, ok = MapVM(VMInfo{Name: "vm-off", InstanceUUID: "uuid-2", PowerState: "poweredOff"}, "", now)
	if !ok {
		t.Fatal("关电 VM 应仍映射成功（instance_uuid 主键在）")
	}
	if _, hasIP := rec.Attributes["ip"]; hasIP {
		t.Errorf("无 IP 不应写 ip 属性: %+v", rec.Attributes)
	}
	if _, hasParent := rec.Attributes["parent_host_uuid"]; hasParent {
		t.Errorf("无所属主机不应写 parent_host_uuid: %+v", rec.Attributes)
	}

	// 无 instance_uuid 无法调和
	if _, ok := MapVM(VMInfo{Name: "vm-bad"}, "", now); ok {
		t.Error("无 instance_uuid 应返回 ok=false")
	}
}

func TestMapHost(t *testing.T) {
	rec, ok := MapHost(HostInfo{
		Name: "esxi-1", HardwareUUID: "hw-1", Model: "PowerEdge R750",
		CPUCores: 64, MemMB: 524288, ParentClusterMOID: "domain-c1",
	}, time.Now())
	if !ok {
		t.Fatal("有 hardware_uuid 应映射成功")
	}
	a := rec.Attributes
	if a["hardware_uuid"] != "hw-1" || a["parent_cluster_moid"] != "domain-c1" || a["cpu_cores"] != int32(64) {
		t.Errorf("属性映射不符: %+v", a)
	}

	if _, ok := MapHost(HostInfo{Name: "esxi-bad"}, time.Now()); ok {
		t.Error("无 hardware_uuid 应返回 ok=false")
	}
}

func TestMapCluster(t *testing.T) {
	rec := MapCluster(ClusterInfo{Name: "DC0_C0", MOID: "domain-c1", HostCount: 3}, time.Now())
	a := rec.Attributes
	if rec.ModelCandidate != "esxi_cluster" || a["moid"] != "domain-c1" || a["host_count"] != 3 {
		t.Errorf("集群映射不符: %+v", rec)
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{":19007", "https://localhost:19007/sdk"},
		{"vcenter.local", "https://vcenter.local/sdk"},
		{"https://vcenter.local:443", "https://vcenter.local:443/sdk"},
		{"http://127.0.0.1:19007/sdk", "http://127.0.0.1:19007/sdk"},
	}
	for _, tc := range cases {
		u, err := NormalizeURL(tc.in, "user", "pass")
		if err != nil {
			t.Fatalf("NormalizeURL(%q) 失败: %v", tc.in, err)
		}
		if u.Scheme+"://"+u.Host+u.Path != tc.want {
			t.Errorf("NormalizeURL(%q) = %s，期望 %s", tc.in, u, tc.want)
		}
		if u.User.Username() != "user" {
			t.Errorf("账密未注入: %s", u.Redacted())
		}
	}
	if _, err := NormalizeURL("", "u", "p"); err == nil {
		t.Error("空 URL 应报错")
	}
}
