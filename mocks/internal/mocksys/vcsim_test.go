package mocksys

import (
	"context"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// TestVCSimInventoryShape 用真实 govmomi 客户端走 SOAP 登录并校验清单形状：
// 2 集群 / 6 ESXi / 30 VM，其中 6 台关电无 IP、24 台开机且带 10.30.4.x Guest IP。
func TestVCSimInventoryShape(t *testing.T) {
	h, err := newVCSim()
	if err != nil {
		t.Fatalf("构建 vcsim 失败: %v", err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	u, err := url.Parse(ts.URL + "/sdk")
	if err != nil {
		t.Fatalf("解析测试服务地址失败: %v", err)
	}
	u.User = url.UserPassword("user", "pass")

	ctx := context.Background()
	client, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		t.Fatalf("govmomi 登录失败: %v", err)
	}

	m := view.NewManager(client.Client)
	countKind := func(kind string) int {
		v, err := m.CreateContainerView(ctx, client.ServiceContent.RootFolder, []string{kind}, true)
		if err != nil {
			t.Fatalf("创建 %s 视图失败: %v", kind, err)
		}
		defer func() { _ = v.Destroy(ctx) }()
		refs, err := v.Find(ctx, []string{kind}, nil)
		if err != nil {
			t.Fatalf("列举 %s 失败: %v", kind, err)
		}
		return len(refs)
	}
	if n := countKind("ClusterComputeResource"); n != 2 {
		t.Fatalf("集群数量 = %d，期望 2", n)
	}
	if n := countKind("HostSystem"); n != 6 {
		t.Fatalf("ESXi 数量 = %d，期望 6", n)
	}
	if n := countKind("VirtualMachine"); n != 30 {
		t.Fatalf("VM 数量 = %d，期望 30", n)
	}

	// 拉取全部 VM 的电源状态与 Guest IP，校验关电无 IP / 开机有 IP 的分支。
	v, err := m.CreateContainerView(ctx, client.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		t.Fatalf("创建 VM 视图失败: %v", err)
	}
	defer func() { _ = v.Destroy(ctx) }()
	var vms []mo.VirtualMachine
	if err := v.Retrieve(ctx, []string{"VirtualMachine"}, []string{"name", "runtime.powerState", "guest.ipAddress"}, &vms); err != nil {
		t.Fatalf("读取 VM 属性失败: %v", err)
	}
	poweredOff, poweredOnWithIP := 0, 0
	for _, vm := range vms {
		switch vm.Runtime.PowerState {
		case types.VirtualMachinePowerStatePoweredOff:
			poweredOff++
			if vm.Guest != nil && vm.Guest.IpAddress != "" {
				t.Fatalf("关电 VM %s 不应有 Guest IP，实际 %s", vm.Name, vm.Guest.IpAddress)
			}
		case types.VirtualMachinePowerStatePoweredOn:
			if vm.Guest == nil || vm.Guest.IpAddress == "" {
				t.Fatalf("开机 VM %s 应有 Guest IP", vm.Name)
			}
			poweredOnWithIP++
		}
	}
	if poweredOff != 6 || poweredOnWithIP != 24 {
		t.Fatalf("电源分布 = 关电 %d / 开机有 IP %d，期望 6 / 24", poweredOff, poweredOnWithIP)
	}

	// 每台 ESXi 恰好挂载 5 台 VM（宿主机侧引用）。
	hv, err := m.CreateContainerView(ctx, client.ServiceContent.RootFolder, []string{"HostSystem"}, true)
	if err != nil {
		t.Fatalf("创建宿主机视图失败: %v", err)
	}
	defer func() { _ = hv.Destroy(ctx) }()
	var hosts []mo.HostSystem
	if err := hv.Retrieve(ctx, []string{"HostSystem"}, []string{"name", "vm"}, &hosts); err != nil {
		t.Fatalf("读取宿主机属性失败: %v", err)
	}
	for _, host := range hosts {
		if len(host.Vm) != 5 {
			t.Fatalf("宿主机 %s 挂载 VM 数 = %d，期望 5", host.Name, len(host.Vm))
		}
	}

	// 确认属性收集器链路可用（与采集器读取路径一致）。
	if err := property.DefaultCollector(client.Client).RetrieveOne(ctx, client.ServiceContent.RootFolder, []string{"name"}, new(mo.Folder)); err != nil {
		t.Fatalf("属性收集器读取根目录失败: %v", err)
	}
}
