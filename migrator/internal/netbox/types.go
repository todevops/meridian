// Package netbox 定义 NetBox REST API 的只读客户端：
// 按 {count,next,previous,results} 信封分页拉取站点/机架/设备/VLAN/虚拟机/前缀/IP。
// 认证方式为 "Authorization: Token <token>"，与 mock 平台（:19002）约定一致。
package netbox

import "encoding/json"

// listEnvelope 为 NetBox 列表接口的统一分页信封。
type listEnvelope struct {
	Count    int               `json:"count"`
	Next     *string           `json:"next"`
	Previous *string           `json:"previous"`
	Results  []json.RawMessage `json:"results"`
}

// nestedRef 表示 NetBox 对象中的嵌套引用（如 device.site、rack.site）。
type nestedRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// labeledValue 表示 NetBox 的状态类字段（{"value":"active","label":"Active"}）。
type labeledValue struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Site 站点（→ CMDB 机房）。
type Site struct {
	ID              int          `json:"id"`
	Name            string       `json:"name"`
	Slug            string       `json:"slug"`
	Status          labeledValue `json:"status"`
	PhysicalAddress string       `json:"physical_address"`
	Description     string       `json:"description"`
}

// Rack 机架（→ CMDB 机柜）。
type Rack struct {
	ID      int          `json:"id"`
	Name    string       `json:"name"`
	Site    *nestedRef   `json:"site"`
	UHeight float64      `json:"u_height"`
	Status  labeledValue `json:"status"`
}

// Device 设备（→ CMDB 网络设备）。
type Device struct {
	ID         int          `json:"id"`
	Name       string       `json:"name"`
	DeviceType *deviceType  `json:"device_type"`
	Serial     string       `json:"serial"`
	PrimaryIP4 *ipRef       `json:"primary_ip4"`
	Site       *nestedRef   `json:"site"`
	Rack       *nestedRef   `json:"rack"`
	DeviceRole *nestedRef   `json:"device_role"`
	Status     labeledValue `json:"status"`
}

type deviceType struct {
	ID           int        `json:"id"`
	Model        string     `json:"model"`
	Manufacturer *nestedRef `json:"manufacturer"`
}

// ipRef 表示 NetBox 中带掩码的 IP 引用（address 形如 "10.1.0.2/24"）。
type ipRef struct {
	ID      int    `json:"id"`
	Address string `json:"address"`
}

// VLAN（→ CMDB vlan CI）。
type VLAN struct {
	ID          int          `json:"id"`
	VID         int          `json:"vid"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Status      labeledValue `json:"status"`
}

// VirtualMachine 虚拟机（→ CMDB virtual_machine CI）。
type VirtualMachine struct {
	ID       int          `json:"id"`
	Name     string       `json:"name"`
	Status   labeledValue `json:"status"`
	VCPUs    float64      `json:"vcpus"`
	Memory   float64      `json:"memory"` // 单位 MB（NetBox 约定）
	Disk     float64      `json:"disk"`
	Comments string       `json:"comments"`
}

// Prefix 网段（→ CMDB IPAM 前缀）。
type Prefix struct {
	ID          int          `json:"id"`
	Prefix      string       `json:"prefix"` // CIDR，如 "10.1.0.0/16"
	Description string       `json:"description"`
	VLAN        *vlanRef     `json:"vlan"`
	Status      labeledValue `json:"status"`
	IsPool      bool         `json:"is_pool"`
}

type vlanRef struct {
	ID   int    `json:"id"`
	VID  int    `json:"vid"`
	Name string `json:"name"`
}

// IPAddress IP 地址（→ CMDB IPAM IP）。
type IPAddress struct {
	ID          int          `json:"id"`
	Address     string       `json:"address"` // 带掩码，如 "10.1.0.5/24"
	Status      labeledValue `json:"status"`
	Description string       `json:"description"`
	DNSName     string       `json:"dns_name"`
}
