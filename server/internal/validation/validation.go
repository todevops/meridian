// Package validation 实现模型校验规则执行器：
// 按 AttributeDefinition 对 CI 属性执行 required/unique/enum/regex 及基础类型校验，
// CI 写入（创建/PATCH/调和合并）时强制执行。
package validation

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"cmdb/server/internal/store"
)

// FieldErrors 为逐字段校验错误，键为属性编码，值为错误说明。
type FieldErrors map[string]string

// Error 实现 error 接口，汇总全部字段错误。
func (e FieldErrors) Error() string {
	parts := make([]string, 0, len(e))
	for field, msg := range e {
		parts = append(parts, fmt.Sprintf("%s: %s", field, msg))
	}
	return "属性校验未通过: " + strings.Join(parts, "; ")
}

// 允许的属性类型与 CI 状态枚举。
var (
	attrTypes  = map[string]bool{"string": true, "number": true, "bool": true, "enum": true, "ip": true, "date": true}
	ciStatuses = map[string]bool{"discovered": true, "active": true, "retired": true}
)

// ValidAttrType 报告属性类型是否合法。
func ValidAttrType(t string) bool { return attrTypes[t] }

// ValidCIStatus 报告 CI 状态是否合法。
func ValidCIStatus(s string) bool { return ciStatuses[s] }

// ValidateAttributes 对属性值集合执行字段级校验（required/enum/regex/类型），
// 不访问数据库；唯一性校验由 ValidateUnique 单独执行。
// values 应为合并后的完整属性集合（PATCH 场景为增量合并结果）。
func ValidateAttributes(defs []store.AttributeDefinition, values map[string]any) FieldErrors {
	errs := FieldErrors{}
	for _, def := range defs {
		v, present := values[def.Code]

		// required：缺失或空值（nil/空字符串）视为未填写。
		if def.Required && (!present || isEmpty(v)) {
			errs[def.Code] = fmt.Sprintf("属性 %s 为必填项", def.Code)
			continue
		}
		if !present || v == nil {
			continue // 非必填且未提供，跳过后续规则
		}

		// 基础类型校验。
		if msg := checkType(def, v); msg != "" {
			errs[def.Code] = msg
			continue
		}

		// enum：type=enum 且配置了候选值时，取值必须在候选集合内。
		if def.Type == "enum" && len(def.EnumValues) > 0 {
			s, _ := v.(string)
			ok := false
			for _, cand := range def.EnumValues {
				if s == cand {
					ok = true
					break
				}
			}
			if !ok {
				errs[def.Code] = fmt.Sprintf("属性 %s 取值 %q 不在枚举候选 %v 内", def.Code, s, def.EnumValues)
				continue
			}
		}

		// regex：配置正则时字符串值必须匹配。
		if def.Regex != "" {
			s, ok := v.(string)
			if !ok {
				errs[def.Code] = fmt.Sprintf("属性 %s 正则校验要求字符串值", def.Code)
				continue
			}
			re, err := regexp.Compile(def.Regex)
			if err != nil {
				errs[def.Code] = fmt.Sprintf("属性 %s 的正则配置无效: %v", def.Code, err)
				continue
			}
			if !re.MatchString(s) {
				errs[def.Code] = fmt.Sprintf("属性 %s 取值 %q 不匹配正则 %q", def.Code, s, def.Regex)
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// ValidateUnique 对声明 unique 的属性执行模型内唯一性校验，
// 排除 excludeCIID 自身（PATCH 场景），仅比较未退役 CI。
func ValidateUnique(db *gorm.DB, modelID string, defs []store.AttributeDefinition, values map[string]any, excludeCIID string) FieldErrors {
	errs := FieldErrors{}
	for _, def := range defs {
		if !def.Unique {
			continue
		}
		v, present := values[def.Code]
		if !present || isEmpty(v) {
			continue
		}
		var count int64
		q := db.Model(&store.CI{}).
			Where("model_id = ? AND status <> ?", modelID, "retired").
			Where(datatypes.JSONQuery("attributes").Equals(v, def.Code))
		if excludeCIID != "" {
			q = q.Where("id <> ?", excludeCIID)
		}
		if err := q.Count(&count).Error; err != nil {
			errs[def.Code] = fmt.Sprintf("属性 %s 唯一性检查失败: %v", def.Code, err)
			continue
		}
		if count > 0 {
			errs[def.Code] = fmt.Sprintf("属性 %s 取值 %v 在模型内已存在，违反唯一性约束", def.Code, v)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// isEmpty 判定值是否为空（nil 或空字符串）。
func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	return false
}

// checkType 执行基础类型校验，返回错误说明（空串表示通过）。
func checkType(def store.AttributeDefinition, v any) string {
	switch def.Type {
	case "string", "enum":
		if _, ok := v.(string); !ok {
			return fmt.Sprintf("属性 %s 应为字符串", def.Code)
		}
	case "number":
		switch v.(type) {
		case float64, float32, int, int64, int32, json.Number:
		default:
			return fmt.Sprintf("属性 %s 应为数值", def.Code)
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			return fmt.Sprintf("属性 %s 应为布尔值", def.Code)
		}
	case "ip":
		s, ok := v.(string)
		if !ok {
			return fmt.Sprintf("属性 %s 应为字符串形式的 IP 地址", def.Code)
		}
		if _, err := netip.ParseAddr(s); err != nil {
			return fmt.Sprintf("属性 %s 取值 %q 不是合法 IP 地址", def.Code, s)
		}
	case "date":
		s, ok := v.(string)
		if !ok {
			return fmt.Sprintf("属性 %s 应为字符串形式的日期时间", def.Code)
		}
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			if _, err2 := time.Parse("2006-01-02", s); err2 != nil {
				return fmt.Sprintf("属性 %s 取值 %q 不是合法日期时间（RFC3339 或 YYYY-MM-DD）", def.Code, s)
			}
		}
	}
	return ""
}
