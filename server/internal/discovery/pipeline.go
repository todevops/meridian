// Package discovery 实现发现记录摄入管道：
// 批量摄入 → 校验 → 原始层落库（留来源/时间戳/原始报文）→ 调和引擎 → 回写调和结果。
// HTTP 接口与 NATS 订阅通道共用同一管道。
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"meridian/server/internal/linker"
	"meridian/server/internal/reconcile"
	"meridian/server/internal/store"
	"meridian/server/internal/topology"
)

// RecordError 描述一条被拒绝的记录。
type RecordError struct {
	Index   int    `json:"index"`   // 被拒绝记录在请求数组中的下标
	Message string `json:"message"` // 拒绝原因
}

// IngestResult 是批量摄入结果，与 openapi.yaml 中 DiscoveryRecordBatchResponse 对应。
type IngestResult struct {
	Accepted int           `json:"accepted"` // 已接收条数
	Rejected int           `json:"rejected"` // 因校验失败被拒绝条数
	Errors   []RecordError `json:"errors"`   // 拒绝明细
}

// Pipeline 是发现记录摄入管道。
type Pipeline struct {
	db     *gorm.DB
	engine *reconcile.Engine
}

// NewPipeline 创建摄入管道。
// 同时挂载：
//   - 自动关联器（2B，internal/linker）：CI 建档/更新成功后异步按内置规则幂等 upsert 关系；
//   - network_link 内建调和（3B，internal/topology）：链路记录无模型定义，
//     按四元组幂等并自动维护 connected_to 关系。
//
// 失败均仅记日志，不阻断调和主流程。
func NewPipeline(db *gorm.DB) *Pipeline {
	engine := reconcile.NewEngine(db)
	engine.AddPostHook(linker.New(db).Handle)
	engine.RegisterBuiltin("network_link", topology.New(db).HandleLinkRecord)
	return &Pipeline{db: db, engine: engine}
}

// Engine 返回管道内嵌的调和引擎（供 preview 等只读场景使用）。
func (p *Pipeline) Engine() *reconcile.Engine {
	return p.engine
}

// ValidateRecord 校验单条发现记录的必填字段，返回错误说明（空串表示通过）。
func ValidateRecord(rec reconcile.Record) string {
	var missing []string
	if strings.TrimSpace(rec.Source) == "" {
		missing = append(missing, "source")
	}
	if strings.TrimSpace(rec.Collector) == "" {
		missing = append(missing, "collector")
	}
	if strings.TrimSpace(rec.ModelCandidate) == "" {
		missing = append(missing, "model_candidate")
	}
	if rec.Attributes == nil {
		missing = append(missing, "attributes")
	}
	if rec.OccurredAt.IsZero() {
		missing = append(missing, "occurred_at")
	}
	if len(missing) > 0 {
		return "缺少必填字段: " + strings.Join(missing, ", ")
	}
	return ""
}

// Ingest 批量摄入发现记录：逐条校验后写原始层并同步调和。
// 校验失败的记录被拒绝并记入 errors；调和冲突不算拒绝（属正常判定结果）。
func (p *Pipeline) Ingest(ctx context.Context, records []reconcile.Record) IngestResult {
	result := IngestResult{Errors: []RecordError{}}
	for i, rec := range records {
		if msg := ValidateRecord(rec); msg != "" {
			result.Rejected++
			result.Errors = append(result.Errors, RecordError{Index: i, Message: msg})
			continue
		}
		if err := p.ingestOne(ctx, rec); err != nil {
			result.Rejected++
			result.Errors = append(result.Errors, RecordError{Index: i, Message: err.Error()})
			continue
		}
		result.Accepted++
	}
	return result
}

// ingestOne 处理单条记录：原始层落库 → 调和 → 回写调和结果。
func (p *Pipeline) ingestOne(ctx context.Context, rec reconcile.Record) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("序列化原始报文失败: %w", err)
	}
	rawRec := store.DiscoveryRawRecord{
		Source:         rec.Source,
		Collector:      rec.Collector,
		ModelCandidate: rec.ModelCandidate,
		Payload:        datatypes.JSONMap{},
		OccurredAt:     rec.OccurredAt,
		ReceivedAt:     time.Now(),
	}
	_ = json.Unmarshal(raw, &rawRec.Payload)

	decision, err := p.engine.Evaluate(ctx, rec, false)
	if err != nil {
		rawRec.ResultAction = "error"
		rawRec.ResultMessage = err.Error()
		if dbErr := p.db.WithContext(ctx).Create(&rawRec).Error; dbErr != nil {
			return fmt.Errorf("写入原始层失败: %w", dbErr)
		}
		return fmt.Errorf("调和失败: %w", err)
	}

	rawRec.ResultAction = decision.Action
	rawRec.ResultCIID = decision.MatchedCIID
	rawRec.ResultMessage = truncate(strings.Join(decision.Reasons, "；"), 1000)
	if err := p.db.WithContext(ctx).Create(&rawRec).Error; err != nil {
		return fmt.Errorf("写入原始层失败: %w", err)
	}
	return nil
}

// IngestPayload 解析并摄入一条消息负载（NATS 订阅通道用）。
// 负载可以是批量对象 {"records":[...]} 或单条发现记录。
func (p *Pipeline) IngestPayload(ctx context.Context, payload []byte) IngestResult {
	var batch struct {
		Records []reconcile.Record `json:"records"`
	}
	if err := json.Unmarshal(payload, &batch); err == nil && len(batch.Records) > 0 {
		return p.Ingest(ctx, batch.Records)
	}
	var single reconcile.Record
	if err := json.Unmarshal(payload, &single); err != nil {
		return IngestResult{
			Rejected: 1,
			Errors:   []RecordError{{Index: 0, Message: fmt.Sprintf("消息负载解析失败: %v", err)}},
		}
	}
	return p.Ingest(ctx, []reconcile.Record{single})
}

// truncate 截断字符串到 max 字节。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
