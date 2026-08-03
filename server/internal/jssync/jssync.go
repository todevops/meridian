// Package jssync 实现 JumpServer 资产同步器（F-071）：
// 筛选"在用 + 有业务归属"的 host CI，按 biz_line/biz_app 组成 JumpServer 节点路径
// 创建/更新资产（name=ident、address=主 IP、platform=linux）；已同步但本环境
// 已退役/失归属的 IP 资产调 disable 禁用。支持 dry_run 预演与每日定时兜底。
// 资产-CI 映射以 address(ip) 为键；客户端解析复用凭据库 type=jumpserver
// 或环境变量 JUMPSERVER_URL/JUMPSERVER_TOKEN。
package jssync

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"meridian/server/internal/credentials"
	"meridian/server/internal/jumpserver"
	"meridian/server/internal/lifecycle"
	"meridian/server/internal/store"
)

// 同步涉及的关系码：主机的业务归属由 biz_app.deployed_on→host（归属引擎）
// 或人工 belongs_to 建联表达，两者均视为"有业务归属"。
var attributionRelCodes = []string{"deployed_on", "belongs_to"}

// Syncer 是 JumpServer 资产同步器。
type Syncer struct {
	db     *gorm.DB
	client *jumpserver.Client // 可空：未配置时 Sync 返回错误
}

// New 创建同步器。
func New(db *gorm.DB, client *jumpserver.Client) *Syncer {
	return &Syncer{db: db, client: client}
}

// Result 是一次同步的结果计数与错误明细。
type Result struct {
	Created  int      `json:"created"`
	Updated  int      `json:"updated"`
	Disabled int      `json:"disabled"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors"`
}

// ResolveClient 解析 JumpServer 客户端：
// 优先凭据库 type=jumpserver 的凭据（secret {"url"/"base_url","token"}），
// 其次环境变量 JUMPSERVER_URL / JUMPSERVER_TOKEN；都未配置返回 nil。
func ResolveClient(db *gorm.DB, cipher *credentials.Cipher) *jumpserver.Client {
	if cipher != nil {
		var cred store.Credential
		if err := db.Where("type = ?", store.CredentialTypeJumpServer).First(&cred).Error; err == nil {
			if plain, err := cipher.Decrypt(cred.SecretCiphertext); err == nil {
				var secret map[string]any
				if json.Unmarshal(plain, &secret) == nil {
					baseURL, _ := secret["url"].(string)
					if baseURL == "" {
						baseURL, _ = secret["base_url"].(string)
					}
					token, _ := secret["token"].(string)
					if baseURL != "" {
						return jumpserver.NewClient(normalizeURL(baseURL), token)
					}
				}
			}
		}
	}
	if baseURL := os.Getenv("JUMPSERVER_URL"); baseURL != "" {
		return jumpserver.NewClient(normalizeURL(baseURL), os.Getenv("JUMPSERVER_TOKEN"))
	}
	return nil
}

// normalizeURL 把 ":19010"、"localhost:19010" 等简写规范化为完整 URL。
func normalizeURL(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, ":") {
		return "http://localhost" + v
	}
	if v != "" && !strings.Contains(v, "://") {
		return "http://" + v
	}
	return v
}

// attrString 读取字符串属性（去空白）。
func attrString(attrs map[string]any, key string) string {
	v, ok := attrs[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// desiredAsset 是一台主机期望的 JumpServer 资产形态。
type desiredAsset struct {
	host   store.CI
	name   string
	ip     string
	nodeID string // 期望归属节点（解析不到时为空，不约束 nodes 字段）
}

// Sync 执行一轮同步。dryRun 为真时只计算动作不写入 JumpServer。
func (s *Syncer) Sync(ctx context.Context, dryRun bool) (Result, error) {
	res := Result{Errors: []string{}}
	if s.client == nil {
		return res, fmt.Errorf("JumpServer 未配置（凭据 type=jumpserver 或 JUMPSERVER_URL/JUMPSERVER_TOKEN）")
	}

	// 一、装载 CMDB 侧数据：host 模型、全部 host CI（含退役，禁用扫描要用）、
	// 业务归属关系（deployed_on/belongs_to 入向）与应用→业务线归属（各一次批量查询）。
	var hostModel store.Model
	if err := s.db.WithContext(ctx).First(&hostModel, "code = ?", "host").Error; err != nil {
		return res, fmt.Errorf("host 模型不存在: %w", err)
	}
	var hosts []store.CI
	if err := s.db.WithContext(ctx).Where("model_id = ?", hostModel.ID).Find(&hosts).Error; err != nil {
		return res, fmt.Errorf("查询 host CI 失败: %w", err)
	}
	hostByID := map[string]store.CI{}
	hostByIP := map[string]store.CI{}
	hostIDs := make([]string, 0, len(hosts))
	for _, h := range hosts {
		hostByID[h.ID] = h
		hostIDs = append(hostIDs, h.ID)
		if ip := attrString(h.Attributes, "ip"); ip != "" {
			hostByIP[ip] = h
		}
	}

	apps, lines, err := s.loadBizTree(ctx)
	if err != nil {
		return res, err
	}
	// 主机 → 归属应用清单（入向归属关系）。
	hostApps := map[string][]string{}
	if len(hostIDs) > 0 {
		var rels []store.CIRelation
		if err := s.db.WithContext(ctx).
			Where("relation_code IN ? AND dst_ci_id IN ?", attributionRelCodes, hostIDs).
			Find(&rels).Error; err != nil {
			return res, fmt.Errorf("查询主机归属关系失败: %w", err)
		}
		for _, r := range rels {
			if _, isApp := apps[r.SrcCIID]; isApp {
				hostApps[r.DstCIID] = append(hostApps[r.DstCIID], r.SrcCIID)
			}
		}
	}

	// 二、筛选同步对象：非退役 + 有业务归属 + 有主 IP。
	var desired []desiredAsset
	for _, h := range hosts {
		if h.Status == lifecycle.StatusRetired {
			continue
		}
		appIDs := hostApps[h.ID]
		if len(appIDs) == 0 {
			continue
		}
		ip := attrString(h.Attributes, "ip")
		if ip == "" {
			continue
		}
		// 多归属时按应用编码取第一个（确定性），节点路径 /Default/{业务线}/{应用}。
		sort.Strings(appIDs)
		app := apps[appIDs[0]]
		nodePath := "/Default"
		if lineID := app.lineID; lineID != "" {
			if line, ok := lines[lineID]; ok {
				nodePath += "/" + line
			}
		}
		if app.name != "" {
			nodePath += "/" + app.name
		}
		desired = append(desired, desiredAsset{
			host: h,
			name: attrString(h.Attributes, "ident"),
			ip:   ip,
			// nodePath 暂存，节点清单读回后解析为 nodeID
			nodeID: nodePath,
		})
	}

	// 三、读回 JumpServer 侧状态：资产（按 address 建键）与节点清单。
	assets, err := s.client.ListAssets(ctx)
	if err != nil {
		return res, fmt.Errorf("拉取 JumpServer 资产失败: %w", err)
	}
	assetByIP := map[string]jumpserver.Asset{}
	for _, a := range assets {
		assetByIP[a.Address] = a
	}
	nodes, err := s.client.ListNodes(ctx)
	if err != nil {
		return res, fmt.Errorf("拉取 JumpServer 节点失败: %w", err)
	}

	// 四、创建/更新分支。
	managedIPs := map[string]bool{} // 本轮"在用+已归属"的 IP 集合
	for _, d := range desired {
		managedIPs[d.ip] = true
		nodeID := resolveNodeID(nodes, d.nodeID)
		existing, ok := assetByIP[d.ip]
		if !ok {
			res.Created += s.apply(ctx, dryRun, &res, d.ip, func() error {
				_, err := s.client.CreateAsset(ctx, jumpserver.Asset{
					Name:     d.name,
					Address:  d.ip,
					Platform: "linux",
					Nodes:    nodeList(nodeID),
				})
				return err
			})
			continue
		}
		// 更新分支：漂移（name/platform/节点）或已禁用需重新启用。
		patch := map[string]any{}
		if existing.Name != d.name {
			patch["name"] = d.name
		}
		if existing.Platform != "linux" {
			patch["platform"] = "linux"
		}
		if nodeID != "" && !sameNodes(existing.Nodes, nodeID) {
			patch["nodes"] = []string{nodeID}
		}
		if !existing.IsActive {
			patch["is_active"] = true
		}
		if len(patch) == 0 {
			res.Skipped++
			continue
		}
		res.Updated += s.apply(ctx, dryRun, &res, d.ip, func() error {
			return s.client.UpdateAsset(ctx, existing.ID, patch)
		})
	}

	// 五、禁用分支：已同步（address 命中本环境 host CI）但该主机已退役/失归属的启用资产。
	for _, a := range assets {
		if !a.IsActive || managedIPs[a.Address] {
			continue
		}
		if _, known := hostByIP[a.Address]; !known {
			continue // 非本环境主机资产（外部纳管），不动
		}
		res.Disabled += s.apply(ctx, dryRun, &res, a.Address, func() error {
			return s.client.DisableAsset(ctx, a.ID, a.IsActive)
		})
	}
	return res, nil
}

// apply 执行一个写动作：dry_run 只计数；写失败记 errors 不计数。返回计数增量。
func (s *Syncer) apply(ctx context.Context, dryRun bool, res *Result, key string, fn func() error) int {
	if dryRun {
		return 1
	}
	if err := fn(); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", key, err))
		return 0
	}
	return 1
}

// bizApp 是同步用的应用条目（含其业务线归属）。
type bizApp struct {
	name   string
	lineID string
}

// loadBizTree 加载 biz_app/biz_line 清单与应用→业务线归属（各一次批量查询）。
func (s *Syncer) loadBizTree(ctx context.Context) (map[string]bizApp, map[string]string, error) {
	var models []store.Model
	if err := s.db.WithContext(ctx).Where("code IN ?", []string{"biz_app", "biz_line"}).Find(&models).Error; err != nil {
		return nil, nil, fmt.Errorf("加载业务模型失败: %w", err)
	}
	modelID := map[string]string{}
	for _, m := range models {
		modelID[m.Code] = m.ID
	}
	apps := map[string]bizApp{}
	lines := map[string]string{}
	if modelID["biz_app"] == "" {
		return apps, lines, nil // 无业务模型：全部主机视为无归属
	}
	var appCIs []store.CI
	if err := s.db.WithContext(ctx).Where("model_id = ?", modelID["biz_app"]).Find(&appCIs).Error; err != nil {
		return nil, nil, fmt.Errorf("查询应用失败: %w", err)
	}
	appIDs := make([]string, 0, len(appCIs))
	for _, a := range appCIs {
		apps[a.ID] = bizApp{name: attrString(a.Attributes, "name")}
		appIDs = append(appIDs, a.ID)
	}
	if modelID["biz_line"] != "" {
		var lineCIs []store.CI
		if err := s.db.WithContext(ctx).Where("model_id = ?", modelID["biz_line"]).Find(&lineCIs).Error; err != nil {
			return nil, nil, fmt.Errorf("查询业务线失败: %w", err)
		}
		for _, l := range lineCIs {
			lines[l.ID] = attrString(l.Attributes, "name")
		}
	}
	if len(appIDs) > 0 {
		var rels []store.CIRelation
		if err := s.db.WithContext(ctx).
			Where("relation_code = ? AND src_ci_id IN ?", "belongs_to", appIDs).
			Find(&rels).Error; err != nil {
			return nil, nil, fmt.Errorf("查询应用业务线归属失败: %w", err)
		}
		for _, r := range rels {
			if app, ok := apps[r.SrcCIID]; ok {
				app.lineID = r.DstCIID
				apps[r.SrcCIID] = app
			}
		}
	}
	return apps, lines, nil
}

// resolveNodeID 把期望节点路径（/Default/业务线/应用）解析为节点 ID：
// 精确匹配 full_value 优先；无精确匹配时降级最长祖先前缀；都没有返回空串
// （空串表示不约束 nodes 字段——节点树尚未建对应分组时不阻断资产同步）。
func resolveNodeID(nodes []jumpserver.Node, fullPath string) string {
	best := ""
	bestLen := -1
	for _, n := range nodes {
		fv := n.FullValue
		if fv == fullPath {
			return n.ID
		}
		// 祖先前缀匹配（"/Default/电商平台" 是 "/Default/电商平台/商城前台" 的祖先）。
		if strings.HasPrefix(fullPath, fv+"/") || fv == "/Default" && strings.HasPrefix(fullPath, "/Default/") {
			if len(fv) > bestLen {
				best, bestLen = n.ID, len(fv)
			}
		}
	}
	return best
}

// nodeList 组装节点 ID 列表（空串归为空数组）。
func nodeList(nodeID string) []string {
	if nodeID == "" {
		return []string{}
	}
	return []string{nodeID}
}

// sameNodes 判定资产节点归属是否为期望的单节点。
func sameNodes(current []string, want string) bool {
	return len(current) == 1 && current[0] == want
}

// RunDailyLoop 每日兜底对账：启动即跑一轮，之后每 24 小时一轮，直到 ctx 取消。
// 每轮重新解析客户端（凭据可能轮换）；未配置 JumpServer 时记日志跳过。
func RunDailyLoop(ctx context.Context, db *gorm.DB, cipher *credentials.Cipher) {
	run := func() {
		client := ResolveClient(db, cipher)
		if client == nil {
			log.Println("JumpServer 每日同步跳过：未配置（凭据 type=jumpserver 或 JUMPSERVER_URL/JUMPSERVER_TOKEN）")
			return
		}
		res, err := New(db, client).Sync(ctx, false)
		if err != nil {
			log.Printf("JumpServer 每日同步失败: %v", err)
			return
		}
		log.Printf("JumpServer 每日同步完成: 新建 %d、更新 %d、禁用 %d、跳过 %d、错误 %d",
			res.Created, res.Updated, res.Disabled, res.Skipped, len(res.Errors))
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
