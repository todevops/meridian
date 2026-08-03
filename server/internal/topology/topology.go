// Package topology 实现网络拓扑域（F-061，3B）：
//   - 消费 network_link 发现记录（LLDP/CDP 邻居表，来源如 librenms）：
//     无对应模型定义，由调和引擎内建委托（reconcile.RegisterBuiltin）；
//     调和主键为 local_device+local_port+remote_device+remote_port 四元组，
//     同一链路重复上报幂等；按记录自动建 network_device↔network_device（或
//     host→network_device）的 connected_to 关系（source=auto 标注，
//     人工 manual 关系永不覆盖/删除）；
//   - GET /api/v1/topology：设备节点 + 链路边（双向互证合并去重）；
//   - GET /api/v1/topology/host-location：ARP/MAC 交叉定位主机接入端口
//     （ip→host CI 或 ip_scan 原始记录取 mac→network_link 记录命中交换机端口）。
package topology

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"meridian/server/internal/reconcile"
	"meridian/server/internal/store"
)

// 模型与关系编码常量。
const (
	modelNetworkDevice   = "network_device"
	modelHost            = "host"
	modelRack            = "rack"
	modelRoom            = "room"
	relConnectedTo       = "connected_to"
	relLocatedIn         = "located_in"
	candidateNetworkLink = "network_link"
)

// Node 是拓扑节点（契约形状）。
type Node struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ModelCode string `json:"model_code"`
	Room      string `json:"room"`
}

// Edge 是拓扑边（契约形状）：双向互证的两条记录合并为一条，
// a/b 为两端 CI ID，a_port/b_port 为对应端端口，source 为建档来源（auto/manual）。
type Edge struct {
	A      string `json:"a"`
	B      string `json:"b"`
	APort  string `json:"a_port"`
	BPort  string `json:"b_port"`
	Source string `json:"source"`
}

// Graph 是 GET /api/v1/topology 的响应。
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// HostLocation 是 GET /api/v1/topology/host-location 的响应。
type HostLocation struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	Switch   string `json:"switch"`
	Port     string `json:"port"`
	Protocol string `json:"protocol"`
}

// linkAttrs 是 network_link 记录的标准属性视图。
type linkAttrs struct {
	LocalDevice  string
	LocalPort    string
	RemoteDevice string
	RemotePort   string
	Protocol     string
	LocalMAC     string
	RemoteMAC    string
}

// Service 是拓扑域服务。
type Service struct {
	db *gorm.DB
}

// New 创建拓扑域服务。
func New(db *gorm.DB) *Service {
	return &Service{db: db}
}

// HandleLinkRecord 是 network_link 记录的内建调和处理器（注册到调和引擎）。
// 不产生 CI：按四元组幂等判定（create=首次上报，update=重复上报），
// 并按记录自动维护端点 CI 间的 connected_to 关系（source=auto，manual 关系不覆盖）。
func (s *Service) HandleLinkRecord(ctx context.Context, rec reconcile.Record, dryRun bool) (reconcile.Decision, error) {
	la := parseLinkAttrs(rec.Attributes)
	var missing []string
	for name, v := range map[string]string{
		"local_device": la.LocalDevice, "local_port": la.LocalPort,
		"remote_device": la.RemoteDevice, "remote_port": la.RemotePort,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return reconcile.Decision{
			Action:  reconcile.ActionPool,
			Reasons: []string{fmt.Sprintf("network_link 记录缺少必填属性 %s，转入发现池", strings.Join(missing, ", "))},
		}, nil
	}

	// 调和主键：local_device+local_port+remote_device+remote_port 四元组。
	// 原始层已有同键记录 → 重复上报（update 幂等），否则首次发现（create）。
	seen, err := s.linkRecordExists(ctx, la)
	if err != nil {
		return reconcile.Decision{}, err
	}
	d := reconcile.Decision{Action: reconcile.ActionCreate}
	if seen {
		d.Action = reconcile.ActionUpdate
		d.Reasons = append(d.Reasons, "链路四元组（local_device/local_port/remote_device/remote_port）与存量记录一致，幂等跳过建档")
	} else {
		d.Reasons = append(d.Reasons, "首次发现链路（四元组未命中存量记录）")
	}
	if dryRun {
		return d, nil
	}

	// 按记录自动建 connected_to 关系（manual 关系永不覆盖）。
	reason, err := s.ensureLinkRelation(ctx, la)
	if err != nil {
		return reconcile.Decision{}, err
	}
	d.Reasons = append(d.Reasons, reason)
	return d, nil
}

// linkRecordExists 按链路四元组查询原始层是否已有同键 network_link 记录。
func (s *Service) linkRecordExists(ctx context.Context, la linkAttrs) (bool, error) {
	var count int64
	q := s.db.WithContext(ctx).Model(&store.DiscoveryRawRecord{}).
		Where("model_candidate = ?", candidateNetworkLink)
	for k, v := range map[string]string{
		"local_device": la.LocalDevice, "local_port": la.LocalPort,
		"remote_device": la.RemoteDevice, "remote_port": la.RemotePort,
	} {
		q = q.Where(datatypes.JSONQuery("payload").Equals(v, "attributes", k))
	}
	if err := q.Count(&count).Error; err != nil {
		return false, fmt.Errorf("按链路四元组查询原始记录失败: %w", err)
	}
	return count > 0, nil
}

// ensureLinkRelation 按链路记录幂等建 connected_to 关系。
// 两端任一未解析到 CI 时跳过（仅留原始记录）；两端间已有 connected_to 关系
// （无论 manual 还是 auto、无论方向）时不覆盖——人工数据优先。
func (s *Service) ensureLinkRelation(ctx context.Context, la linkAttrs) (string, error) {
	localCI, localModel, err := s.resolveDevice(ctx, la.LocalDevice)
	if err != nil {
		return "", err
	}
	remoteCI, remoteModel, err := s.resolveDevice(ctx, la.RemoteDevice)
	if err != nil {
		return "", err
	}
	if localCI == nil || remoteCI == nil {
		unresolved := []string{}
		if localCI == nil {
			unresolved = append(unresolved, la.LocalDevice)
		}
		if remoteCI == nil {
			unresolved = append(unresolved, la.RemoteDevice)
		}
		return fmt.Sprintf("端点 %s 未解析到存量 CI，仅保留原始记录，待端点建档后由后续上报补链", strings.Join(unresolved, ", ")), nil
	}
	if localCI.ID == remoteCI.ID {
		return "链路两端解析为同一 CI，跳过建链", nil
	}

	// manual 保护：两端间任一方向已有 connected_to 关系即不覆盖。
	var count int64
	if err := s.db.WithContext(ctx).Model(&store.CIRelation{}).
		Where("relation_code = ? AND ((src_ci_id = ? AND dst_ci_id = ?) OR (src_ci_id = ? AND dst_ci_id = ?))",
			relConnectedTo, localCI.ID, remoteCI.ID, remoteCI.ID, localCI.ID).
		Count(&count).Error; err != nil {
		return "", fmt.Errorf("查询存量 connected_to 关系失败: %w", err)
	}
	if count > 0 {
		return fmt.Sprintf("两端（%s↔%s）已存在 connected_to 关系，自动关联不覆盖", la.LocalDevice, la.RemoteDevice), nil
	}

	// 方向：host 模型定义 connected_to→network_device 为 outgoing，
	// 一端为 host 时固定 host 为源；其余（设备↔设备）按记录方向 local→remote。
	src, dst := localCI.ID, remoteCI.ID
	if localModel != modelHost && remoteModel == modelHost {
		src, dst = dst, src
	}
	rel := store.CIRelation{
		RelationCode: relConnectedTo,
		SrcCIID:      src,
		DstCIID:      dst,
		Source:       store.RelationSourceAuto,
	}
	if err := s.db.WithContext(ctx).Create(&rel).Error; err != nil {
		return "", fmt.Errorf("创建 connected_to 关系失败: %w", err)
	}
	return fmt.Sprintf("自动建立 connected_to 关系（%s → %s，link_source=auto）", la.LocalDevice, la.RemoteDevice), nil
}

// resolveDevice 把链路记录中的设备名解析为存量 CI：
// 依次尝试 network_device 的 name/mgmt_ip、host 的 ident/ip。
// 未命中返回 (nil, "", nil)。
func (s *Service) resolveDevice(ctx context.Context, name string) (*store.CI, string, error) {
	for _, cand := range []struct {
		model string
		attrs []string
	}{
		{modelNetworkDevice, []string{"name", "mgmt_ip"}},
		{modelHost, []string{"ident", "ip"}},
	} {
		var model store.Model
		err := s.db.WithContext(ctx).Where("code = ?", cand.model).First(&model).Error
		if err == gorm.ErrRecordNotFound {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("查询模型 %s 失败: %w", cand.model, err)
		}
		for _, attr := range cand.attrs {
			var cis []store.CI
			if err := s.db.WithContext(ctx).
				Where("model_id = ? AND status <> ?", model.ID, "retired").
				Where(datatypes.JSONQuery("attributes").Equals(name, attr)).
				Limit(1).Find(&cis).Error; err != nil {
				return nil, "", fmt.Errorf("按 %s=%s 查询 %s CI 失败: %w", attr, name, cand.model, err)
			}
			if len(cis) > 0 {
				return &cis[0], cand.model, nil
			}
		}
	}
	return nil, "", nil
}

// edgeKey 是合并边的键：无序端点对（设备名+端口）。
type edgeKey struct{ lo, hi string }

// Graph 组装拓扑图：节点为全部未退役 network_device 及链路端点 CI；
// 边主要来自 network_link 原始记录（双向互证合并为一条），
// 无链路记录佐证的 connected_to 关系补为无边口信息的边（source 取关系建档来源）。
func (s *Service) Graph(ctx context.Context) (Graph, error) {
	links, err := s.loadLinkRecords(ctx)
	if err != nil {
		return Graph{}, err
	}

	// 端点设备名 → CI 解析（含边的全部设备名）。
	resolved := map[string]*store.CI{}
	resolve := func(name string) (*store.CI, error) {
		if ci, ok := resolved[name]; ok {
			return ci, nil
		}
		ci, _, err := s.resolveDevice(ctx, name)
		if err != nil {
			return nil, err
		}
		resolved[name] = ci
		return ci, nil
	}

	// 边合并：以无序端点对（设备+端口）为键，双向互证的两条记录合并为一条；
	// 字典序较小端固定为 a，保证输出稳定。
	endpoint := func(dev, port string) string { return dev + "\x00" + port }
	edges := map[edgeKey]*Edge{}
	for _, la := range links {
		lci, err := resolve(la.LocalDevice)
		if err != nil {
			return Graph{}, err
		}
		rci, err := resolve(la.RemoteDevice)
		if err != nil {
			return Graph{}, err
		}
		if lci == nil || rci == nil || lci.ID == rci.ID {
			continue // 端点未建档的链路不入图（原始记录保留可查）
		}
		lep, rep := endpoint(la.LocalDevice, la.LocalPort), endpoint(la.RemoteDevice, la.RemotePort)
		k := edgeKey{lo: lep, hi: rep}
		e := &Edge{A: lci.ID, B: rci.ID, APort: la.LocalPort, BPort: la.RemotePort, Source: store.RelationSourceAuto}
		if lep > rep {
			k.lo, k.hi = rep, lep
			e.A, e.B, e.APort, e.BPort = e.B, e.A, e.BPort, e.APort
		}
		edges[k] = e // 同键后者覆盖前者（内容等价，幂等）
	}

	// connected_to 关系补充：无链路记录佐证的端点对补边（端口信息缺失，source 取关系来源）。
	var rels []store.CIRelation
	if err := s.db.WithContext(ctx).Where("relation_code = ?", relConnectedTo).Find(&rels).Error; err != nil {
		return Graph{}, fmt.Errorf("查询 connected_to 关系失败: %w", err)
	}
	ciByID, err := s.loadCIsByID(ctx)
	if err != nil {
		return Graph{}, err
	}
	for _, rel := range rels {
		src, ok1 := ciByID[rel.SrcCIID]
		dst, ok2 := ciByID[rel.DstCIID]
		if !ok1 || !ok2 {
			continue
		}
		lep, rep := endpoint(ciNodeName(src), ""), endpoint(ciNodeName(dst), "")
		k := edgeKey{lo: lep, hi: rep}
		if lep > rep {
			k.lo, k.hi = rep, lep
		}
		// 链路记录边（含端口）优先；仅有同设备对的关系边且链路边已覆盖该设备对时跳过。
		if _, exists := edges[k]; exists {
			continue
		}
		if pairCoveredByLink(edges, rel.SrcCIID, rel.DstCIID) {
			continue
		}
		src2 := rel.Source
		if src2 == "" {
			src2 = store.RelationSourceManual
		}
		e := Edge{A: rel.SrcCIID, B: rel.DstCIID, Source: src2}
		if lep > rep {
			e.A, e.B = e.B, e.A
		}
		edges[k] = &e
	}

	// 节点：全部未退役 network_device + 边端点涉及的其它模型 CI。
	nodeIDs := map[string]bool{}
	for _, e := range edges {
		nodeIDs[e.A], nodeIDs[e.B] = true, true
	}
	var ndModel store.Model
	if err := s.db.WithContext(ctx).Where("code = ?", modelNetworkDevice).First(&ndModel).Error; err != nil && err != gorm.ErrRecordNotFound {
		return Graph{}, fmt.Errorf("查询 network_device 模型失败: %w", err)
	}
	if ndModel.ID != "" {
		var nds []store.CI
		if err := s.db.WithContext(ctx).Where("model_id = ? AND status <> ?", ndModel.ID, "retired").Find(&nds).Error; err != nil {
			return Graph{}, fmt.Errorf("查询 network_device CI 失败: %w", err)
		}
		for _, nd := range nds {
			nodeIDs[nd.ID] = true
			ciByID[nd.ID] = nd
		}
	}

	rooms, err := s.deviceRooms(ctx)
	if err != nil {
		return Graph{}, err
	}
	modelCodes, err := s.modelCodesByID(ctx)
	if err != nil {
		return Graph{}, err
	}
	g := Graph{Nodes: []Node{}, Edges: []Edge{}}
	for id := range nodeIDs {
		ci, ok := ciByID[id]
		if !ok {
			continue
		}
		g.Nodes = append(g.Nodes, Node{
			ID:        id,
			Name:      ciNodeName(ci),
			ModelCode: modelCodes[ci.ModelID],
			Room:      rooms[id],
		})
	}
	for _, e := range edges {
		g.Edges = append(g.Edges, *e)
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].Name < g.Nodes[j].Name })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].A != g.Edges[j].A {
			return g.Edges[i].A < g.Edges[j].A
		}
		return g.Edges[i].B < g.Edges[j].B
	})
	return g, nil
}

// pairCoveredByLink 判定两 CI 间是否已有链路记录产生的边（关系补充边不重复覆盖）。
func pairCoveredByLink(edges map[edgeKey]*Edge, a, b string) bool {
	for _, e := range edges {
		if (e.A == a && e.B == b) || (e.A == b && e.B == a) {
			return true
		}
	}
	return false
}

// HostLocation ARP/MAC 交叉定位主机接入端口：
// ip→host CI（或 ip_scan 原始记录）取 mac→network_link 记录中 local/remote 命中
// → 返回对端交换机与端口。任一环未命中返回 (nil, nil)（由 HTTP 层转 404）。
func (s *Service) HostLocation(ctx context.Context, ip string) (*HostLocation, error) {
	normIP := ip
	if addr, err := netip.ParseAddr(strings.TrimSpace(ip)); err == nil {
		normIP = addr.String()
	}

	// 1. ip → mac：先查 host CI，再回退 ip_scan 原始记录。
	mac, err := s.macForIP(ctx, normIP)
	if err != nil {
		return nil, err
	}
	if mac == "" {
		return nil, nil // 无命中
	}

	// 2. mac → network_link 记录：local/remote 任一端命中（MAC 归一化比较），
	// 取命中最新的记录；命中 remote 侧时 local 侧为交换机，反之亦然。
	normMAC := normalizeMAC(mac)
	links, err := s.loadLinkRecords(ctx)
	if err != nil {
		return nil, err
	}
	best := -1
	bestRemote := false
	for i, la := range links {
		if normalizeMAC(la.RemoteMAC) == normMAC || normalizeMAC(la.RemoteDevice) == normMAC {
			best, bestRemote = i, true
		} else if normalizeMAC(la.LocalMAC) == normMAC || normalizeMAC(la.LocalDevice) == normMAC {
			if best < 0 {
				best, bestRemote = i, false
			}
		}
	}
	if best < 0 {
		return nil, nil
	}
	la := links[best]
	loc := &HostLocation{IP: normIP, MAC: mac, Protocol: la.Protocol}
	if bestRemote {
		loc.Switch, loc.Port = la.LocalDevice, la.LocalPort
	} else {
		loc.Switch, loc.Port = la.RemoteDevice, la.RemotePort
	}
	return loc, nil
}

// macForIP 解析 IP 对应的 MAC：优先 host CI 的 mac 属性，
// 其次最近一条携带该 ip 与 mac 的原始发现记录（如 ip_scan）。
func (s *Service) macForIP(ctx context.Context, ip string) (string, error) {
	var hostModel store.Model
	err := s.db.WithContext(ctx).Where("code = ?", modelHost).First(&hostModel).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return "", fmt.Errorf("查询 host 模型失败: %w", err)
	}
	if hostModel.ID != "" {
		var hosts []store.CI
		if err := s.db.WithContext(ctx).
			Where("model_id = ? AND status <> ?", hostModel.ID, "retired").
			Where(datatypes.JSONQuery("attributes").Equals(ip, "ip")).
			Find(&hosts).Error; err != nil {
			return "", fmt.Errorf("按 ip 查询 host CI 失败: %w", err)
		}
		for _, h := range hosts {
			if mac := attrStr(h.Attributes, "mac"); mac != "" {
				return mac, nil
			}
		}
	}
	// 回退：原始发现记录（ip_scan 等来源的 host 记录携带 ip+mac）。
	var raws []store.DiscoveryRawRecord
	if err := s.db.WithContext(ctx).
		Where("model_candidate = ?", modelHost).
		Where(datatypes.JSONQuery("payload").Equals(ip, "attributes", "ip")).
		Order("received_at DESC").Limit(20).Find(&raws).Error; err != nil {
		return "", fmt.Errorf("按 ip 查询原始发现记录失败: %w", err)
	}
	for _, raw := range raws {
		if mac := attrStr(payloadAttrs(raw.Payload), "mac"); mac != "" {
			return mac, nil
		}
	}
	return "", nil
}

// loadLinkRecords 加载全部 network_link 原始记录并解析为标准属性视图（按采集时间升序）。
func (s *Service) loadLinkRecords(ctx context.Context) ([]linkAttrs, error) {
	var raws []store.DiscoveryRawRecord
	if err := s.db.WithContext(ctx).
		Where("model_candidate = ?", candidateNetworkLink).
		Order("occurred_at ASC").Find(&raws).Error; err != nil {
		return nil, fmt.Errorf("查询 network_link 原始记录失败: %w", err)
	}
	links := make([]linkAttrs, 0, len(raws))
	for _, raw := range raws {
		la := parseLinkAttrs(payloadAttrs(raw.Payload))
		if la.LocalDevice == "" || la.RemoteDevice == "" {
			continue
		}
		links = append(links, la)
	}
	return links, nil
}

// loadCIsByID 加载全部未退役 CI 并以 ID 索引（拓扑图规模有限，全量装载换简单）。
func (s *Service) loadCIsByID(ctx context.Context) (map[string]store.CI, error) {
	var cis []store.CI
	if err := s.db.WithContext(ctx).Where("status <> ?", "retired").Find(&cis).Error; err != nil {
		return nil, fmt.Errorf("查询 CI 失败: %w", err)
	}
	out := make(map[string]store.CI, len(cis))
	for _, ci := range cis {
		out[ci.ID] = ci
	}
	return out, nil
}

// modelCodesByID 返回模型 ID → 模型编码映射。
func (s *Service) modelCodesByID(ctx context.Context) (map[string]string, error) {
	var models []store.Model
	if err := s.db.WithContext(ctx).Find(&models).Error; err != nil {
		return nil, fmt.Errorf("查询模型失败: %w", err)
	}
	out := make(map[string]string, len(models))
	for _, m := range models {
		out[m.ID] = m.Code
	}
	return out, nil
}

// deviceRooms 解析 network_device CI 所在机房：沿 located_in→rack→located_in→room 两跳。
func (s *Service) deviceRooms(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	var ndModel, rackModel, roomModel store.Model
	for code, m := range map[string]*store.Model{modelNetworkDevice: &ndModel, modelRack: &rackModel, modelRoom: &roomModel} {
		err := s.db.WithContext(ctx).Where("code = ?", code).First(m).Error
		if err == gorm.ErrRecordNotFound {
			return out, nil // 模型未就位（机房信息缺省为空）
		}
		if err != nil {
			return nil, fmt.Errorf("查询模型 %s 失败: %w", code, err)
		}
	}
	var rels []store.CIRelation
	if err := s.db.WithContext(ctx).Where("relation_code = ?", relLocatedIn).Find(&rels).Error; err != nil {
		return nil, fmt.Errorf("查询 located_in 关系失败: %w", err)
	}
	ciByID, err := s.loadCIsByID(ctx)
	if err != nil {
		return nil, err
	}
	// rack → room 映射。
	rackRoom := map[string]string{}
	for _, rel := range rels {
		rack, ok := ciByID[rel.SrcCIID]
		if !ok || rack.ModelID != rackModel.ID {
			continue
		}
		if room, ok := ciByID[rel.DstCIID]; ok && room.ModelID == roomModel.ID {
			rackRoom[rack.ID] = attrStr(room.Attributes, "name")
		}
	}
	// network_device → rack → room。
	for _, rel := range rels {
		dev, ok := ciByID[rel.SrcCIID]
		if !ok || dev.ModelID != ndModel.ID {
			continue
		}
		if roomName, ok := rackRoom[rel.DstCIID]; ok {
			out[dev.ID] = roomName
		}
	}
	return out, nil
}

// parseLinkAttrs 从记录属性解析链路标准视图。
func parseLinkAttrs(attrs map[string]any) linkAttrs {
	return linkAttrs{
		LocalDevice:  attrStr(attrs, "local_device"),
		LocalPort:    attrStr(attrs, "local_port"),
		RemoteDevice: attrStr(attrs, "remote_device"),
		RemotePort:   attrStr(attrs, "remote_port"),
		Protocol:     attrStr(attrs, "protocol"),
		LocalMAC:     attrStr(attrs, "local_mac"),
		RemoteMAC:    attrStr(attrs, "remote_mac"),
	}
}

// payloadAttrs 取原始记录 payload 中的 attributes 段。
func payloadAttrs(payload datatypes.JSONMap) map[string]any {
	if m, ok := payload["attributes"].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// ciNodeName 取 CI 的节点显示名：name/ident/mgmt_ip/serial_no 依次回退，最后为 ID。
func ciNodeName(ci store.CI) string {
	for _, attr := range []string{"name", "ident", "mgmt_ip", "serial_no"} {
		if v := attrStr(ci.Attributes, attr); v != "" {
			return v
		}
	}
	return ci.ID
}

// attrStr 读取字符串属性（去空白）；非字符串或空返回 ""。
func attrStr(attrs map[string]any, key string) string {
	v, ok := attrs[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// normalizeMAC 归一化 MAC/标识串：去分隔符（: - .）并转小写，
// 兼容 "52:54:00:AB:CD:EF"、"5254.00ab.cdef"、"04f9c81a0101" 等形态。
func normalizeMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(":", "", "-", "", ".", "").Replace(s)
	return s
}
