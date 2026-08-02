// Package migrate 实现 NetBox → CMDB 的迁移编排与迁移报告。
// 流程按方案 13.1 节：确保模型 → 站点/机架/设备/VLAN/虚拟机 → 前缀/IP → 报告。
package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// maxFailureDetails 为每类实体保留的失败明细上限（计数不受影响）。
const maxFailureDetails = 5

// FailureDetail 记录一条迁移失败的明细。
type FailureDetail struct {
	NetboxID string `json:"netbox_id"` // NetBox 原始 ID（拉取级失败为 "-"）
	Name     string `json:"name"`      // 记录名称（便于回查）
	Error    string `json:"error"`     // 失败原因
}

// EntityReport 为单类实体的迁移统计（拉取数/成功数/失败数/失败明细前 5 条）。
type EntityReport struct {
	Entity    string          `json:"entity"` // 实体类别键（sites/racks/...）
	Label     string          `json:"label"`  // 中文说明
	Fetched   int             `json:"fetched"`
	Succeeded int             `json:"succeeded"`
	Failed    int             `json:"failed"`
	Failures  []FailureDetail `json:"failures,omitempty"`
}

// recordSuccess 记一次成功。
func (e *EntityReport) recordSuccess() { e.Succeeded++ }

// recordFailure 记一次失败；明细只保留前 maxFailureDetails 条，计数完整。
func (e *EntityReport) recordFailure(netboxID, name string, err error) {
	e.Failed++
	if len(e.Failures) < maxFailureDetails {
		e.Failures = append(e.Failures, FailureDetail{NetboxID: netboxID, Name: name, Error: err.Error()})
	}
}

// recordFetchError 记录拉取级失败（不计入 Fetched/Failed 计数，仅留明细）。
func (e *EntityReport) recordFetchError(err error) {
	if len(e.Failures) < maxFailureDetails {
		e.Failures = append(e.Failures, FailureDetail{NetboxID: "-", Name: "拉取列表", Error: err.Error()})
	}
}

// ModelReport 记录一个模型的确保结果。
type ModelReport struct {
	Code   string `json:"code"`
	Status string `json:"status"` // created（新建）/ existing（已存在）
}

// Report 为完整迁移报告（写入 migration-report.json）。
type Report struct {
	StartedAt       time.Time      `json:"started_at"`
	FinishedAt      time.Time      `json:"finished_at"`
	DurationSeconds float64        `json:"duration_seconds"`
	NetboxAPIURL    string         `json:"netbox_api_url"`
	CMDBAPIURL      string         `json:"cmdb_api_url"`
	Models          []ModelReport  `json:"models"`
	Entities        []EntityReport `json:"entities"`
}

// entity 按类别键取回（或追加）实体统计块，保持固定的输出顺序。
func (r *Report) entity(key, label string) *EntityReport {
	for i := range r.Entities {
		if r.Entities[i].Entity == key {
			return &r.Entities[i]
		}
	}
	r.Entities = append(r.Entities, EntityReport{Entity: key, Label: label})
	return &r.Entities[len(r.Entities)-1]
}

// TotalFailed 汇总全部实体的失败数。
func (r *Report) TotalFailed() int {
	total := 0
	for _, e := range r.Entities {
		total += e.Failed
	}
	return total
}

// WriteJSON 把报告以缩进 JSON 写入指定路径。
func (r *Report) WriteJSON(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化迁移报告失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("写入迁移报告 %s 失败: %w", path, err)
	}
	return nil
}

// Summary 生成打印到终端的中文摘要。
func (r *Report) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "==> NetBox → CMDB 迁移完成（耗时 %.2fs）\n", r.DurationSeconds)

	parts := make([]string, 0, len(r.Models))
	for _, m := range r.Models {
		status := "已存在"
		if m.Status == "created" {
			status = "新建"
		}
		parts = append(parts, fmt.Sprintf("%s=%s", m.Code, status))
	}
	fmt.Fprintf(&b, "模型确保：%s\n", strings.Join(parts, " "))

	b.WriteString("实体迁移：\n")
	fmt.Fprintf(&b, "  %-18s %-14s %6s %6s %6s\n", "类别", "说明", "拉取", "成功", "失败")
	for _, e := range r.Entities {
		fmt.Fprintf(&b, "  %-18s %-14s %6d %6d %6d\n", e.Entity, e.Label, e.Fetched, e.Succeeded, e.Failed)
		for _, f := range e.Failures {
			fmt.Fprintf(&b, "    × [%s] %s: %s\n", f.NetboxID, f.Name, f.Error)
		}
	}
	fmt.Fprintf(&b, "失败合计：%d 条\n", r.TotalFailed())
	return b.String()
}
