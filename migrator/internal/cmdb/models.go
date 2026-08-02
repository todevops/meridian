// Package cmdb 定义 CMDB API 客户端与迁移所需的内嵌模型定义。
// 模型定义与 cmdb/scripts/seed/ 同风格（code/name/attributes/relations 结构），
// 额外为每类 CI 增加 netbox_id 留痕属性（NetBox 原始主键，支撑迁移后对账回查）。
package cmdb

// AttributeDefinition 与 CMDB 模型属性定义对应（同 store.AttributeDefinition）。
type AttributeDefinition struct {
	Name       string   `json:"name"`
	Code       string   `json:"code"`
	Type       string   `json:"type"`
	Required   bool     `json:"required,omitempty"`
	Unique     bool     `json:"unique,omitempty"`
	EnumValues []string `json:"enum_values,omitempty"`
	Source     string   `json:"source,omitempty"`
}

// RelationDefinition 与 CMDB 模型关系定义对应（同 store.RelationDefinition）。
type RelationDefinition struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	TargetModel string `json:"target_model"`
	Cardinality string `json:"cardinality"`
	Direction   string `json:"direction"`
}

// ModelDefinition 为创建模型的请求体（同 ModelCreateRequest）。
type ModelDefinition struct {
	Code       string                `json:"code"`
	Name       string                `json:"name"`
	Attributes []AttributeDefinition `json:"attributes"`
	Relations  []RelationDefinition  `json:"relations"`
}

// MigrationSource 为迁移写入的 CI 来源标识（审计与调和合并优先级用）。
const MigrationSource = "netbox-migration"

// netboxIDAttr 为所有迁移模型共用的 NetBox 原始 ID 留痕属性（模型内唯一）。
func netboxIDAttr() AttributeDefinition {
	return AttributeDefinition{Code: "netbox_id", Name: "NetBox 原始 ID", Type: "string", Unique: true, Source: MigrationSource}
}

// RequiredModels 返回迁移依赖的全部模型定义（不存在时由迁移器创建）。
// 与 scripts/seed/ 的差异：
//   - 每个模型增加 netbox_id（留痕）及必要的 netbox_*_id 关联留痕属性；
//   - network_device 增加 name 属性（CI 列表可读性需要），mgmt_ip 放宽为非必填
//     （NetBox 设备允许无 primary_ip4，必填会造成可避免的单条失败）。
func RequiredModels() []ModelDefinition {
	return []ModelDefinition{
		{
			Code: "room",
			Name: "机房",
			Attributes: []AttributeDefinition{
				{Code: "name", Name: "机房名称", Type: "string", Required: true, Source: "manual"},
				{Code: "code", Name: "机房编码", Type: "string", Required: true, Unique: true, Source: "manual"},
				{Code: "address", Name: "地址", Type: "string", Source: "manual"},
				netboxIDAttr(),
			},
			Relations: []RelationDefinition{},
		},
		{
			Code: "rack",
			Name: "机柜",
			Attributes: []AttributeDefinition{
				{Code: "name", Name: "机柜编号", Type: "string", Required: true, Source: "manual"},
				{Code: "u_capacity", Name: "U 位容量", Type: "number", Source: "manual"},
				{Code: "power_capacity_kw", Name: "电力容量(kW)", Type: "number", Source: "manual"},
				netboxIDAttr(),
				{Code: "netbox_site_id", Name: "所属机房 NetBox ID", Type: "string", Source: MigrationSource},
			},
			Relations: []RelationDefinition{
				{Code: "located_in", Name: "所在机房", TargetModel: "room", Cardinality: "one_to_one", Direction: "outgoing"},
			},
		},
		{
			Code: "network_device",
			Name: "网络设备",
			Attributes: []AttributeDefinition{
				{Code: "name", Name: "设备名称", Type: "string", Required: true, Source: "snmp"},
				{Code: "serial_no", Name: "序列号", Type: "string", Unique: true, Source: "snmp"},
				{Code: "model", Name: "型号", Type: "string", Source: "snmp"},
				{Code: "vendor", Name: "厂商", Type: "string", Source: "snmp"},
				{Code: "mgmt_ip", Name: "管理 IP", Type: "ip", Source: "snmp"},
				netboxIDAttr(),
				{Code: "netbox_rack_id", Name: "所在机柜 NetBox ID", Type: "string", Source: MigrationSource},
			},
			Relations: []RelationDefinition{
				{Code: "located_in", Name: "所在机柜", TargetModel: "rack", Cardinality: "one_to_one", Direction: "outgoing"},
			},
		},
		{
			Code: "vlan",
			Name: "VLAN",
			Attributes: []AttributeDefinition{
				{Code: "vid", Name: "VLAN ID", Type: "number", Required: true, Source: MigrationSource},
				{Code: "name", Name: "VLAN 名称", Type: "string", Required: true, Source: MigrationSource},
				{Code: "description", Name: "描述", Type: "string", Source: MigrationSource},
				netboxIDAttr(),
			},
			Relations: []RelationDefinition{},
		},
		{
			Code: "virtual_machine",
			Name: "虚拟机",
			Attributes: []AttributeDefinition{
				{Code: "instance_uuid", Name: "实例 UUID", Type: "string", Required: true, Unique: true, Source: "vcenter"},
				{Code: "name", Name: "虚拟机名称", Type: "string", Required: true, Source: "vcenter"},
				{Code: "vcpu", Name: "vCPU 核数", Type: "number", Source: "vcenter"},
				{Code: "memory_gb", Name: "内存(GB)", Type: "number", Source: "vcenter"},
				{Code: "power_state", Name: "电源状态", Type: "enum", EnumValues: []string{"poweredOn", "poweredOff", "suspended"}, Source: "vcenter"},
				netboxIDAttr(),
			},
			Relations: []RelationDefinition{
				{Code: "runs_on", Name: "运行于", TargetModel: "physical_server", Cardinality: "one_to_one", Direction: "outgoing"},
			},
		},
	}
}
