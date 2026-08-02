// 校验规则执行器单测：required/unique/enum/regex 及基础类型校验。
package validation

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"cmdb/server/internal/store"
)

// testDefs 构造覆盖全部规则类型的属性定义。
func testDefs() []store.AttributeDefinition {
	return []store.AttributeDefinition{
		{Name: "主机名", Code: "hostname", Type: "string", Required: true},
		{Name: "内网IP", Code: "ip", Type: "ip", Unique: true},
		{Name: "环境", Code: "env", Type: "enum", EnumValues: []string{"prod", "staging"}},
		{Name: "序列号", Code: "sn", Type: "string", Regex: `^[A-Z]{2}\d{4}$`},
		{Name: "CPU核数", Code: "cpu_num", Type: "number"},
		{Name: "已备案", Code: "registered", Type: "bool"},
		{Name: "上线日期", Code: "online_at", Type: "date"},
	}
}

func TestValidateAttributes(t *testing.T) {
	cases := []struct {
		name    string
		values  map[string]any
		wantErr string // 期望出错的属性编码，空串表示应通过
	}{
		{
			name: "全部合法",
			values: map[string]any{
				"hostname": "web-01", "ip": "10.0.0.1", "env": "prod",
				"sn": "AB1234", "cpu_num": 8.0, "registered": true,
				"online_at": "2026-01-01",
			},
		},
		{
			name:    "缺少必填项",
			values:  map[string]any{"ip": "10.0.0.1"},
			wantErr: "hostname",
		},
		{
			name:    "必填项为空字符串",
			values:  map[string]any{"hostname": "  "},
			wantErr: "hostname",
		},
		{
			name:    "枚举值不在候选内",
			values:  map[string]any{"hostname": "web-01", "env": "dev"},
			wantErr: "env",
		},
		{
			name:    "正则不匹配",
			values:  map[string]any{"hostname": "web-01", "sn": "abcd"},
			wantErr: "sn",
		},
		{
			name:    "IP 格式非法",
			values:  map[string]any{"hostname": "web-01", "ip": "999.1.1.1"},
			wantErr: "ip",
		},
		{
			name:    "数值类型错误",
			values:  map[string]any{"hostname": "web-01", "cpu_num": "八核"},
			wantErr: "cpu_num",
		},
		{
			name:    "布尔类型错误",
			values:  map[string]any{"hostname": "web-01", "registered": "yes"},
			wantErr: "registered",
		},
		{
			name:    "日期格式非法",
			values:  map[string]any{"hostname": "web-01", "online_at": "昨天"},
			wantErr: "online_at",
		},
		{
			name:   "RFC3339 日期合法",
			values: map[string]any{"hostname": "web-01", "online_at": "2026-01-02T03:04:05Z"},
		},
		{
			name:   "非必填项缺失不报错",
			values: map[string]any{"hostname": "web-01"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateAttributes(testDefs(), tc.values)
			if tc.wantErr == "" {
				if errs != nil {
					t.Fatalf("期望通过，实际报错: %v", errs)
				}
				return
			}
			if errs == nil {
				t.Fatalf("期望属性 %s 报错，实际通过", tc.wantErr)
			}
			if _, ok := errs[tc.wantErr]; !ok {
				t.Fatalf("期望属性 %s 报错，实际错误: %v", tc.wantErr, errs)
			}
		})
	}
}

// setupUniqueDB 打开独立内存库并预置一条带唯一属性的 CI。
func setupUniqueDB(t *testing.T) (*gorm.DB, store.Model, store.CI) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	model := store.Model{
		Name: "主机", Code: "host",
		Attributes: datatypes.NewJSONType(testDefs()),
	}
	if err := db.Create(&model).Error; err != nil {
		t.Fatalf("创建模型失败: %v", err)
	}
	ci := store.CI{
		ModelID:    model.ID,
		Attributes: datatypes.JSONMap{"hostname": "web-01", "ip": "10.0.0.1"},
		Status:     "active",
		Source:     "manual",
	}
	if err := db.Create(&ci).Error; err != nil {
		t.Fatalf("创建 CI 失败: %v", err)
	}
	return db, model, ci
}

func TestValidateUnique(t *testing.T) {
	db, model, ci := setupUniqueDB(t)

	// 同模型另一条 CI 使用相同 ip → 违反唯一性。
	errs := ValidateUnique(db, model.ID, testDefs(), map[string]any{"ip": "10.0.0.1"}, "")
	if errs == nil {
		t.Fatal("期望唯一性冲突，实际通过")
	}
	if _, ok := errs["ip"]; !ok {
		t.Fatalf("期望 ip 字段报错，实际: %v", errs)
	}

	// 排除自身（PATCH 场景）→ 通过。
	if errs := ValidateUnique(db, model.ID, testDefs(), map[string]any{"ip": "10.0.0.1"}, ci.ID); errs != nil {
		t.Fatalf("排除自身后应通过，实际: %v", errs)
	}

	// 不同 ip → 通过。
	if errs := ValidateUnique(db, model.ID, testDefs(), map[string]any{"ip": "10.0.0.2"}, ""); errs != nil {
		t.Fatalf("不同 ip 应通过，实际: %v", errs)
	}

	// 已退役 CI 不占唯一性名额。
	if err := db.Model(&store.CI{}).Where("id = ?", ci.ID).Update("status", "retired").Error; err != nil {
		t.Fatalf("更新状态失败: %v", err)
	}
	if errs := ValidateUnique(db, model.ID, testDefs(), map[string]any{"ip": "10.0.0.1"}, ""); errs != nil {
		t.Fatalf("退役 CI 不应占唯一性名额，实际: %v", errs)
	}
}
