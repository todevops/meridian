// verify 模式：重拉 NetBox 七类实体与 CMDB 双轨对账（方案 13.4 节"双轨校验"）。
// CI 类（sites/racks/devices/vlans/virtual-machines）按 attributes.netbox_id 留痕匹配，
// 存在性之外比对关键字段（复用 translate.go 的映射产出期望值）；
// IPAM 类（prefixes/ip-addresses）按 cidr/address 匹配存在性。
// 输出 verify-report.json；总一致率 100% 时退出码 0，否则 2（见 cmd/migrate）。
package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"migrator/internal/cmdb"
	"migrator/internal/netbox"
)

// ModeVerify 为对账模式（重拉比对，不写入）。
const ModeVerify = "verify"

// maxVerifyDetails 为每类实体保留的缺失/字段差异明细上限（计数完整，明细截断）。
const maxVerifyDetails = 10

// MissingEntry 记录一条 NetBox 有而 CMDB 缺失的实体。
type MissingEntry struct {
	NetboxID string `json:"netbox_id"`
	Name     string `json:"name"`
}

// FieldDiff 记录一条字段级差异（NetBox 期望值 vs CMDB 实际值）。
type FieldDiff struct {
	NetboxID    string `json:"netbox_id"`
	Name        string `json:"name"`
	Field       string `json:"field"`        // CMDB 属性名（u_height→u_capacity、serial→serial_no、memory→memory_gb）
	NetboxValue string `json:"netbox_value"` // 按迁移映射换算后的期望值
	CMDBValue   string `json:"cmdb_value"`
}

// VerifyEntityReport 为单类实体的对账统计（缺失/差异明细各保留前 10 条）。
type VerifyEntityReport struct {
	Entity       string         `json:"entity"`
	Label        string         `json:"label"`
	NetboxCount  int            `json:"netbox_count"`  // NetBox 侧实体数
	Matched      int            `json:"matched"`       // CMDB 存在且关键字段一致数
	MissingCount int            `json:"missing_count"` // CMDB 缺失数
	DiffCount    int            `json:"diff_count"`    // 存在但字段不一致数
	Missing      []MissingEntry `json:"missing,omitempty"`
	FieldDiffs   []FieldDiff    `json:"field_diffs,omitempty"`
	FetchError   string         `json:"fetch_error,omitempty"` // 拉取级失败（该类无法对账，总一致率直接判不一致）
}

// recordMissing 记一条缺失；明细只保留前 maxVerifyDetails 条。
func (e *VerifyEntityReport) recordMissing(netboxID, name string) {
	e.MissingCount++
	if len(e.Missing) < maxVerifyDetails {
		e.Missing = append(e.Missing, MissingEntry{NetboxID: netboxID, Name: name})
	}
}

// recordDiff 记一条字段差异；明细只保留前 maxVerifyDetails 条。
func (e *VerifyEntityReport) recordDiff(d FieldDiff) {
	e.DiffCount++
	if len(e.FieldDiffs) < maxVerifyDetails {
		e.FieldDiffs = append(e.FieldDiffs, d)
	}
}

// VerifyReport 为完整对账报告（写入 verify-report.json）。
type VerifyReport struct {
	StartedAt       time.Time            `json:"started_at"`
	FinishedAt      time.Time            `json:"finished_at"`
	DurationSeconds float64              `json:"duration_seconds"`
	NetboxAPIURL    string               `json:"netbox_api_url"`
	CMDBAPIURL      string               `json:"cmdb_api_url"`
	Entities        []VerifyEntityReport `json:"entities"`
	TotalEntities   int                  `json:"total_entities"`   // NetBox 七类实体总数
	Consistent      int                  `json:"consistent"`       // 对账一致总数
	ConsistencyRate float64              `json:"consistency_rate"` // 总一致率（百分比，保留两位小数）
}

// FullyConsistent 判定总一致率是否 100%（任一类拉取失败也判不一致）。
func (r *VerifyReport) FullyConsistent() bool {
	if r.Consistent != r.TotalEntities {
		return false
	}
	for _, e := range r.Entities {
		if e.FetchError != "" {
			return false
		}
	}
	return true
}

// WriteJSON 把对账报告以缩进 JSON 写入指定路径。
func (r *VerifyReport) WriteJSON(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化对账报告失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("写入对账报告 %s 失败: %w", path, err)
	}
	return nil
}

// Summary 生成打印到终端的中文摘要。
func (r *VerifyReport) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "==> NetBox ↔ CMDB 双轨对账完成（耗时 %.2fs）\n", r.DurationSeconds)
	fmt.Fprintf(&b, "  %-18s %-14s %8s %8s %8s %8s\n", "类别", "说明", "NetBox数", "匹配", "缺失", "字段差异")
	for _, e := range r.Entities {
		fmt.Fprintf(&b, "  %-18s %-14s %8d %8d %8d %8d\n",
			e.Entity, e.Label, e.NetboxCount, e.Matched, e.MissingCount, e.DiffCount)
		if e.FetchError != "" {
			fmt.Fprintf(&b, "    × 拉取失败: %s\n", e.FetchError)
		}
		for _, m := range e.Missing {
			fmt.Fprintf(&b, "    × 缺失 [%s] %s\n", m.NetboxID, m.Name)
		}
		for _, d := range e.FieldDiffs {
			fmt.Fprintf(&b, "    × 差异 [%s] %s 字段 %s: NetBox=%s CMDB=%s\n",
				d.NetboxID, d.Name, d.Field, d.NetboxValue, d.CMDBValue)
		}
	}
	fmt.Fprintf(&b, "总一致率：%.2f%%（一致 %d / 总数 %d）\n", r.ConsistencyRate, r.Consistent, r.TotalEntities)
	if r.FullyConsistent() {
		b.WriteString("对账结论：一致（退出码 0）\n")
	} else {
		b.WriteString("对账结论：不一致（退出码 2）\n")
	}
	return b.String()
}

// Verifier 聚合 NetBox 读取端与 CMDB 读取端，执行只读对账。
type Verifier struct {
	nb   *netbox.Client
	cmdb *cmdb.Client
}

// NewVerifier 创建对账器。
func NewVerifier(nb *netbox.Client, cm *cmdb.Client) *Verifier {
	return &Verifier{nb: nb, cmdb: cm}
}

// translatedEntity 为按迁移映射翻译后的 NetBox 实体（对账期望值来源）。
type translatedEntity struct {
	netboxID string
	name     string
	attrs    map[string]any
}

// ciClassCheck 描述一类 CI 实体的对账规则。
type ciClassCheck struct {
	key       string   // 实体类别键（报告用）
	label     string   // 中文说明
	modelCode string   // CMDB 模型编码
	keyFields []string // 关键字段（CMDB 属性名）
	fetch     func(ctx context.Context) ([]translatedEntity, error)
}

// Run 执行完整对账：七类实体逐类拉取比对，汇总一致率。
// 单类拉取失败记入报告后继续后续实体（该类判不一致）。
func (v *Verifier) Run(ctx context.Context, netboxURL, cmdbURL string) (*VerifyReport, error) {
	report := &VerifyReport{
		StartedAt:    time.Now(),
		NetboxAPIURL: netboxURL,
		CMDBAPIURL:   cmdbURL,
	}

	ciChecks := []ciClassCheck{
		{"sites", "站点→机房", "room", []string{"name", "address"}, v.fetchSiteEntities},
		{"racks", "机架→机柜", "rack", []string{"name", "u_capacity"}, v.fetchRackEntities},
		{"devices", "设备→网络设备", "network_device", []string{"name", "serial_no", "mgmt_ip"}, v.fetchDeviceEntities},
		{"vlans", "VLAN→VLAN CI", "vlan", []string{"vid", "name"}, v.fetchVLANEntities},
		{"virtual_machines", "虚拟机→VM CI", "virtual_machine", []string{"name", "memory_gb", "vcpu"}, v.fetchVMEntities},
	}
	for _, check := range ciChecks {
		if ctx.Err() != nil {
			break
		}
		report.Entities = append(report.Entities, v.verifyCIClass(ctx, check))
	}
	if ctx.Err() == nil {
		report.Entities = append(report.Entities, v.verifyPrefixes(ctx))
	}
	if ctx.Err() == nil {
		report.Entities = append(report.Entities, v.verifyIPs(ctx))
	}

	for _, e := range report.Entities {
		report.TotalEntities += e.NetboxCount
		report.Consistent += e.Matched
	}
	report.ConsistencyRate = 100
	if report.TotalEntities > 0 {
		report.ConsistencyRate = math.Round(float64(report.Consistent)/float64(report.TotalEntities)*10000) / 100
	}
	report.FinishedAt = time.Now()
	report.DurationSeconds = math.Round(report.FinishedAt.Sub(report.StartedAt).Seconds()*100) / 100
	return report, nil
}

// verifyCIClass 对账一类 CI 实体：netbox_id 匹配存在性 + 关键字段比对。
func (v *Verifier) verifyCIClass(ctx context.Context, check ciClassCheck) VerifyEntityReport {
	ent := VerifyEntityReport{Entity: check.key, Label: check.label}
	entities, err := check.fetch(ctx)
	if err != nil {
		ent.FetchError = err.Error()
		return ent
	}
	ent.NetboxCount = len(entities)

	cis, err := v.cmdb.ListCIs(ctx, check.modelCode)
	if err != nil {
		ent.FetchError = err.Error()
		return ent
	}
	// 按 attributes.netbox_id 留痕建索引（与迁移写入侧的留痕约定对应）。
	index := map[string]cmdb.CI{}
	for _, ci := range cis {
		if nbID, ok := ci.Attributes["netbox_id"].(string); ok && nbID != "" {
			index[nbID] = ci
		}
	}

	for _, e := range entities {
		ci, ok := index[e.netboxID]
		if !ok {
			ent.recordMissing(e.netboxID, e.name)
			continue
		}
		consistent := true
		for _, field := range check.keyFields {
			if !attrEqual(e.attrs[field], ci.Attributes[field]) {
				ent.recordDiff(FieldDiff{
					NetboxID:    e.netboxID,
					Name:        e.name,
					Field:       field,
					NetboxValue: displayValue(e.attrs[field]),
					CMDBValue:   displayValue(ci.Attributes[field]),
				})
				consistent = false
			}
		}
		if consistent {
			ent.Matched++
		}
	}
	return ent
}

// verifyPrefixes 对账前缀：NetBox 前缀按归一化 CIDR 在 CMDB 前缀中的存在性。
// CMDB 多出的前缀（如 IP 迁移自动建的兜底段）不影响判定。
func (v *Verifier) verifyPrefixes(ctx context.Context) VerifyEntityReport {
	ent := VerifyEntityReport{Entity: "prefixes", Label: "前缀→IPAM 前缀"}
	prefixes, err := v.nb.ListPrefixes(ctx)
	if err != nil {
		ent.FetchError = err.Error()
		return ent
	}
	ent.NetboxCount = len(prefixes)

	cmdbPrefixes, err := v.cmdb.ListPrefixes(ctx)
	if err != nil {
		ent.FetchError = err.Error()
		return ent
	}
	existing := map[string]bool{}
	for _, p := range cmdbPrefixes {
		existing[normalizeCIDR(p.CIDR)] = true
	}

	for _, np := range prefixes {
		cidr := normalizeCIDR(np.Prefix)
		if existing[cidr] {
			ent.Matched++
		} else {
			ent.recordMissing(strconv.Itoa(np.ID), np.Prefix)
		}
	}
	return ent
}

// verifyIPs 对账 IP：NetBox 地址（剥离掩码）在 CMDB IP 中的存在性。
func (v *Verifier) verifyIPs(ctx context.Context) VerifyEntityReport {
	ent := VerifyEntityReport{Entity: "ip_addresses", Label: "IP→IPAM IP"}
	ips, err := v.nb.ListIPAddresses(ctx)
	if err != nil {
		ent.FetchError = err.Error()
		return ent
	}
	ent.NetboxCount = len(ips)

	cmdbIPs, err := v.cmdb.ListIPs(ctx)
	if err != nil {
		ent.FetchError = err.Error()
		return ent
	}
	existing := map[string]bool{}
	for _, ip := range cmdbIPs {
		existing[ip.IP] = true
	}

	for _, ip := range ips {
		p, err := netip.ParsePrefix(ip.Address)
		if err != nil {
			ent.recordMissing(strconv.Itoa(ip.ID), ip.Address+"（地址解析失败）")
			continue
		}
		if existing[p.Addr().String()] {
			ent.Matched++
		} else {
			ent.recordMissing(strconv.Itoa(ip.ID), ip.Address)
		}
	}
	return ent
}

// ---------- NetBox 实体拉取（复用 translate.go 映射产出期望值） ----------

func (v *Verifier) fetchSiteEntities(ctx context.Context) ([]translatedEntity, error) {
	sites, err := v.nb.ListSites(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]translatedEntity, 0, len(sites))
	for _, s := range sites {
		label, attrs := siteAttrs(s)
		out = append(out, translatedEntity{strconv.Itoa(s.ID), label, attrs})
	}
	return out, nil
}

func (v *Verifier) fetchRackEntities(ctx context.Context) ([]translatedEntity, error) {
	racks, err := v.nb.ListRacks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]translatedEntity, 0, len(racks))
	for _, r := range racks {
		label, attrs := rackAttrs(r)
		out = append(out, translatedEntity{strconv.Itoa(r.ID), label, attrs})
	}
	return out, nil
}

func (v *Verifier) fetchDeviceEntities(ctx context.Context) ([]translatedEntity, error) {
	devices, err := v.nb.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]translatedEntity, 0, len(devices))
	for _, d := range devices {
		label, attrs := deviceAttrs(d)
		out = append(out, translatedEntity{strconv.Itoa(d.ID), label, attrs})
	}
	return out, nil
}

func (v *Verifier) fetchVLANEntities(ctx context.Context) ([]translatedEntity, error) {
	vlans, err := v.nb.ListVLANs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]translatedEntity, 0, len(vlans))
	for _, vl := range vlans {
		label, attrs := vlanAttrs(vl)
		out = append(out, translatedEntity{strconv.Itoa(vl.ID), label, attrs})
	}
	return out, nil
}

func (v *Verifier) fetchVMEntities(ctx context.Context) ([]translatedEntity, error) {
	vms, err := v.nb.ListVirtualMachines(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]translatedEntity, 0, len(vms))
	for _, vm := range vms {
		label, attrs := vmAttrs(vm)
		out = append(out, translatedEntity{strconv.Itoa(vm.ID), label, attrs})
	}
	return out, nil
}

// ---------- 值比对工具 ----------

// attrEqual 比对期望值与实际属性值：数字按数值比（JSON 解码后均为 float64），
// 其余按字符串比；期望缺失时实际为空字符串/缺失视为一致。
func attrEqual(expected, actual any) bool {
	if expected == nil {
		return actual == nil || actual == ""
	}
	if actual == nil {
		return false
	}
	if fe, ok := toFloat(expected); ok {
		fa, ok2 := toFloat(actual)
		return ok2 && fe == fa
	}
	return fmt.Sprint(expected) == fmt.Sprint(actual)
}

// toFloat 尝试把值转换为 float64（覆盖 JSON 数字与整型）。
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// displayValue 格式化属性值用于差异明细展示。
func displayValue(v any) string {
	if v == nil {
		return "<缺失>"
	}
	return fmt.Sprint(v)
}

// normalizeCIDR 归一化 CIDR（解析并取掩码网络地址）；解析失败原样返回。
func normalizeCIDR(cidr string) string {
	if p, err := netip.ParsePrefix(cidr); err == nil {
		return p.Masked().String()
	}
	return cidr
}
