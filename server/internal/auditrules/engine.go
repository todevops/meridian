// Package auditrules 稽核引擎主体（F-081）：
// 规则每日定时执行或经 POST /governance/rules/{id}/run 手动触发；
// 断言失败的 CI 按 (rule_id, ci_id) 去重生成整改待办，下次执行通过自动关闭；
// dry_run=true 只出违规报告，不产生/关闭任何待办。
package auditrules

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// ErrAssertionInvalid 表示断言表达式解析失败。
var ErrAssertionInvalid = errors.New("断言表达式非法")

// Engine 是稽核规则引擎。
type Engine struct {
	db *gorm.DB
}

// NewEngine 创建稽核引擎。
func NewEngine(db *gorm.DB) *Engine { return &Engine{db: db} }

// Violation 是一条违规记录（断言失败的 CI）。
type Violation struct {
	CIID  string `json:"ci_id"`
	Title string `json:"title"`
}

// RunResult 是一次规则执行的报告。
type RunResult struct {
	RuleID       string      `json:"rule_id"`
	RuleName     string      `json:"rule_name"`
	DryRun       bool        `json:"dry_run"`
	Checked      int         `json:"checked"`       // 参与检查的 CI 数
	Violations   []Violation `json:"violations"`    // 违规清单
	TodosCreated int         `json:"todos_created"` // 本次新建待办数
	TodosClosed  int         `json:"todos_closed"`  // 本次自动关闭待办数
}

// ValidateAssertion 校验断言表达式语法（创建/修改规则时使用）。
func ValidateAssertion(s string) error {
	if s == "" {
		return fmt.Errorf("%w: 不能为空", ErrAssertionInvalid)
	}
	if _, err := parseExpr(s); err != nil {
		return fmt.Errorf("%w: %v", ErrAssertionInvalid, err)
	}
	return nil
}

// RunRule 执行单条规则：过滤 → 断言 → 待办闭环。
func (e *Engine) RunRule(ctx context.Context, rule store.AuditRule) (RunResult, error) {
	result := RunResult{RuleID: rule.ID, RuleName: rule.Name, DryRun: rule.DryRun, Violations: []Violation{}}
	ast, err := parseExpr(rule.Assertion)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrAssertionInvalid, err)
	}
	var model store.Model
	if err := e.db.WithContext(ctx).First(&model, "code = ?", rule.ModelCode).Error; err != nil {
		return result, fmt.Errorf("模型 %q 不存在: %w", rule.ModelCode, err)
	}
	var cis []store.CI
	if err := e.db.WithContext(ctx).Where("model_id = ? AND status <> ?", model.ID, "retired").
		Find(&cis).Error; err != nil {
		return result, fmt.Errorf("查询 CI 失败: %w", err)
	}

	violated := map[string]bool{}
	for _, ci := range cis {
		if !matchFilter(rule.Filter, ci) {
			continue
		}
		result.Checked++
		ec := &evalCtx{db: e.db, ci: ci, modelCode: map[string]string{}, now: time.Now()}
		ok, err := ec.truth(ast)
		if err != nil {
			return result, fmt.Errorf("规则 %q 在 CI %s 上求值失败: %w", rule.Name, ci.ID, err)
		}
		if !ok {
			violated[ci.ID] = true
			result.Violations = append(result.Violations, Violation{CIID: ci.ID, Title: rule.Message})
		}
	}

	// 待办闭环（dry_run 不落库）。
	if !rule.DryRun {
		created, closed, err := e.reconcileTodos(ctx, rule, violated)
		if err != nil {
			return result, err
		}
		result.TodosCreated = created
		result.TodosClosed = closed
	}

	now := time.Now()
	e.db.WithContext(ctx).Model(&store.AuditRule{}).Where("id = ?", rule.ID).Update("last_run_at", now)
	return result, nil
}

// reconcileTodos 把违规集合与存量 open 待办对齐：新违规补建、已修复自动关闭。
func (e *Engine) reconcileTodos(ctx context.Context, rule store.AuditRule, violated map[string]bool) (created, closed int, err error) {
	var openTodos []store.GovernanceTodo
	if err := e.db.WithContext(ctx).
		Where("rule_id = ? AND status = ?", rule.ID, store.TodoStatusOpen).
		Find(&openTodos).Error; err != nil {
		return 0, 0, fmt.Errorf("查询存量待办失败: %w", err)
	}
	openByCI := map[string]store.GovernanceTodo{}
	for _, t := range openTodos {
		openByCI[t.CIID] = t
	}
	now := time.Now()
	for ciID := range violated {
		if _, exists := openByCI[ciID]; exists {
			continue // 已有 open 待办，去重
		}
		todo := store.GovernanceTodo{
			RuleID: rule.ID,
			CIID:   ciID,
			Title:  rule.Message,
			Status: store.TodoStatusOpen,
		}
		if err := e.db.WithContext(ctx).Create(&todo).Error; err != nil {
			return created, closed, fmt.Errorf("创建待办失败: %w", err)
		}
		created++
	}
	for _, t := range openTodos {
		if violated[t.CIID] {
			continue // 仍违规，保持打开
		}
		if err := e.db.WithContext(ctx).Model(&store.GovernanceTodo{}).Where("id = ?", t.ID).
			Updates(map[string]any{"status": store.TodoStatusClosed, "closed_at": now}).Error; err != nil {
			return created, closed, fmt.Errorf("自动关闭待办失败: %w", err)
		}
		closed++
	}
	return created, closed, nil
}

// RunAll 执行全部 enabled 规则（每日定时通道）。
func (e *Engine) RunAll(ctx context.Context) ([]RunResult, error) {
	var rules []store.AuditRule
	if err := e.db.WithContext(ctx).Where("enabled = ?", true).Find(&rules).Error; err != nil {
		return nil, fmt.Errorf("查询稽核规则失败: %w", err)
	}
	results := make([]RunResult, 0, len(rules))
	for _, rule := range rules {
		res, err := e.RunRule(ctx, rule)
		if err != nil {
			log.Printf("稽核规则 %q 执行失败: %v", rule.Name, err)
			continue
		}
		results = append(results, res)
	}
	return results, nil
}

// RunDailyLoop 每日执行通道：启动即跑一轮，之后每 24 小时一轮，直到 ctx 取消。
func (e *Engine) RunDailyLoop(ctx context.Context) {
	run := func() {
		results, err := e.RunAll(ctx)
		if err != nil {
			log.Printf("稽核规则每日执行失败: %v", err)
			return
		}
		created, closed := 0, 0
		for _, r := range results {
			created += r.TodosCreated
			closed += r.TodosClosed
		}
		log.Printf("稽核规则每日执行完成: %d 条规则，新建待办 %d，自动关闭 %d", len(results), created, closed)
	}
	run()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// matchFilter 按属性等值条件过滤 CI。
func matchFilter(filter datatypes.JSONMap, ci store.CI) bool {
	for k, v := range filter {
		if !valuesEqual(ci.Attributes[k], v) {
			return false
		}
	}
	return true
}

// builtinRule 是内置规则定义（幂等种子，按 name 判重）。
type builtinRule struct {
	Name      string
	ModelCode string
	Filter    map[string]any
	Assertion string
	Message   string
}

// builtinRules 为 PRD F-081 首期内置的 6 条规则。
var builtinRules = []builtinRule{
	{"生产主机必须有负责人", "host", map[string]any{"env": "prod"}, `not_empty(owner)`, "生产主机缺少负责人（owner）"},
	{"主机序列号唯一", "host", map[string]any{}, `unique(serial_no)`, "主机序列号在模型内重复"},
	{"心跳停更超 7 天未退役", "host", map[string]any{}, `age_days(last_heartbeat_at) <= 7`, "主机心跳停更超 7 天且未退役"},
	{"生产数据库必须有集群与备份", "db_instance", map[string]any{"env": "prod"}, `not_empty(cluster_name) and backup_count > 0`, "生产数据库实例缺少集群归属或无备份"},
	{"K8s 工作负载必须有业务归属", "k8s_workload", map[string]any{}, `biz_attributed()`, "K8s 工作负载未归属任何业务应用"},
	{"主机必须有业务归属", "host", map[string]any{}, `biz_attributed()`, "主机未归属任何业务应用"},
}

// SeedBuiltin 幂等写入内置规则（按 name 判重，不覆盖人工修改）。
func SeedBuiltin(db *gorm.DB) error {
	for _, def := range builtinRules {
		var count int64
		if err := db.Model(&store.AuditRule{}).Where("name = ?", def.Name).Count(&count).Error; err != nil {
			return fmt.Errorf("查询内置稽核规则失败: %w", err)
		}
		if count > 0 {
			continue
		}
		rule := store.AuditRule{
			Name:      def.Name,
			ModelCode: def.ModelCode,
			Filter:    datatypes.JSONMap(def.Filter),
			Assertion: def.Assertion,
			Message:   def.Message,
			Enabled:   true,
		}
		if err := db.Create(&rule).Error; err != nil {
			return fmt.Errorf("创建内置稽核规则 %q 失败: %w", def.Name, err)
		}
	}
	return nil
}
