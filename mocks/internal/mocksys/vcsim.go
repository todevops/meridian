package mocksys

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
)

// newVCSim 构建 vCenter 模拟器（:19007）：基于 govmomi/simulator 的进程内 vcsim。
// 定制模型：1 个数据中心 / 2 个集群 / 每集群 3 台 ESXi / 每台 ESXi 5 台 VM（共 30 台），
// 每台宿主机的第 5 台 VM 关电且无 Guest IP（模拟真实 vCenter 中关电 VM 的采集盲区），
// 其余 VM 分配 10.30.4.0/24 网段的静态 Guest IP。
// 鉴权与官方 vcsim 一致：任意凭据均可登录，约定使用 user:pass。
// SOAP SDK 端点为 /sdk（裸请求返回 500 + SOAP Fault，属预期行为，证明端点在线）。
func newVCSim() (http.Handler, error) {
	model := simulator.VPX()
	model.Datacenter = 1
	model.Host = 0 // 不要 VPX 默认的独立宿主机，只保留集群内 ESXi
	model.Cluster = 2
	model.ClusterHost = 3
	// simulator 对集群的 Machine 语义是「每集群 VM 数」（随机分布到集群内宿主机），
	// 15 = 每集群 3 台 ESXi × 每台 5 台 VM，随后由 shapeVMs 确定性归位。
	model.Machine = 15
	model.Autostart = true
	if err := model.Create(); err != nil {
		return nil, fmt.Errorf("创建 vcsim 清单模型失败: %w", err)
	}

	if err := shapeVMs(model.Service.Context); err != nil {
		return nil, fmt.Errorf("整形 vcsim VM 分布失败: %w", err)
	}

	svc := model.Service
	// NewServer 会设置 svc.Listen（SessionManager.Login 鉴权时解引用），
	// 此处复用 mockd 统一监听框架不另起端口，需手动补齐：
	// User 置为官方默认凭据 user:pass 时，任意非空凭据均可登录（与官方 vcsim 行为一致）。
	svc.Listen = &url.URL{Scheme: "http", Host: "127.0.0.1:19007", Path: "/sdk", User: simulator.DefaultLogin}
	// 与 simulator.NewServer 等价的 SOAP 端点注册（此处复用 mockd 统一监听框架，不另起端口）。
	svc.RegisterSDK(svc.Context.Map, svc.Context.Map.Path+"/vimService")
	svc.ServeMux.HandleFunc(svc.Context.Map.Path+"/vimServiceVersions.xml", svc.ServiceVersions)
	svc.ServeMux.HandleFunc("/about", svc.About)
	return svc.ServeMux, nil
}

// shapeVMs 对模型生成的 30 台 VM 做确定性整形：
// simulator 把集群 VM 随机挂到集群内宿主机，这里按名称排序后重新归位——
// 每集群 15 台 VM 轮值均分到 3 台 ESXi（每台 5 台，同步改写两侧引用），
// 每台 ESXi 的第 5 台关电并清空 Guest IP（共 6 台关电），
// 其余 VM 按全局序号分配 10.30.4.x 静态 Guest IP。
// 直接改写模拟器内存对象（属性查询实时生效）。
func shapeVMs(ctx *simulator.Context) error {
	allVMs := make([]*simulator.VirtualMachine, 0, 30)
	for _, ref := range ctx.Map.All("VirtualMachine") {
		vm, ok := ctx.Map.Get(ref.Reference()).(*simulator.VirtualMachine)
		if !ok {
			continue
		}
		allVMs = append(allVMs, vm)
	}

	clusters := make([]*simulator.ClusterComputeResource, 0, 2)
	for _, ref := range ctx.Map.All("ClusterComputeResource") {
		cluster, ok := ctx.Map.Get(ref.Reference()).(*simulator.ClusterComputeResource)
		if !ok {
			continue
		}
		clusters = append(clusters, cluster)
	}
	if len(clusters) == 0 {
		return fmt.Errorf("vcsim 清单中没有集群")
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Name < clusters[j].Name })

	ipSeq := 0
	for _, cluster := range clusters {
		// 收集本集群的宿主机（按名排序）与当前挂在这些宿主机上的 VM（按名排序）。
		hosts := make([]*simulator.HostSystem, 0, len(cluster.Host))
		hostSet := make(map[types.ManagedObjectReference]struct{}, len(cluster.Host))
		for _, href := range cluster.Host {
			host, ok := ctx.Map.Get(href).(*simulator.HostSystem)
			if !ok {
				return fmt.Errorf("集群 %s 引用的宿主机 %s 不存在", cluster.Name, href.Value)
			}
			hosts = append(hosts, host)
			hostSet[href] = struct{}{}
		}
		sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })

		vms := make([]*simulator.VirtualMachine, 0, 15)
		for _, vm := range allVMs {
			if vm.Runtime.Host == nil {
				return fmt.Errorf("VM %s 未分配宿主机", vm.Name)
			}
			if _, ok := hostSet[*vm.Runtime.Host]; ok {
				vms = append(vms, vm)
			}
		}
		sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
		if len(vms) != len(hosts)*5 {
			return fmt.Errorf("集群 %s 下 VM 数量 = %d，期望 %d", cluster.Name, len(vms), len(hosts)*5)
		}

		// 重新归位：每台 ESXi 分 5 台，两侧引用同步改写。
		assigned := make(map[types.ManagedObjectReference][]types.ManagedObjectReference, len(hosts))
		for i, vm := range vms {
			host := hosts[i/5]
			href := host.Reference()
			vm.Runtime.Host = &href
			vm.Summary.Runtime.Host = &href
			assigned[href] = append(assigned[href], vm.Reference())

			// 内存整形：vcsim 默认 VM 内存过小（采集换算后显示 0 GB），
			// 按 4/8/12GB 交错赋值贴近真实规格。
			memMB := int32(4096 * (1 + i%3))
			vm.Config.Hardware.MemoryMB = memMB
			vm.Summary.Config.MemorySizeMB = memMB

			if i%5 == 4 {
				// 关电 VM：真实 vCenter 下无 Guest IP，模拟采集盲区。
				vm.Runtime.PowerState = types.VirtualMachinePowerStatePoweredOff
				vm.Summary.Runtime.PowerState = types.VirtualMachinePowerStatePoweredOff
				vm.Guest.IpAddress = ""
				vm.Guest.Net = nil
				vm.Summary.Guest.IpAddress = ""
				continue
			}
			ipSeq++
			ip := fmt.Sprintf("10.30.4.%d", ipSeq)
			vm.Guest.IpAddress = ip
			if len(vm.Guest.Net) > 0 {
				vm.Guest.Net[0].IpAddress = []string{ip}
			} else {
				vm.Guest.Net = []types.GuestNicInfo{
					{Network: "VM Network", IpAddress: []string{ip}},
				}
			}
			vm.Summary.Guest.IpAddress = ip
		}
		for _, host := range hosts {
			host.Vm = assigned[host.Reference()]
		}
	}
	return nil
}
