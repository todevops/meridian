// vCenter 清单拉取：govmomi 客户端封装与三类对象（集群/主机/虚拟机）属性提取。
package vsphere

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
)

// ClusterInfo 是一个 ESXi 集群的采集视图。
type ClusterInfo struct {
	Name      string
	MOID      string // vCenter 受管对象 ID（集群级唯一，作调和主键）
	HostCount int    // 集群内主机数
}

// HostInfo 是一台 ESXi 主机的采集视图。
type HostInfo struct {
	Name              string
	MOID              string // vCenter 受管对象 ID
	HardwareUUID      string // 硬件 UUID（主键）
	Model             string // 硬件型号
	CPUCores          int32  // 物理 CPU 核数
	MemMB             int64  // 内存（MB）
	ParentClusterMOID string // 所属集群 moid（独立主机为空串）
}

// VMInfo 是一台虚拟机的采集视图。
type VMInfo struct {
	Name           string
	InstanceUUID   string // 实例 UUID（主键，跨 vCenter 迁移不变）
	IP             string // 主 IP（guest.ipAddress，关电或无 VMware Tools 时为空）
	OS             string // 客户机操作系统全名
	VCPU           int32
	MemMB          int64
	PowerState     string // poweredOn/poweredOff/suspended
	ParentHostMOID string // 所属 ESXi 主机 moid
}

// Inventory 是一次全量清单快照。
type Inventory struct {
	Clusters []ClusterInfo
	Hosts    []HostInfo
	VMs      []VMInfo
}

// Client 是 vSphere SDK 客户端。
type Client struct {
	endpoint *url.URL
	insecure bool
}

// NormalizeURL 把 ":19007"、"host:port"、完整 URL 等写法规范化为带 /sdk 路径的 https URL，
// 并注入账密（govmomi 登录用）。
func NormalizeURL(raw, username, password string) (*url.URL, error) {
	v := strings.TrimRight(strings.TrimSpace(raw), "/")
	if v == "" {
		return nil, fmt.Errorf("VSPHERE_URL 为空")
	}
	if strings.HasPrefix(v, ":") {
		v = "localhost" + v
	}
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil {
		return nil, fmt.Errorf("解析 VSPHERE_URL %q 失败: %w", raw, err)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/sdk"
	}
	u.User = url.UserPassword(username, password)
	return u, nil
}

// NewClient 创建客户端。rawURL 支持 ":19007" 简写；insecure 为 true 时跳过 TLS 证书校验
// （对接 vcsim / 自签名证书的 vCenter）。
func NewClient(rawURL, username, password string, insecure bool) (*Client, error) {
	u, err := NormalizeURL(rawURL, username, password)
	if err != nil {
		return nil, err
	}
	return &Client{endpoint: u, insecure: insecure}, nil
}

// 各类对象需要拉取的属性路径。
var (
	clusterProps = []string{"name", "host"}
	hostProps    = []string{
		"name", "parent",
		"hardware.systemInfo.uuid",
		"hardware.cpuInfo.numCpuCores",
		"hardware.memorySize",
		"summary.hardware.model",
	}
	vmProps = []string{
		"name",
		"config.instanceUuid",
		"config.hardware.numCPU",
		"config.hardware.memoryMB",
		"guest.ipAddress",
		"guest.guestFullName",
		"runtime.powerState",
		"runtime.host",
	}
)

// Fetch 登录 vCenter 并全量拉取集群/主机/虚拟机清单。
func (c *Client) Fetch(ctx context.Context) (*Inventory, error) {
	conn, err := govmomi.NewClient(ctx, c.endpoint, c.insecure)
	if err != nil {
		return nil, fmt.Errorf("登录 vSphere %s 失败: %w", c.endpoint.Redacted(), err)
	}
	defer func() { _ = conn.Logout(ctx) }()

	m := view.NewManager(conn.Client)
	root := conn.Client.ServiceContent.RootFolder

	var inv Inventory

	// 集群
	cv, err := m.CreateContainerView(ctx, root, []string{"ClusterComputeResource"}, true)
	if err != nil {
		return nil, fmt.Errorf("创建集群视图失败: %w", err)
	}
	var clusters []mo.ClusterComputeResource
	if err := cv.Retrieve(ctx, []string{"ClusterComputeResource"}, clusterProps, &clusters); err != nil {
		_ = cv.Destroy(ctx)
		return nil, fmt.Errorf("拉取集群清单失败: %w", err)
	}
	_ = cv.Destroy(ctx)
	for _, cl := range clusters {
		inv.Clusters = append(inv.Clusters, ClusterInfo{
			Name:      cl.Name,
			MOID:      cl.Reference().Value,
			HostCount: len(cl.Host),
		})
	}

	// 主机
	hv, err := m.CreateContainerView(ctx, root, []string{"HostSystem"}, true)
	if err != nil {
		return nil, fmt.Errorf("创建主机视图失败: %w", err)
	}
	var hosts []mo.HostSystem
	if err := hv.Retrieve(ctx, []string{"HostSystem"}, hostProps, &hosts); err != nil {
		_ = hv.Destroy(ctx)
		return nil, fmt.Errorf("拉取主机清单失败: %w", err)
	}
	_ = hv.Destroy(ctx)
	for _, h := range hosts {
		info := HostInfo{
			Name: h.Name,
			MOID: h.Reference().Value,
		}
		if h.Hardware != nil {
			info.HardwareUUID = h.Hardware.SystemInfo.Uuid
			info.CPUCores = int32(h.Hardware.CpuInfo.NumCpuCores)
			info.MemMB = h.Hardware.MemorySize / 1024 / 1024
		}
		if h.Summary.Hardware != nil {
			info.Model = h.Summary.Hardware.Model
		}
		// 集群内主机的 parent 即 ClusterComputeResource；独立主机 parent 为 ComputeResource，不带集群归属。
		if h.Parent != nil && h.Parent.Type == "ClusterComputeResource" {
			info.ParentClusterMOID = h.Parent.Value
		}
		inv.Hosts = append(inv.Hosts, info)
	}

	// 虚拟机
	vv, err := m.CreateContainerView(ctx, root, []string{"VirtualMachine"}, true)
	if err != nil {
		return nil, fmt.Errorf("创建虚拟机视图失败: %w", err)
	}
	var vms []mo.VirtualMachine
	if err := vv.Retrieve(ctx, []string{"VirtualMachine"}, vmProps, &vms); err != nil {
		_ = vv.Destroy(ctx)
		return nil, fmt.Errorf("拉取虚拟机清单失败: %w", err)
	}
	_ = vv.Destroy(ctx)
	for _, vm := range vms {
		info := VMInfo{Name: vm.Name}
		if vm.Config != nil {
			info.InstanceUUID = vm.Config.InstanceUuid
			info.VCPU = vm.Config.Hardware.NumCPU
			info.MemMB = int64(vm.Config.Hardware.MemoryMB)
		}
		if vm.Guest != nil {
			info.IP = vm.Guest.IpAddress
			info.OS = vm.Guest.GuestFullName
		}
		info.PowerState = string(vm.Runtime.PowerState)
		if vm.Runtime.Host != nil {
			info.ParentHostMOID = vm.Runtime.Host.Value
		}
		inv.VMs = append(inv.VMs, info)
	}

	return &inv, nil
}
