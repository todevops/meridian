// Package auditrules 实现稽核规则引擎（F-081）：
// 声明式规则 = 模型过滤条件（filter JSON）+ 断言表达式（assertion）+ 待办模板（message）。
// 本文件实现断言表达式的词法/语法解析与求值。
//
// 表达式语法：
//
//	expr      := orExpr
//	orExpr    := andExpr ("or" andExpr)*
//	andExpr   := notExpr ("and" notExpr)*
//	notExpr   := "not" notExpr | primary
//	primary   := "(" expr ")" | comparison
//	comparison:= operand (("=="|"!="|">"|">="|"<"|"<="|"contains") operand)?
//	operand   := ident | number | "string" | funcCall
//	funcCall  := ident "(" [ident ("," ident)*] ")"
//
// 标识符解析为 CI 属性值；内置函数：
//   - empty(attr) / not_empty(attr)：属性缺失或为空串
//   - age_days(attr)：日期属性距今天数（缺失/不可解析视为 +Inf，即"超期"）
//   - unique(attr)：属性在模型内（未退役 CI 间）唯一；空值视为通过
//   - biz_attributed()：CI 已归属业务应用（一跳直达 biz_app，或经 k8s_namespace 两跳）
package auditrules

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// ---------- 词法 ----------

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokNumber
	tokString
	tokOp
	tokLParen
	tokRParen
	tokComma
)

type token struct {
	kind tokenKind
	text string
}

// tokenize 把表达式切分为 token 序列。
func tokenize(s string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(s) {
		ch := s[i]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			i++
		case ch == '(':
			toks = append(toks, token{tokLParen, "("})
			i++
		case ch == ')':
			toks = append(toks, token{tokRParen, ")"})
			i++
		case ch == ',':
			toks = append(toks, token{tokComma, ","})
			i++
		case ch == '"' || ch == '\'':
			end := i + 1
			var sb strings.Builder
			for end < len(s) && s[end] != ch {
				if s[end] == '\\' && end+1 < len(s) {
					end++
				}
				sb.WriteByte(s[end])
				end++
			}
			if end >= len(s) {
				return nil, fmt.Errorf("字符串字面量未闭合: %s", s[i:])
			}
			toks = append(toks, token{tokString, sb.String()})
			i = end + 1
		case ch == '=' || ch == '!' || ch == '>' || ch == '<':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, token{tokOp, s[i : i+2]})
				i += 2
			} else if ch == '=' || ch == '!' {
				return nil, fmt.Errorf("非法运算符（应为 ==/!=）: %c", ch)
			} else {
				toks = append(toks, token{tokOp, string(ch)})
				i++
			}
		case ch >= '0' && ch <= '9' || ch == '-' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			end := i + 1
			for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == '.') {
				end++
			}
			toks = append(toks, token{tokNumber, s[i:end]})
			i = end
		case ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch == '_':
			end := i + 1
			for end < len(s) && (s[end] >= 'a' && s[end] <= 'z' || s[end] >= 'A' && s[end] <= 'Z' || s[end] >= '0' && s[end] <= '9' || s[end] == '_') {
				end++
			}
			word := s[i:end]
			if word == "and" || word == "or" || word == "not" || word == "contains" {
				toks = append(toks, token{tokOp, word})
			} else {
				toks = append(toks, token{tokIdent, word})
			}
			i = end
		default:
			return nil, fmt.Errorf("无法识别的字符: %q", string(ch))
		}
	}
	toks = append(toks, token{tokEOF, ""})
	return toks, nil
}

// ---------- 语法树 ----------

// node 是表达式语法树节点。
type node interface {
	eval(ec *evalCtx) (any, error)
}

type orNode struct{ l, r node }

func (n orNode) eval(ec *evalCtx) (any, error) {
	l, err := ec.truth(n.l)
	if err != nil {
		return nil, err
	}
	if l {
		return true, nil
	}
	return ec.truth(n.r)
}

type andNode struct{ l, r node }

func (n andNode) eval(ec *evalCtx) (any, error) {
	l, err := ec.truth(n.l)
	if err != nil {
		return nil, err
	}
	if !l {
		return false, nil
	}
	return ec.truth(n.r)
}

type notNode struct{ inner node }

func (n notNode) eval(ec *evalCtx) (any, error) {
	v, err := ec.truth(n.inner)
	return !v, err
}

type cmpNode struct {
	op   string // 空串表示单操作数真值判断
	l, r node
}

func (n cmpNode) eval(ec *evalCtx) (any, error) {
	lv, err := n.l.eval(ec)
	if err != nil {
		return nil, err
	}
	if n.op == "" {
		return truthy(lv), nil
	}
	rv, err := n.r.eval(ec)
	if err != nil {
		return nil, err
	}
	switch n.op {
	case "==":
		return valuesEqual(lv, rv), nil
	case "!=":
		return !valuesEqual(lv, rv), nil
	case "contains":
		return strings.Contains(toString(lv), toString(rv)), nil
	case ">", ">=", "<", "<=":
		lf, lok := toFloat(lv)
		rf, rok := toFloat(rv)
		if !lok || !rok {
			return nil, fmt.Errorf("运算符 %s 需要数值操作数（左=%v 右=%v）", n.op, lv, rv)
		}
		switch n.op {
		case ">":
			return lf > rf, nil
		case ">=":
			return lf >= rf, nil
		case "<":
			return lf < rf, nil
		default:
			return lf <= rf, nil
		}
	}
	return nil, fmt.Errorf("未知运算符 %s", n.op)
}

type identNode struct{ name string }

func (n identNode) eval(ec *evalCtx) (any, error) {
	return ec.attr(n.name), nil
}

type numNode struct{ v float64 }

func (n numNode) eval(_ *evalCtx) (any, error) { return n.v, nil }

type strNode struct{ v string }

func (n strNode) eval(_ *evalCtx) (any, error) { return n.v, nil }

type callNode struct {
	name string
	args []string
}

func (n callNode) eval(ec *evalCtx) (any, error) {
	switch n.name {
	case "empty", "not_empty":
		if len(n.args) != 1 {
			return nil, fmt.Errorf("%s 需要 1 个属性参数", n.name)
		}
		empty := isEmptyAttr(ec.attr(n.args[0]))
		if n.name == "empty" {
			return empty, nil
		}
		return !empty, nil
	case "age_days":
		if len(n.args) != 1 {
			return nil, fmt.Errorf("age_days 需要 1 个属性参数")
		}
		v := ec.attr(n.args[0])
		if isEmptyAttr(v) {
			return math.Inf(1), nil // 无心跳时间即视为超期
		}
		t, err := parseTime(toString(v))
		if err != nil {
			return math.Inf(1), nil
		}
		return time.Since(t).Hours() / 24, nil
	case "unique":
		if len(n.args) != 1 {
			return nil, fmt.Errorf("unique 需要 1 个属性参数")
		}
		v := ec.attr(n.args[0])
		if isEmptyAttr(v) {
			return true, nil // 空值不参与唯一性判定
		}
		return ec.isUnique(n.args[0], v)
	case "biz_attributed":
		if len(n.args) != 0 {
			return nil, fmt.Errorf("biz_attributed 不接受参数")
		}
		return ec.bizAttributed()
	default:
		return nil, fmt.Errorf("未知函数 %s", n.name)
	}
}

// ---------- 解析 ----------

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token { t := p.toks[p.pos]; p.pos++; return t }

// parseExpr 解析完整表达式；返回语法树。
func parseExpr(s string) (node, error) {
	toks, err := tokenize(s)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokEOF {
		return nil, fmt.Errorf("表达式在 %q 处有多余内容", p.peek().text)
	}
	return n, nil
}

func (p *parser) parseOr() (node, error) {
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOp && p.peek().text == "or" {
		p.next()
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = orNode{l, r}
	}
	return l, nil
}

func (p *parser) parseAnd() (node, error) {
	l, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOp && p.peek().text == "and" {
		p.next()
		r, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		l = andNode{l, r}
	}
	return l, nil
}

func (p *parser) parseNot() (node, error) {
	if p.peek().kind == tokOp && p.peek().text == "not" {
		p.next()
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return notNode{inner}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (node, error) {
	if p.peek().kind == tokLParen {
		p.next()
		n, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tokRParen {
			return nil, fmt.Errorf("缺少右括号")
		}
		p.next()
		return n, nil
	}
	l, err := p.parseOperand()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == tokOp && t.text != "and" && t.text != "or" {
		p.next()
		r, err := p.parseOperand()
		if err != nil {
			return nil, err
		}
		return cmpNode{op: t.text, l: l, r: r}, nil
	}
	return cmpNode{l: l}, nil
}

// knownFuncs 是断言表达式支持的函数白名单（解析期校验）。
var knownFuncs = map[string]bool{
	"empty": true, "not_empty": true, "age_days": true, "unique": true, "biz_attributed": true,
}

func (p *parser) parseOperand() (node, error) {
	t := p.next()
	switch t.kind {
	case tokNumber:
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, fmt.Errorf("数值字面量 %q 非法", t.text)
		}
		return numNode{f}, nil
	case tokString:
		return strNode{t.text}, nil
	case tokIdent:
		if p.peek().kind == tokLParen {
			if !knownFuncs[t.text] {
				return nil, fmt.Errorf("未知函数 %s", t.text)
			}
			p.next()
			var args []string
			if p.peek().kind != tokRParen {
				for {
					a := p.next()
					if a.kind != tokIdent {
						return nil, fmt.Errorf("函数参数应为属性名，得到 %q", a.text)
					}
					args = append(args, a.text)
					if p.peek().kind == tokComma {
						p.next()
						continue
					}
					break
				}
			}
			if p.peek().kind != tokRParen {
				return nil, fmt.Errorf("函数调用缺少右括号")
			}
			p.next()
			return callNode{name: t.text, args: args}, nil
		}
		return identNode{t.text}, nil
	}
	return nil, fmt.Errorf("期望操作数，得到 %q", t.text)
}

// ---------- 求值上下文 ----------

// evalCtx 携带单个 CI 的求值上下文（含唯一性/归属判定所需的数据库句柄）。
type evalCtx struct {
	db        *gorm.DB
	ci        store.CI
	modelCode map[string]string // model_id → code 缓存
	peerIDs   map[string]string // 一跳对端 ci_id → model_code 缓存（归属判定）
	now       time.Time
}

func (ec *evalCtx) attr(name string) any {
	if ec.ci.Attributes == nil {
		return nil
	}
	return ec.ci.Attributes[name]
}

// truth 求值节点并转布尔。
func (ec *evalCtx) truth(n node) (bool, error) {
	v, err := n.eval(ec)
	if err != nil {
		return false, err
	}
	return truthy(v), nil
}

// isUnique 判定属性值在模型内（未退役 CI，排除自身）是否唯一。
func (ec *evalCtx) isUnique(attr string, v any) (bool, error) {
	var siblings []store.CI
	if err := ec.db.Where("model_id = ? AND status <> ? AND id <> ?", ec.ci.ModelID, "retired", ec.ci.ID).
		Find(&siblings).Error; err != nil {
		return false, fmt.Errorf("唯一性检查查询失败: %w", err)
	}
	for _, sib := range siblings {
		if valuesEqual(sib.Attributes[attr], v) {
			return false, nil
		}
	}
	return true, nil
}

// bizAttributed 判定 CI 是否已归属业务应用：一跳直达 biz_app，
// 或（K8s 工作负载场景）经 k8s_namespace 两跳继承。
func (ec *evalCtx) bizAttributed() (bool, error) {
	if err := ec.loadPeers(); err != nil {
		return false, err
	}
	for _, code := range ec.peerIDs {
		if code == "biz_app" {
			return true, nil
		}
	}
	// 两跳：peer 为 k8s_namespace 时，沿命名空间再找一跳。
	for peerID, code := range ec.peerIDs {
		if code != "k8s_namespace" {
			continue
		}
		second, err := ec.peerModelCodes(peerID)
		if err != nil {
			return false, err
		}
		for _, c2 := range second {
			if c2 == "biz_app" {
				return true, nil
			}
		}
	}
	return false, nil
}

// peerIDs 缓存当前 CI 的一跳对端（id → model_code），供两跳判定复用。
func (ec *evalCtx) loadPeers() error {
	if ec.peerIDs != nil {
		return nil
	}
	ec.peerIDs = map[string]string{}
	var rels []store.CIRelation
	if err := ec.db.Where("src_ci_id = ? OR dst_ci_id = ?", ec.ci.ID, ec.ci.ID).Find(&rels).Error; err != nil {
		return fmt.Errorf("查询关系失败: %w", err)
	}
	for _, rel := range rels {
		peerID := rel.DstCIID
		if rel.DstCIID == ec.ci.ID {
			peerID = rel.SrcCIID
		}
		var peer store.CI
		if err := ec.db.First(&peer, "id = ?", peerID).Error; err != nil {
			continue // 对端已删除，跳过
		}
		ec.peerIDs[peerID] = ec.modelCodeOf(peer.ModelID)
	}
	return nil
}

// peerModelCodes 返回指定 CI 一跳对端的模型编码列表。
func (ec *evalCtx) peerModelCodes(ciID string) ([]string, error) {
	var rels []store.CIRelation
	if err := ec.db.Where("src_ci_id = ? OR dst_ci_id = ?", ciID, ciID).Find(&rels).Error; err != nil {
		return nil, fmt.Errorf("查询关系失败: %w", err)
	}
	codes := []string{}
	for _, rel := range rels {
		peerID := rel.DstCIID
		if rel.DstCIID == ciID {
			peerID = rel.SrcCIID
		}
		var peer store.CI
		if err := ec.db.First(&peer, "id = ?", peerID).Error; err != nil {
			continue
		}
		codes = append(codes, ec.modelCodeOf(peer.ModelID))
	}
	return codes, nil
}

// modelCodeOf 按 model_id 取模型编码（带缓存）。
func (ec *evalCtx) modelCodeOf(modelID string) string {
	if code, ok := ec.modelCode[modelID]; ok {
		return code
	}
	var m store.Model
	if err := ec.db.First(&m, "id = ?", modelID).Error; err != nil {
		ec.modelCode[modelID] = ""
		return ""
	}
	ec.modelCode[modelID] = m.Code
	return m.Code
}

// ---------- 值工具 ----------

// truthy 按"非空即真"语义转布尔。
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return strings.TrimSpace(t) != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	}
	return true
}

// isEmptyAttr 判定属性值是否为空（nil 或空白字符串）。
func isEmptyAttr(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	return false
}

// toFloat 尝试把值转为 float64。
func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	}
	return 0, false
}

// toString 把值转为字符串表示。
func toString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

// valuesEqual 比较两个属性值：两边均可数值化时按数值比较，否则按字符串比较。
func valuesEqual(a, b any) bool {
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			return af == bf
		}
	}
	return toString(a) == toString(b)
}

// parseTime 解析日期属性（RFC3339 或 YYYY-MM-DD）。
func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}
