// Package store 定义 CMDB 核心 GORM 实体与自动迁移。
// 所有实体同时使用 PostgreSQL（生产）与 SQLite（本地开发，glebarez 纯 Go 驱动）验证。
package store

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// AttributeDefinition 描述模型的一个属性定义，与 openapi.yaml 中 AttributeDefinition 对应。
type AttributeDefinition struct {
	Name       string   `json:"name"`                  // 属性显示名
	Code       string   `json:"code"`                  // 属性编码（模型内唯一）
	Type       string   `json:"type"`                  // string/number/bool/enum/ip/date
	Required   bool     `json:"required,omitempty"`    // 是否必填
	Unique     bool     `json:"unique,omitempty"`      // 是否模型内唯一
	EnumValues []string `json:"enum_values,omitempty"` // type=enum 时的候选值
	Regex      string   `json:"regex,omitempty"`       // 字符串格式校验正则
	Source     string   `json:"source,omitempty"`      // 属性来源（manual/n9e 等）
}

// RelationDefinition 描述模型的一个关系定义，与 openapi.yaml 中 RelationDefinition 对应。
type RelationDefinition struct {
	Name        string `json:"name"`         // 关系显示名
	Code        string `json:"code"`         // 关系编码（模型内唯一）
	TargetModel string `json:"target_model"` // 对端模型编码
	Cardinality string `json:"cardinality"`  // one_to_one/one_to_many/many_to_many
	Direction   string `json:"direction"`    // outgoing/incoming（相对本模型）
}

// Model 是模型定义实体（元模型），attributes/relations 以 JSON 整体存储。
type Model struct {
	ID         string                                    `gorm:"primaryKey;size:36" json:"id"`
	Name       string                                    `gorm:"size:128;not null" json:"name"`
	Code       string                                    `gorm:"size:64;not null;uniqueIndex" json:"code"`
	Attributes datatypes.JSONType[[]AttributeDefinition] `json:"attributes"`
	Relations  datatypes.JSONType[[]RelationDefinition]  `json:"relations"`
	// ReconcileKeys 为调和键配置（按优先级排序，如主机模型为 ["ip"]——
	// 主 IP 即唯一键），调和引擎据此判定发现记录与存量 CI 的同一性。
	ReconcileKeys datatypes.JSONType[[]string] `json:"reconcile_keys,omitempty"`
	CreatedAt     time.Time                    `json:"created_at"`
	UpdatedAt     time.Time                    `json:"updated_at"`
}

// CI 是配置项实例实体。attributes 为 JSON 键值对，取值须通过所属模型的校验规则。
type CI struct {
	ID         string            `gorm:"primaryKey;size:36" json:"id"`
	ModelID    string            `gorm:"size:36;not null;index" json:"model_id"`
	Attributes datatypes.JSONMap `json:"attributes"`
	// FieldSources 记录每个属性字段最后写入来源（属性编码 → 来源标识），
	// 用于调和时的来源优先级合并；不对外暴露。
	FieldSources datatypes.JSONMap `json:"-"`
	// Status 为生命周期状态（F-026）：discovered/purchase/stock/active/maintenance/
	// pending_retire/retired；合法流转见 internal/lifecycle。
	Status    string    `gorm:"size:16;not null;index" json:"status"`
	Source    string    `gorm:"size:64;not null" json:"source"` // 建档来源
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CIRelation 是 CI 之间的关系实例。
// (relation_code, src_ci_id, dst_ci_id) 有数据库级唯一约束——人工建联与自动关联器
// 都按此三元组幂等去重，重复触发不会产生重复关系。
type CIRelation struct {
	ID           string    `gorm:"primaryKey;size:36" json:"id"`
	RelationCode string    `gorm:"size:64;not null;index;uniqueIndex:idx_ci_rel_unique" json:"relation_code"`
	SrcCIID      string    `gorm:"size:36;not null;index;uniqueIndex:idx_ci_rel_unique" json:"src_ci_id"`
	DstCIID      string    `gorm:"size:36;not null;index;uniqueIndex:idx_ci_rel_unique" json:"dst_ci_id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// 告警事件级别。
const (
	AlertLevelInfo     = "info"
	AlertLevelWarning  = "warning"
	AlertLevelCritical = "critical"
)

// AlertEvent 是告警事件实体（2B：黑设备等资产风险线索）。
// 由服务端各检测点写入（如调和引擎发现 black_device_risk 主机），
// 经 /api/v1/alerts 查询与确认（ack）。
type AlertEvent struct {
	ID     string `gorm:"primaryKey;size:36" json:"id"`
	Level  string `gorm:"size:16;not null;index" json:"level"`  // info/warning/critical
	Title  string `gorm:"size:256;not null" json:"title"`       // 告警标题
	Source string `gorm:"size:64;not null;index" json:"source"` // 产生来源（如调和引擎、ipscan）
	// CIID 为关联 CI（可空，如网段级告警无单一 CI）。
	CIID         string    `gorm:"size:36;index" json:"ci_id,omitempty"`
	Detail       string    `gorm:"size:2048" json:"detail"`                          // 详细说明
	Acknowledged bool      `gorm:"not null;default:false;index" json:"acknowledged"` // 是否已确认
	CreatedAt    time.Time `json:"created_at"`
}

// TableName 显式指定表名。
func (AlertEvent) TableName() string { return "alert_events" }

// DiscoveryRawRecord 是发现记录原始层：保留来源、时间戳与原始报文，
// 调和结果（动作/命中 CI/说明）也回写在此，便于追溯与重放。
type DiscoveryRawRecord struct {
	ID             string            `gorm:"primaryKey;size:36" json:"id"`
	Source         string            `gorm:"size:64;not null;index" json:"source"`
	Collector      string            `gorm:"size:128;not null" json:"collector"`
	ModelCandidate string            `gorm:"size:64;not null;index" json:"model_candidate"`
	Payload        datatypes.JSONMap `json:"payload"` // 原始报文（整条发现记录）
	OccurredAt     time.Time         `json:"occurred_at"`
	ReceivedAt     time.Time         `json:"received_at"`
	ResultAction   string            `gorm:"size:16" json:"result_action"` // create/update/conflict/pool/error
	ResultCIID     string            `gorm:"size:36" json:"result_ci_id,omitempty"`
	ResultMessage  string            `gorm:"size:1024" json:"result_message,omitempty"`
}

// PoolItem 是发现池条目：调和判定为 conflict（或暂无法调和）的记录在此等待人工裁决。
// 状态机：pending → confirmed（人工确认建档）/ ignored（人工忽略），终态不可逆。
type PoolItem struct {
	ID           string            `gorm:"primaryKey;size:36" json:"id"`
	ModelCode    string            `gorm:"size:64;not null;index" json:"model_code"`
	Record       datatypes.JSONMap `json:"record"` // 发现记录原文（source/collector/model_candidate/attributes/occurred_at）
	ConflictCIID string            `gorm:"size:36" json:"conflict_ci_id,omitempty"`
	// RecordHash 为记录同一性哈希（model_candidate + 调和键值 / 全属性），
	// 用于「ignore 后同一记录不再入池」的去重判定（D-02）；不对外暴露。
	RecordHash string `gorm:"size:64;index" json:"-"`
	// ReconcileAction 为调和判定动作（conflict/pool），供发现池列表展示判定类别。
	ReconcileAction string    `gorm:"size:16" json:"reconcile_action,omitempty"`
	Reason          string    `gorm:"size:1024" json:"reason"`              // 判定依据（多条以"；"连接）
	Status          string    `gorm:"size:16;not null;index" json:"status"` // pending/confirmed/ignored
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// 凭据类型枚举（F-005）。
const (
	CredentialTypeVCenter    = "vcenter"
	CredentialTypeAliyun     = "aliyun"
	CredentialTypeVolc       = "volc"
	CredentialTypeSNMP       = "snmp"
	CredentialTypeDB         = "db"
	CredentialTypeKubeconfig = "kubeconfig"
	CredentialTypeSSHIPMI    = "ssh_ipmi"
	CredentialTypeN9E        = "n9e"
	CredentialTypeNetbox     = "netbox"
	CredentialTypeJumpServer = "jumpserver"
)

// CredentialTypes 是全部合法凭据类型。
var CredentialTypes = []string{
	CredentialTypeVCenter, CredentialTypeAliyun, CredentialTypeVolc, CredentialTypeSNMP,
	CredentialTypeDB, CredentialTypeKubeconfig, CredentialTypeSSHIPMI,
	CredentialTypeN9E, CredentialTypeNetbox, CredentialTypeJumpServer,
}

// Credential 是凭据实体（F-005）：secret 明文绝不落库——
// SecretCiphertext 存 AES-256-GCM 加密后的 secret JSON（base64 编码），
// 任何 API 响应均不回传该字段。
type Credential struct {
	ID          string `gorm:"primaryKey;size:36" json:"id"`
	Name        string `gorm:"size:128;not null" json:"name"`
	Type        string `gorm:"size:32;not null;index" json:"type"` // 见 CredentialTypes
	Description string `gorm:"size:512" json:"description"`
	// SecretCiphertext 为密文列：nonce|ciphertext 的 base64；json:"-" 保证不外泄。
	SecretCiphertext string     `gorm:"type:text;not null" json:"-"`
	LastRotatedAt    *time.Time `json:"last_rotated_at,omitempty"`
	UseCount         int64      `gorm:"not null;default:0" json:"use_count"` // 被任务使用次数
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// TableName 显式指定表名。
func (Credential) TableName() string { return "credentials" }

// 凭据审计动作。
const (
	CredentialAuditCreate = "create"
	CredentialAuditUpdate = "update"
	CredentialAuditRotate = "rotate"
	CredentialAuditUse    = "use"
)

// CredentialAudit 是凭据操作审计：记录操作者、动作与来源（人工操作或任务引用）。
type CredentialAudit struct {
	ID           string    `gorm:"primaryKey;size:36" json:"id"`
	CredentialID string    `gorm:"size:36;not null;index" json:"credential_id"`
	Action       string    `gorm:"size:16;not null;index" json:"action"` // create/update/rotate/use
	Operator     string    `gorm:"size:64;not null" json:"operator"`     // 操作者用户名（任务使用场景为 system）
	Source       string    `gorm:"size:128;not null" json:"source"`      // 来源：manual 或 task:<任务名>
	CreatedAt    time.Time `json:"created_at"`
}

// TableName 显式指定表名。
func (CredentialAudit) TableName() string { return "credential_audits" }

// 采集任务状态。
const (
	TaskStatusIdle    = "idle"
	TaskStatusRunning = "running"
	TaskStatusError   = "error"
)

// 采集器类型前缀：builtin:<内置执行器名> 或 exec:<二进制名>。
const (
	CollectorTypeBuiltinPrefix = "builtin:"
	CollectorTypeExecPrefix    = "exec:"
	// CollectorBuiltinN9E 为内置 n9e 消费器执行器。
	CollectorBuiltinN9E = "builtin:n9e-consumer"
)

// DiscoveryTask 是采集任务实体（F-033）：由调度器按 interval_seconds 周期执行，
// 也可经 POST /discovery/tasks/{id}/run 手动触发。
type DiscoveryTask struct {
	ID              string            `gorm:"primaryKey;size:36" json:"id"`
	Name            string            `gorm:"size:128;not null" json:"name"`
	CollectorType   string            `gorm:"size:64;not null" json:"collector_type"` // builtin:* / exec:*
	CredentialID    *string           `gorm:"size:36" json:"credential_id,omitempty"`
	IntervalSeconds int               `gorm:"not null;default:300" json:"interval_seconds"`
	Enabled         bool              `gorm:"not null;default:false" json:"enabled"`
	Config          datatypes.JSONMap `json:"config"` // 执行器配置（api_url/binary/args/env/timeout_seconds 等）
	Status          string            `gorm:"size:16;not null;default:idle;index" json:"status"`
	LastRunAt       *time.Time        `json:"last_run_at,omitempty"`
	LastSuccessAt   *time.Time        `json:"last_success_at,omitempty"`
	LastError       string            `gorm:"size:1024" json:"last_error,omitempty"` // 截断存储
	RunCount        int64             `gorm:"not null;default:0" json:"run_count"`
	FailCount       int64             `gorm:"not null;default:0" json:"fail_count"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// TableName 显式指定表名。
func (DiscoveryTask) TableName() string { return "discovery_tasks" }

// DiscoveryTaskRun 是一次任务执行记录：含产出条数、成败与输出尾巴（截断 4KB）。
type DiscoveryTaskRun struct {
	ID           string     `gorm:"primaryKey;size:36" json:"id"`
	TaskID       string     `gorm:"size:36;not null;index" json:"task_id"`
	Trigger      string     `gorm:"size:16;not null" json:"trigger"` // schedule/manual
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Success      bool       `gorm:"not null;default:false" json:"success"`
	Produced     int        `gorm:"not null;default:0" json:"produced"` // 摄入接受的发现记录条数
	ErrorSummary string     `gorm:"size:1024" json:"error_summary,omitempty"`
	// Output 为 stdout/stderr 合并输出的尾部（截断 4KB），仅 exec 执行器有值。
	Output string `gorm:"size:4096" json:"output,omitempty"`
}

// TableName 显式指定表名。
func (DiscoveryTaskRun) TableName() string { return "discovery_task_runs" }

// User 是系统用户实体（认证主体）。密码只存 bcrypt 哈希，永不外泄。
type User struct {
	ID           string `gorm:"primaryKey;size:36" json:"id"`
	Username     string `gorm:"size:64;not null;uniqueIndex" json:"username"`
	DisplayName  string `gorm:"size:128;not null" json:"display_name"`
	PasswordHash string `gorm:"size:128;not null" json:"-"`
	Status       string `gorm:"size:16;not null;index" json:"status"` // active/disabled
	// IsBuiltin 标记内置账号（admin/collector），内置账号不可停用、不可改角色。
	IsBuiltin bool      `gorm:"not null;default:false" json:"is_builtin"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Role 是角色元数据实体（编码/显示名/说明）。
// 角色→权限点、用户→角色的授权关系不在此建表，统一由 Casbin 策略承载（casbin_rule 表）。
type Role struct {
	ID          string `gorm:"primaryKey;size:36" json:"id"`
	Code        string `gorm:"size:64;not null;uniqueIndex" json:"code"`
	Name        string `gorm:"size:128;not null" json:"name"`
	Description string `gorm:"size:512" json:"description"`
	// IsBuiltin 标记内置角色（admin/operator/viewer/collector），内置角色不可删除。
	IsBuiltin bool      `gorm:"not null;default:false" json:"is_builtin"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AuditLog 是 CI 变更审计：所有写操作（人工与采集器）按字段记录变更明细。
type AuditLog struct {
	ID     string `gorm:"primaryKey;size:36" json:"id"`
	CIID   string `gorm:"size:36;not null;index" json:"ci_id"`
	Action string `gorm:"size:32;not null" json:"action"` // create/update/conflict/lifecycle/retire
	Source string `gorm:"size:64;not null" json:"source"`
	// Operator 为操作者用户名（F-004）；采集器/调和等系统写入为 system，
	// 存量数据为空串（视为 system）。
	Operator  string            `gorm:"size:64;not null;default:'';index" json:"operator"`
	Changes   datatypes.JSONMap `json:"changes"` // 字段变更明细：{"ip": {"old": "...", "new": "..."}}
	Message   string            `gorm:"size:1024" json:"message,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// AuditRule 是稽核规则实体（F-081）：声明式规则 = 模型过滤条件 + 断言表达式 + 待办模板，
// 存库热更新，由稽核引擎每日执行或手动触发（POST /governance/rules/{id}/run）。
// Assertion 表达式语法见 internal/auditrules（如 `not_empty(owner)`、
// `not_empty(cluster_name) and backup_count > 0`）。
type AuditRule struct {
	ID        string `gorm:"primaryKey;size:36" json:"id"`
	Name      string `gorm:"size:128;not null" json:"name"`
	ModelCode string `gorm:"size:64;not null;index" json:"model_code"` // 目标模型编码（如 host）
	// Filter 为 CI 属性等值过滤条件（如 {"env":"prod"}），空对象表示模型内全部 CI。
	Filter    datatypes.JSONMap `json:"filter"`
	Assertion string            `gorm:"size:512;not null" json:"assertion"` // 断言表达式，为真即合规
	Message   string            `gorm:"size:512;not null" json:"message"`   // 违规时生成待办的标题
	Enabled   bool              `gorm:"not null;default:true;index" json:"enabled"`
	// DryRun 为演练模式：只出违规报告，不产生/关闭待办。
	DryRun    bool       `gorm:"not null;default:false" json:"dry_run"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// TableName 显式指定表名。
func (AuditRule) TableName() string { return "audit_rules" }

// 稽核待办状态。
const (
	TodoStatusOpen   = "open"
	TodoStatusClosed = "closed"
)

// GovernanceTodo 是整改待办实体（F-081）：断言失败的 CI 按 (rule_id, ci_id) 去重
// （仅保留一条 open 待办，由引擎在创建前查询判重）；规则下次执行通过时自动关闭。
type GovernanceTodo struct {
	ID        string     `gorm:"primaryKey;size:36" json:"id"`
	RuleID    string     `gorm:"size:36;not null;index:idx_todo_rule_ci" json:"rule_id"`
	CIID      string     `gorm:"size:36;not null;index:idx_todo_rule_ci" json:"ci_id"`
	Title     string     `gorm:"size:512;not null" json:"title"`
	Status    string     `gorm:"size:16;not null;index" json:"status"` // open/closed
	CreatedAt time.Time  `json:"created_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

// TableName 显式指定表名。
func (GovernanceTodo) TableName() string { return "governance_todos" }

// IPPrefix 是 IPAM 前缀（网段）实体：CIDR 为掩码规范化后的网络地址形式，
// parent_id 指向父前缀构成树形层级（同级前缀不允许重叠）。
type IPPrefix struct {
	ID          string    `gorm:"primaryKey;size:36" json:"id"`
	CIDR        string    `gorm:"column:cidr;size:64;not null;index" json:"cidr"` // 规范化 CIDR（如 10.0.0.0/24）
	Name        string    `gorm:"size:128;not null" json:"name"`
	VlanID      *int      `gorm:"index" json:"vlan_id,omitempty"`
	Description string    `gorm:"size:512" json:"description,omitempty"`
	ParentID    *string   `gorm:"size:36;index" json:"parent_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName 显式指定表名，避免命名策略对首字母缩写分词产生歧义。
func (IPPrefix) TableName() string { return "ip_prefixes" }

// IPAddress 是 IPAM 已登记 IP 实体：(prefix_id, ip) 全局唯一，
// ip 为 netip 规范化字符串；status 取 used（在用）/reserved（预留）。
type IPAddress struct {
	ID          string    `gorm:"primaryKey;size:36" json:"id"`
	PrefixID    string    `gorm:"size:36;not null;uniqueIndex:idx_ipam_prefix_ip" json:"prefix_id"`
	IP          string    `gorm:"size:64;not null;uniqueIndex:idx_ipam_prefix_ip" json:"ip"`
	Status      string    `gorm:"size:16;not null;index" json:"status"` // used/reserved
	CIID        string    `gorm:"size:36" json:"ci_id,omitempty"`
	Description string    `gorm:"size:512" json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName 显式指定表名。
func (IPAddress) TableName() string { return "ip_addresses" }

// RackMount 是机柜 U 位挂载记录：device_ci_id 挂在 rack_ci_id 的
// [u_position, u_position+u_height) 区间，区间内不允许与其他设备重叠。
type RackMount struct {
	ID         string    `gorm:"primaryKey;size:36" json:"id"`
	RackCIID   string    `gorm:"size:36;not null;index" json:"rack_ci_id"`
	DeviceCIID string    `gorm:"size:36;not null;index" json:"device_ci_id"`
	UPosition  int       `gorm:"not null" json:"u_position"` // 起始 U 位（从 1 开始）
	UHeight    int       `gorm:"not null" json:"u_height"`   // 占 U 高度
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TableName 显式指定表名。
func (RackMount) TableName() string { return "rack_mounts" }

// AllModels 返回需要自动迁移的全部实体。
func AllModels() []any {
	return []any{
		&Model{},
		&CI{},
		&CIRelation{},
		&DiscoveryRawRecord{},
		&PoolItem{},
		&AuditLog{},
		&IPPrefix{},
		&IPAddress{},
		&RackMount{},
		&User{},
		&Role{},
		&Credential{},
		&CredentialAudit{},
		&DiscoveryTask{},
		&DiscoveryTaskRun{},
		&AlertEvent{},
		&AuditRule{},
		&GovernanceTodo{},
	}
}

// BeforeCreate 为各实体在入库前生成 UUID 主键。
func (m *Model) BeforeCreate(_ *gorm.DB) error              { return ensureID(&m.ID) }
func (c *CI) BeforeCreate(_ *gorm.DB) error                 { return ensureID(&c.ID) }
func (r *CIRelation) BeforeCreate(_ *gorm.DB) error         { return ensureID(&r.ID) }
func (r *DiscoveryRawRecord) BeforeCreate(_ *gorm.DB) error { return ensureID(&r.ID) }
func (p *PoolItem) BeforeCreate(_ *gorm.DB) error           { return ensureID(&p.ID) }
func (a *AuditLog) BeforeCreate(_ *gorm.DB) error           { return ensureID(&a.ID) }
func (p *IPPrefix) BeforeCreate(_ *gorm.DB) error           { return ensureID(&p.ID) }
func (a *IPAddress) BeforeCreate(_ *gorm.DB) error          { return ensureID(&a.ID) }
func (m *RackMount) BeforeCreate(_ *gorm.DB) error          { return ensureID(&m.ID) }
func (u *User) BeforeCreate(_ *gorm.DB) error               { return ensureID(&u.ID) }
func (r *Role) BeforeCreate(_ *gorm.DB) error               { return ensureID(&r.ID) }
func (c *Credential) BeforeCreate(_ *gorm.DB) error         { return ensureID(&c.ID) }
func (a *CredentialAudit) BeforeCreate(_ *gorm.DB) error    { return ensureID(&a.ID) }
func (t *DiscoveryTask) BeforeCreate(_ *gorm.DB) error      { return ensureID(&t.ID) }
func (r *DiscoveryTaskRun) BeforeCreate(_ *gorm.DB) error   { return ensureID(&r.ID) }
func (a *AlertEvent) BeforeCreate(_ *gorm.DB) error         { return ensureID(&a.ID) }
func (r *AuditRule) BeforeCreate(_ *gorm.DB) error          { return ensureID(&r.ID) }
func (t *GovernanceTodo) BeforeCreate(_ *gorm.DB) error     { return ensureID(&t.ID) }

// ensureID 在主键为空时生成新 UUID。
func ensureID(id *string) error {
	if *id == "" {
		*id = uuid.NewString()
	}
	return nil
}
