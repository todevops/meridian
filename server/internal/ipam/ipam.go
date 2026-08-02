// Package ipam 实现自研 IPAM 模块核心逻辑：
// 前缀（网段）树形管理（netip 解析、同级重叠检测）、IP 登记与顺序分配、
// 利用率统计（/31、/32 与 IPv6 大网段用 big.Int 计数防溢出，按 host 位计算）。
package ipam

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/netip"
	"strings"

	"gorm.io/gorm"

	"meridian/server/internal/store"
)

// 业务错误（HTTP 层据此映射状态码）。
var (
	ErrInvalidCIDR     = errors.New("CIDR 格式非法")
	ErrParentNotFound  = errors.New("父前缀不存在")
	ErrNotContained    = errors.New("子前缀必须落在父前缀网段内")
	ErrOverlap         = errors.New("与同级前缀网段重叠")
	ErrPrefixNotFound  = errors.New("前缀不存在")
	ErrInvalidIP       = errors.New("IP 地址格式非法")
	ErrIPNotInPrefix   = errors.New("IP 不在前缀网段内")
	ErrDuplicateIP     = errors.New("IP 已登记")
	ErrInsufficientIPs = errors.New("前缀内空闲 IP 不足")
	ErrInvalidStatus   = errors.New("status 取值非法（used/reserved）")
	ErrInvalidCount    = errors.New("count 必须为 1-1024 的整数")
	ErrInvalidVLAN     = errors.New("vlan_id 必须为 1-4094 的整数")
	ErrIPNotFound      = errors.New("IP 登记记录不存在")
)

// IP 状态枚举：free 不落库（未登记即空闲），仅持久化 used/reserved。
var ipStatuses = map[string]bool{"used": true, "reserved": true}

// MaxAllocateCount 是单次 allocate 的分配上限，防止误操作耗尽连接资源。
const MaxAllocateCount = 1024

// Service 是 IPAM 业务服务。
type Service struct {
	db *gorm.DB
}

// NewService 创建 IPAM 服务。
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// ---------- 纯函数（无状态，可独立单测） ----------

// ParsePrefix 解析并规范化 CIDR：host 位会被掩掉（10.0.0.1/24 → 10.0.0.0/24）。
func ParsePrefix(cidr string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%w: %s", ErrInvalidCIDR, cidr)
	}
	return p.Masked(), nil
}

// Overlaps 判定两个前缀网段是否有交集（互为包含即重叠）；
// IPv4 与 IPv6 属于不同地址族，永不重叠。
func Overlaps(a, b netip.Prefix) bool {
	if a.Addr().Is4() != b.Addr().Is4() {
		return false
	}
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

// TotalIPs 计算前缀可用 IP 总数（按 host 位，big.Int 防 IPv6 大网段溢出）：
//   - IPv4 /32 计 1、/31 计 2（点对点链路两端可用）；
//   - 其余 IPv4 前缀为 2^host 位 - 2（去掉网络地址与广播地址）；
//   - IPv6 无广播概念，计 2^host 位。
func TotalIPs(p netip.Prefix) *big.Int {
	hostBits := p.Addr().BitLen() - p.Bits()
	size := new(big.Int).Lsh(big.NewInt(1), uint(hostBits))
	if p.Addr().Is4() {
		switch {
		case hostBits == 0:
			return big.NewInt(1)
		case hostBits == 1:
			return big.NewInt(2)
		default:
			return new(big.Int).Sub(size, big.NewInt(2))
		}
	}
	return size
}

// usableRange 返回前缀的可用地址区间 [first, last]（语义同 TotalIPs）。
func usableRange(p netip.Prefix) (first, last netip.Addr) {
	is4 := p.Addr().Is4()
	hostBits := p.Addr().BitLen() - p.Bits()
	size := new(big.Int).Lsh(big.NewInt(1), uint(hostBits))
	firstNum := addrToBig(p.Masked().Addr())
	lastNum := new(big.Int).Sub(new(big.Int).Add(firstNum, size), big.NewInt(1))
	if is4 && hostBits >= 2 {
		firstNum.Add(firstNum, big.NewInt(1)) // 去掉网络地址
		lastNum.Sub(lastNum, big.NewInt(1))   // 去掉广播地址
	}
	return bigToAddr(firstNum, is4), bigToAddr(lastNum, is4)
}

// addrToBig 把 IP 地址转为大整数（v4 按 4 字节、v6 按 16 字节，两族互不混算）。
func addrToBig(a netip.Addr) *big.Int {
	if a.Is4() {
		b := a.As4()
		return new(big.Int).SetBytes(b[:])
	}
	b := a.As16()
	return new(big.Int).SetBytes(b[:])
}

// bigToAddr 把大整数还原为 IP 地址，is4 指定地址族。
func bigToAddr(n *big.Int, is4 bool) netip.Addr {
	b := n.Bytes()
	if is4 {
		var a [4]byte
		copy(a[4-len(b):], b)
		return netip.AddrFrom4(a)
	}
	var a [16]byte
	copy(a[16-len(b):], b)
	return netip.AddrFrom16(a)
}

// ---------- 前缀管理 ----------

// CreatePrefixInput 是创建前缀的入参。
type CreatePrefixInput struct {
	CIDR        string
	Name        string
	VlanID      *int
	Description string
	ParentID    *string
}

// CreatePrefix 创建前缀：CIDR 规范化后做父级包含与同级重叠校验。
func (s *Service) CreatePrefix(ctx context.Context, in CreatePrefixInput) (store.IPPrefix, error) {
	p, err := ParsePrefix(in.CIDR)
	if err != nil {
		return store.IPPrefix{}, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return store.IPPrefix{}, fmt.Errorf("name 为必填项")
	}
	if in.VlanID != nil && (*in.VlanID < 1 || *in.VlanID > 4094) {
		return store.IPPrefix{}, ErrInvalidVLAN
	}

	// 父前缀必须存在且新前缀落在其网段内。
	if in.ParentID != nil && *in.ParentID != "" {
		parent, err := s.getPrefix(ctx, *in.ParentID)
		if err != nil {
			if errors.Is(err, ErrPrefixNotFound) {
				return store.IPPrefix{}, fmt.Errorf("%w: %s", ErrParentNotFound, *in.ParentID)
			}
			return store.IPPrefix{}, err
		}
		parentP, _ := ParsePrefix(parent.CIDR) // 库内 CIDR 入库时已规范化
		if !parentP.Contains(p.Addr()) || p.Bits() < parentP.Bits() {
			return store.IPPrefix{}, fmt.Errorf("%w: %s 不在父前缀 %s 内", ErrNotContained, p, parent.CIDR)
		}
	}

	// 同级（同一父前缀下）不允许网段重叠；跨层级重叠属于正常树形嵌套。
	siblings, err := s.listSiblings(ctx, in.ParentID)
	if err != nil {
		return store.IPPrefix{}, err
	}
	for _, sib := range siblings {
		sibP, _ := ParsePrefix(sib.CIDR)
		if Overlaps(p, sibP) {
			return store.IPPrefix{}, fmt.Errorf("%w: %s 与同级前缀 %s 重叠", ErrOverlap, p, sib.CIDR)
		}
	}

	prefix := store.IPPrefix{
		CIDR:        p.String(),
		Name:        in.Name,
		VlanID:      in.VlanID,
		Description: in.Description,
		ParentID:    in.ParentID,
	}
	if err := s.db.WithContext(ctx).Create(&prefix).Error; err != nil {
		return store.IPPrefix{}, fmt.Errorf("创建前缀失败: %w", err)
	}
	return prefix, nil
}

// getPrefix 按 ID 加载前缀，不存在时返回 ErrPrefixNotFound。
func (s *Service) getPrefix(ctx context.Context, id string) (store.IPPrefix, error) {
	var prefix store.IPPrefix
	err := s.db.WithContext(ctx).First(&prefix, "id = ?", id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.IPPrefix{}, ErrPrefixNotFound
		}
		return store.IPPrefix{}, fmt.Errorf("查询前缀失败: %w", err)
	}
	return prefix, nil
}

// GetPrefix 按 ID 加载前缀并带出直接子前缀列表。
func (s *Service) GetPrefix(ctx context.Context, id string) (store.IPPrefix, []store.IPPrefix, error) {
	prefix, err := s.getPrefix(ctx, id)
	if err != nil {
		return store.IPPrefix{}, nil, err
	}
	var children []store.IPPrefix
	if err := s.db.WithContext(ctx).Where("parent_id = ?", prefix.ID).Order("cidr ASC").Find(&children).Error; err != nil {
		return store.IPPrefix{}, nil, fmt.Errorf("查询子前缀失败: %w", err)
	}
	return prefix, children, nil
}

// listSiblings 列出同级前缀（parentID 为 nil 时列出全部根前缀）。
func (s *Service) listSiblings(ctx context.Context, parentID *string) ([]store.IPPrefix, error) {
	q := s.db.WithContext(ctx).Model(&store.IPPrefix{})
	if parentID == nil || *parentID == "" {
		q = q.Where("parent_id IS NULL")
	} else {
		q = q.Where("parent_id = ?", *parentID)
	}
	var siblings []store.IPPrefix
	if err := q.Find(&siblings).Error; err != nil {
		return nil, fmt.Errorf("查询同级前缀失败: %w", err)
	}
	return siblings, nil
}

// ListPrefixes 按关键字（CIDR 或名称模糊）分页列出前缀。
func (s *Service) ListPrefixes(ctx context.Context, keyword string, page, pageSize int) (items []store.IPPrefix, total int64, err error) {
	q := s.db.WithContext(ctx).Model(&store.IPPrefix{})
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("cidr LIKE ? OR LOWER(name) LIKE LOWER(?)", like, like)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计前缀总数失败: %w", err)
	}
	if err := q.Order("cidr ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("查询前缀列表失败: %w", err)
	}
	return items, total, nil
}

// ---------- 利用率统计 ----------

// Utilization 是前缀利用率快照。TotalIPs 为十进制字符串（IPv6 大网段可能超出 int64）。
type Utilization struct {
	TotalIPs    string  `json:"total_ips"`   // 可用 IP 总数（十进制字符串，防溢出）
	UsedIPs     int64   `json:"used_ips"`    // 已登记 IP 数（used+reserved）
	Utilization float64 `json:"utilization"` // 利用率百分比（0-100）
}

// UsedCount 统计前缀下已登记 IP 数量。
func (s *Service) UsedCount(ctx context.Context, prefixID string) (int64, error) {
	var n int64
	if err := s.db.WithContext(ctx).Model(&store.IPAddress{}).Where("prefix_id = ?", prefixID).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("统计已用 IP 失败: %w", err)
	}
	return n, nil
}

// UtilizationOf 计算单个前缀的利用率。
func (s *Service) UtilizationOf(ctx context.Context, prefix store.IPPrefix) (Utilization, error) {
	used, err := s.UsedCount(ctx, prefix.ID)
	if err != nil {
		return Utilization{}, err
	}
	p, _ := ParsePrefix(prefix.CIDR) // 库内 CIDR 入库时已规范化
	return Utilization{
		TotalIPs:    TotalIPs(p).String(),
		UsedIPs:     used,
		Utilization: utilizationPercent(TotalIPs(p), used),
	}, nil
}

// utilizationPercent 用 big.Rat 精确计算百分比后转 float64（大网段下近似为 0 是正常精度损失）。
func utilizationPercent(total *big.Int, used int64) float64 {
	if total.Sign() == 0 {
		return 0
	}
	rat := new(big.Rat).SetInt(new(big.Int).Mul(big.NewInt(used), big.NewInt(100)))
	rat.Quo(rat, new(big.Rat).SetInt(total))
	f, _ := rat.Float64()
	return f
}

// ---------- IP 登记与分配 ----------

// CreateIPInput 是登记 IP 的入参。
type CreateIPInput struct {
	PrefixID    string
	IP          string
	Status      string
	CIID        string
	Description string
}

// CreateIP 在前缀内登记一个 IP：IP 必须落在前缀网段内，同前缀内不允许重复。
func (s *Service) CreateIP(ctx context.Context, in CreateIPInput) (store.IPAddress, error) {
	prefix, err := s.getPrefix(ctx, in.PrefixID)
	if err != nil {
		return store.IPAddress{}, err
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(in.IP))
	if err != nil {
		return store.IPAddress{}, fmt.Errorf("%w: %s", ErrInvalidIP, in.IP)
	}
	p, _ := ParsePrefix(prefix.CIDR)
	if !p.Contains(addr) {
		return store.IPAddress{}, fmt.Errorf("%w: %s 不在 %s 内", ErrIPNotInPrefix, addr, prefix.CIDR)
	}
	status := in.Status
	if status == "" {
		status = "used"
	}
	if !ipStatuses[status] {
		return store.IPAddress{}, ErrInvalidStatus
	}
	// 应用层预检给出友好错误；唯一索引兜底并发写入。
	var dup int64
	if err := s.db.WithContext(ctx).Model(&store.IPAddress{}).
		Where("prefix_id = ? AND ip = ?", prefix.ID, addr.String()).Count(&dup).Error; err != nil {
		return store.IPAddress{}, fmt.Errorf("检查 IP 重复失败: %w", err)
	}
	if dup > 0 {
		return store.IPAddress{}, fmt.Errorf("%w: %s 已在前缀 %s 内登记", ErrDuplicateIP, addr, prefix.CIDR)
	}
	ip := store.IPAddress{
		PrefixID:    prefix.ID,
		IP:          addr.String(),
		Status:      status,
		CIID:        in.CIID,
		Description: in.Description,
	}
	if err := s.db.WithContext(ctx).Create(&ip).Error; err != nil {
		if strings.Contains(err.Error(), "idx_ipam_prefix_ip") || strings.Contains(strings.ToLower(err.Error()), "unique") {
			return store.IPAddress{}, fmt.Errorf("%w: %s 已在前缀 %s 内登记", ErrDuplicateIP, addr, prefix.CIDR)
		}
		return store.IPAddress{}, fmt.Errorf("登记 IP 失败: %w", err)
	}
	return ip, nil
}

// Allocate 在前缀内顺序分配首个空闲起的 count 个 IP（status=used），整体原子：
// 空闲不足时不产生任何部分分配。
func (s *Service) Allocate(ctx context.Context, prefixID string, count int, description string) ([]store.IPAddress, error) {
	if count < 1 || count > MaxAllocateCount {
		return nil, ErrInvalidCount
	}
	prefix, err := s.getPrefix(ctx, prefixID)
	if err != nil {
		return nil, err
	}
	p, _ := ParsePrefix(prefix.CIDR)

	// 已登记 IP 构成占用集合，顺序扫描可用区间找前 count 个空位。
	var usedIPs []string
	if err := s.db.WithContext(ctx).Model(&store.IPAddress{}).
		Where("prefix_id = ?", prefix.ID).Pluck("ip", &usedIPs).Error; err != nil {
		return nil, fmt.Errorf("查询已登记 IP 失败: %w", err)
	}
	used := make(map[string]bool, len(usedIPs))
	for _, u := range usedIPs {
		used[u] = true
	}

	first, last := usableRange(p)
	picked := []store.IPAddress{}
	for cur := first; cur.IsValid() && cur.Compare(last) <= 0 && len(picked) < count; cur = cur.Next() {
		if used[cur.String()] {
			continue
		}
		picked = append(picked, store.IPAddress{
			PrefixID:    prefix.ID,
			IP:          cur.String(),
			Status:      "used",
			Description: description,
		})
	}
	if len(picked) < count {
		return nil, fmt.Errorf("%w: 前缀 %s 仅有 %d 个空闲 IP，无法满足 %d 个", ErrInsufficientIPs, prefix.CIDR, len(picked), count)
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range picked {
			if err := tx.Create(&picked[i]).Error; err != nil {
				return fmt.Errorf("写入分配结果失败: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return picked, nil
}

// ListIPs 按前缀/状态/关键字分页列出已登记 IP。
func (s *Service) ListIPs(ctx context.Context, prefixID, status, keyword string, page, pageSize int) (items []store.IPAddress, total int64, err error) {
	q := s.db.WithContext(ctx).Model(&store.IPAddress{})
	if prefixID != "" {
		q = q.Where("prefix_id = ?", prefixID)
	}
	if status != "" {
		if !ipStatuses[status] {
			return nil, 0, ErrInvalidStatus
		}
		q = q.Where("status = ?", status)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("ip LIKE ? OR LOWER(description) LIKE LOWER(?)", like, like)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计 IP 总数失败: %w", err)
	}
	if err := q.Order("ip ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("查询 IP 列表失败: %w", err)
	}
	return items, total, nil
}

// PatchIPInput 是更新 IP 的入参，nil 表示不更新该字段。
type PatchIPInput struct {
	Status      *string
	CIID        *string
	Description *string
}

// PatchIP 部分更新 IP 登记信息（状态/关联 CI/描述）。
func (s *Service) PatchIP(ctx context.Context, id string, in PatchIPInput) (store.IPAddress, error) {
	var ip store.IPAddress
	err := s.db.WithContext(ctx).First(&ip, "id = ?", id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return store.IPAddress{}, ErrIPNotFound
		}
		return store.IPAddress{}, fmt.Errorf("查询 IP 失败: %w", err)
	}
	updates := map[string]any{}
	if in.Status != nil {
		if !ipStatuses[*in.Status] {
			return store.IPAddress{}, ErrInvalidStatus
		}
		updates["status"] = *in.Status
	}
	if in.CIID != nil {
		updates["ci_id"] = *in.CIID
	}
	if in.Description != nil {
		updates["description"] = *in.Description
	}
	if len(updates) > 0 {
		if err := s.db.WithContext(ctx).Model(&store.IPAddress{}).Where("id = ?", ip.ID).Updates(updates).Error; err != nil {
			return store.IPAddress{}, fmt.Errorf("更新 IP 失败: %w", err)
		}
	}
	if err := s.db.WithContext(ctx).First(&ip, "id = ?", ip.ID).Error; err != nil {
		return store.IPAddress{}, fmt.Errorf("重新加载 IP 失败: %w", err)
	}
	return ip, nil
}

// RelinkCI 维护 CI 与 IPAM 地址登记（ip_addresses.ci_id）的关联，供调和等
// 写路径在自身事务内调用：IP 变更时先解除旧 IP 对本 CI 的挂载，再把新 IP 的
// 登记记录挂到 CI 上。只挂接已登记的 IP，不为未登记 IP 自动创建条目（登记权在 IPAM）。
// 重叠前缀/多 VRF 下同 IP 会命中多条登记记录，此时无法判定 CI 属于哪个前缀，
// 跳过挂接并告警（一条也不挂），绝不批量改挂。
func RelinkCI(tx *gorm.DB, ciID, oldIP, newIP string) error {
	if oldIP != "" && oldIP != newIP {
		if err := tx.Model(&store.IPAddress{}).
			Where("ip = ? AND ci_id = ?", oldIP, ciID).
			Update("ci_id", "").Error; err != nil {
			return fmt.Errorf("解除旧 IP %s 的 IPAM 关联失败: %w", oldIP, err)
		}
	}
	if newIP == "" {
		return nil
	}
	var rows []store.IPAddress
	if err := tx.Where("ip = ?", newIP).Find(&rows).Error; err != nil {
		return fmt.Errorf("查询 IP %s 的登记记录失败: %w", newIP, err)
	}
	switch len(rows) {
	case 0:
		// 未登记，不自动创建。
	case 1:
		if err := tx.Model(&store.IPAddress{}).
			Where("id = ?", rows[0].ID).
			Update("ci_id", ciID).Error; err != nil {
			return fmt.Errorf("建立 IP %s 的 IPAM 关联失败: %w", newIP, err)
		}
	default:
		log.Printf("IP %s 在 %d 个前缀下有登记记录，无法判定归属，跳过 CI %s 的 IPAM 挂接", newIP, len(rows), ciID)
	}
	return nil
}
