// 稽核规则引擎（F-081）单元测试：表达式求值与待办闭环。
package auditrules

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/store"
)

// newTestDB 打开独立内存库并迁移全部实体。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// seedHostModel 写入 host/biz_app 模型定义。
func seedModels(t *testing.T, db *gorm.DB) (hostModel, appModel store.Model) {
	t.Helper()
	hostModel = store.Model{
		Code: "host", Name: "主机",
		Attributes: datatypes.NewJSONType([]store.AttributeDefinition{
			{Code: "ident", Name: "标识", Type: "string", Required: true},
			{Code: "ip", Name: "主 IP", Type: "ip", Required: true},
			{Code: "serial_no", Name: "序列号", Type: "string"},
		}),
	}
	appModel = store.Model{
		Code: "biz_app", Name: "应用",
		Attributes: datatypes.NewJSONType([]store.AttributeDefinition{
			{Code: "code", Name: "编码", Type: "string", Required: true},
		}),
	}
	for _, m := range []*store.Model{&hostModel, &appModel} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("创建模型失败: %v", err)
		}
	}
	return hostModel, appModel
}

// mkCI 快速建 CI。
func mkCI(t *testing.T, db *gorm.DB, modelID string, attrs map[string]any) store.CI {
	t.Helper()
	ci := store.CI{ModelID: modelID, Attributes: datatypes.JSONMap(attrs), Status: "active", Source: "manual"}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("创建 CI 失败: %v", err)
	}
	return ci
}

func TestExprEval(t *testing.T) {
	db := newTestDB(t)
	ci := store.CI{
		ModelID: "m1",
		Attributes: datatypes.JSONMap{
			"owner":             "张三",
			"backup_count":      float64(2),
			"cluster_name":      "core-db",
			"env":               "prod",
			"last_heartbeat_at": time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
		},
		Status: "active",
	}
	cases := []struct {
		expr string
		want bool
	}{
		{`not_empty(owner)`, true},
		{`empty(missing)`, true},
		{`not_empty(missing)`, false},
		{`env == "prod"`, true},
		{`env != "prod"`, false},
		{`backup_count > 0`, true},
		{`backup_count > 2`, false},
		{`backup_count >= 2`, true},
		{`not_empty(cluster_name) and backup_count > 0`, true},
		{`not_empty(cluster_name) and backup_count > 5`, false},
		{`empty(cluster_name) or backup_count > 0`, true},
		{`not empty(owner)`, true}, // owner 非空 → empty 为假 → not 为真
		{`age_days(last_heartbeat_at) <= 7`, true},
		{`age_days(last_heartbeat_at) <= 1`, false},
		{`age_days(missing) <= 7`, false}, // 缺失心跳时间视为超期
		{`cluster_name contains "core"`, true},
		{`(env == "prod" or env == "staging") and not_empty(owner)`, true},
		{`owner`, true},
		{`missing`, false},
	}
	for _, tc := range cases {
		ast, err := parseExpr(tc.expr)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", tc.expr, err)
		}
		ec := &evalCtx{db: db, ci: ci, modelCode: map[string]string{}, now: time.Now()}
		got, err := ec.truth(ast)
		if err != nil {
			t.Fatalf("求值 %q 失败: %v", tc.expr, err)
		}
		if got != tc.want {
			t.Errorf("表达式 %q = %v，期望 %v", tc.expr, got, tc.want)
		}
	}
}

func TestExprSyntaxError(t *testing.T) {
	for _, bad := range []string{`not_empty(`, `owner ==`, `a b c`, `unknown_fn(x)`, ``} {
		if err := ValidateAssertion(bad); err == nil {
			t.Errorf("断言 %q 应判定为非法", bad)
		}
	}
}

// TestRunRuleTodoLifecycle 验证待办闭环：违规生成 → 去重 → 修复自动关闭 → dry_run 不落库。
func TestRunRuleTodoLifecycle(t *testing.T) {
	db := newTestDB(t)
	hostModel, _ := seedModels(t, db)
	bad := mkCI(t, db, hostModel.ID, map[string]any{"ident": "h1", "ip": "10.0.0.1", "env": "prod"})
	mkCI(t, db, hostModel.ID, map[string]any{"ident": "h2", "ip": "10.0.0.2", "env": "prod", "owner": "张三"})
	mkCI(t, db, hostModel.ID, map[string]any{"ident": "h3", "ip": "10.0.0.3", "env": "staging"}) // filter 不匹配

	rule := store.AuditRule{
		Name: "生产主机必须有负责人", ModelCode: "host",
		Filter:    datatypes.JSONMap{"env": "prod"},
		Assertion: `not_empty(owner)`,
		Message:   "生产主机缺少负责人（owner）",
		Enabled:   true,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}
	eng := NewEngine(db)
	ctx := context.Background()

	// 第一次执行：检查 2 台（env=prod），1 台违规，生成 1 条待办。
	res, err := eng.RunRule(ctx, rule)
	if err != nil {
		t.Fatalf("执行规则失败: %v", err)
	}
	if res.Checked != 2 || len(res.Violations) != 1 || res.TodosCreated != 1 || res.TodosClosed != 0 {
		t.Fatalf("首次执行结果不符: %+v", res)
	}
	if res.Violations[0].CIID != bad.ID {
		t.Fatalf("违规 CI 不符: %+v", res.Violations)
	}

	// 第二次执行：同一 CI 仍违规，去重不新建。
	res, err = eng.RunRule(ctx, rule)
	if err != nil {
		t.Fatalf("二次执行失败: %v", err)
	}
	if res.TodosCreated != 0 {
		t.Fatalf("待办未去重: %+v", res)
	}

	// 修复后（补上 owner）：第三次执行自动关闭。
	db.Model(&store.CI{}).Where("id = ?", bad.ID).
		Update("attributes", datatypes.JSONMap{"ident": "h1", "ip": "10.0.0.1", "env": "prod", "owner": "李四"})
	res, err = eng.RunRule(ctx, rule)
	if err != nil {
		t.Fatalf("三次执行失败: %v", err)
	}
	if res.TodosClosed != 1 {
		t.Fatalf("修复后未自动关闭: %+v", res)
	}
	var todo store.GovernanceTodo
	if err := db.First(&todo, "rule_id = ? AND ci_id = ?", rule.ID, bad.ID).Error; err != nil {
		t.Fatalf("查询待办失败: %v", err)
	}
	if todo.Status != store.TodoStatusClosed || todo.ClosedAt == nil {
		t.Fatalf("待办状态不符: %+v", todo)
	}
}

// TestRunRuleDryRun 验证 dry_run 只出报告不落待办。
func TestRunRuleDryRun(t *testing.T) {
	db := newTestDB(t)
	hostModel, _ := seedModels(t, db)
	mkCI(t, db, hostModel.ID, map[string]any{"ident": "h1", "ip": "10.0.0.1"})

	rule := store.AuditRule{
		Name: "主机必须有业务归属", ModelCode: "host",
		Filter: datatypes.JSONMap{}, Assertion: `biz_attributed()`,
		Message: "主机未归属任何业务应用", Enabled: true, DryRun: true,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}
	res, err := NewEngine(db).RunRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if len(res.Violations) != 1 {
		t.Fatalf("dry_run 违规数不符: %+v", res)
	}
	var count int64
	db.Model(&store.GovernanceTodo{}).Count(&count)
	if count != 0 {
		t.Fatalf("dry_run 不应产生待办，实际 %d 条", count)
	}
}

// TestBizAttributed 验证归属判定：一跳直达与经 k8s_namespace 两跳。
func TestBizAttributed(t *testing.T) {
	db := newTestDB(t)
	hostModel, appModel := seedModels(t, db)
	nsModel := store.Model{Code: "k8s_namespace", Name: "命名空间",
		Attributes: datatypes.NewJSONType([]store.AttributeDefinition{{Code: "name", Type: "string"}})}
	wlModel := store.Model{Code: "k8s_workload", Name: "工作负载",
		Attributes: datatypes.NewJSONType([]store.AttributeDefinition{{Code: "name", Type: "string"}})}
	for _, m := range []*store.Model{&nsModel, &wlModel} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("创建模型失败: %v", err)
		}
	}

	app := mkCI(t, db, appModel.ID, map[string]any{"code": "mall"})
	hostOK := mkCI(t, db, hostModel.ID, map[string]any{"ident": "h1", "ip": "10.0.0.1"})
	hostBad := mkCI(t, db, hostModel.ID, map[string]any{"ident": "h2", "ip": "10.0.0.2"})
	ns := mkCI(t, db, nsModel.ID, map[string]any{"name": "mall"})
	wlOK := mkCI(t, db, wlModel.ID, map[string]any{"name": "deploy-a"})
	wlBad := mkCI(t, db, wlModel.ID, map[string]any{"name": "deploy-b"})

	link := func(code, src, dst string) {
		if err := db.Create(&store.CIRelation{RelationCode: code, SrcCIID: src, DstCIID: dst}).Error; err != nil {
			t.Fatalf("建关系失败: %v", err)
		}
	}
	link("deployed_on", app.ID, hostOK.ID) // host 一跳直达
	link("contains", app.ID, ns.ID)        // 应用整挂命名空间
	link("belongs_to", wlOK.ID, ns.ID)     // 工作负载经命名空间两跳

	rule := store.AuditRule{
		Name: "归属检查", ModelCode: "host", Filter: datatypes.JSONMap{},
		Assertion: `biz_attributed()`, Message: "未归属", Enabled: true, DryRun: true,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}
	res, err := NewEngine(db).RunRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if len(res.Violations) != 1 || res.Violations[0].CIID != hostBad.ID {
		t.Fatalf("host 归属判定不符: %+v", res.Violations)
	}

	ruleWl := store.AuditRule{
		Name: "工作负载归属", ModelCode: "k8s_workload", Filter: datatypes.JSONMap{},
		Assertion: `biz_attributed()`, Message: "未归属", Enabled: true, DryRun: true,
	}
	if err := db.Create(&ruleWl).Error; err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}
	res, err = NewEngine(db).RunRule(context.Background(), ruleWl)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if len(res.Violations) != 1 || res.Violations[0].CIID != wlBad.ID {
		t.Fatalf("工作负载两跳归属判定不符: %+v", res.Violations)
	}
}

// TestUniqueAssertion 验证序列号唯一断言与空值放行。
func TestUniqueAssertion(t *testing.T) {
	db := newTestDB(t)
	hostModel, _ := seedModels(t, db)
	mkCI(t, db, hostModel.ID, map[string]any{"ident": "h1", "ip": "10.0.0.1", "serial_no": "SN-1"})
	dup := mkCI(t, db, hostModel.ID, map[string]any{"ident": "h2", "ip": "10.0.0.2", "serial_no": "SN-1"})
	mkCI(t, db, hostModel.ID, map[string]any{"ident": "h3", "ip": "10.0.0.3"}) // 无序列号，放行

	rule := store.AuditRule{
		Name: "序列号唯一", ModelCode: "host", Filter: datatypes.JSONMap{},
		Assertion: `unique(serial_no)`, Message: "序列号重复", Enabled: true, DryRun: true,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("创建规则失败: %v", err)
	}
	res, err := NewEngine(db).RunRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if len(res.Violations) != 2 {
		t.Fatalf("重复序列号应两台均违规: %+v", res.Violations)
	}
	_ = dup
}

// TestSeedBuiltin 验证内置规则幂等种子。
func TestSeedBuiltin(t *testing.T) {
	db := newTestDB(t)
	if err := SeedBuiltin(db); err != nil {
		t.Fatalf("种子失败: %v", err)
	}
	if err := SeedBuiltin(db); err != nil {
		t.Fatalf("二次种子失败: %v", err)
	}
	var count int64
	db.Model(&store.AuditRule{}).Count(&count)
	if count != 6 {
		t.Fatalf("内置规则应为 6 条，实际 %d", count)
	}
}
