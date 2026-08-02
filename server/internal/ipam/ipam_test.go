// IPAM 单测：CIDR 解析/重叠/容量纯函数 + 前缀树/IP 登记/分配的服务分支（含 409 冲突）。
package ipam

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"cmdb/server/internal/store"
)

// setup 打开独立内存库并完成全量迁移。
func setup(t *testing.T) (*gorm.DB, *Service) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db, NewService(db)
}

// mustCreatePrefix 创建前缀，失败即终止测试。
func mustCreatePrefix(t *testing.T, s *Service, cidr, name string, parentID *string) store.IPPrefix {
	t.Helper()
	p, err := s.CreatePrefix(context.Background(), CreatePrefixInput{CIDR: cidr, Name: name, ParentID: parentID})
	if err != nil {
		t.Fatalf("创建前缀 %s 失败: %v", cidr, err)
	}
	return p
}

// ---------- 纯函数 ----------

func TestParsePrefix(t *testing.T) {
	// host 位被掩掉，规范化为网络地址。
	p, err := ParsePrefix("10.0.0.1/24")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.String() != "10.0.0.0/24" {
		t.Fatalf("期望 10.0.0.0/24，得到 %s", p)
	}
	if _, err := ParsePrefix("10.0.0.300/24"); !errors.Is(err, ErrInvalidCIDR) {
		t.Fatalf("非法 CIDR 期望 ErrInvalidCIDR，得到 %v", err)
	}
	if _, err := ParsePrefix("not-a-cidr"); !errors.Is(err, ErrInvalidCIDR) {
		t.Fatalf("期望 ErrInvalidCIDR，得到 %v", err)
	}
}

func TestTotalIPs(t *testing.T) {
	cases := []struct {
		cidr string
		want string
	}{
		{"10.0.0.0/24", "254"}, // 常规 IPv4 去掉网络/广播
		{"10.0.0.0/30", "2"},   // 小子网
		{"10.0.0.0/31", "2"},   // 点对点链路两端可用
		{"10.0.0.1/32", "1"},   // 单主机
		{"::/0", new(big.Int).Lsh(big.NewInt(1), 128).String()},         // IPv6 全空间
		{"2001:db8::/64", new(big.Int).Lsh(big.NewInt(1), 64).String()}, // IPv6 大网段（超出 int64）
		{"2001:db8::1/128", "1"},                                        // IPv6 单地址
	}
	for _, tc := range cases {
		p, err := ParsePrefix(tc.cidr)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", tc.cidr, err)
		}
		if got := TotalIPs(p).String(); got != tc.want {
			t.Errorf("TotalIPs(%s) = %s，期望 %s", tc.cidr, got, tc.want)
		}
	}
}

func TestOverlaps(t *testing.T) {
	mustParse := func(s string) netip.Prefix {
		p, err := ParsePrefix(s)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", s, err)
		}
		return p
	}
	cases := []struct {
		a, b string
		want bool
	}{
		{"10.0.0.0/24", "10.0.0.128/25", true},  // 包含关系
		{"10.0.0.128/25", "10.0.0.0/24", true},  // 反向包含
		{"10.0.0.0/24", "10.0.1.0/24", false},   // 不相交
		{"10.0.0.0/25", "10.0.0.128/25", false}, // 相邻不重叠
		{"10.0.0.0/24", "2001:db8::/64", false}, // 地址族不同
	}
	for _, tc := range cases {
		if got := Overlaps(mustParse(tc.a), mustParse(tc.b)); got != tc.want {
			t.Errorf("Overlaps(%s, %s) = %v，期望 %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// ---------- 前缀树 ----------

func TestCreatePrefixBranches(t *testing.T) {
	_, s := setup(t)
	ctx := context.Background()

	// 非法 CIDR → ErrInvalidCIDR。
	if _, err := s.CreatePrefix(ctx, CreatePrefixInput{CIDR: "bad", Name: "x"}); !errors.Is(err, ErrInvalidCIDR) {
		t.Fatalf("期望 ErrInvalidCIDR，得到 %v", err)
	}
	// 父前缀不存在 → ErrParentNotFound。
	missing := "00000000-0000-0000-0000-000000000000"
	if _, err := s.CreatePrefix(ctx, CreatePrefixInput{CIDR: "10.9.0.0/24", Name: "x", ParentID: &missing}); !errors.Is(err, ErrParentNotFound) {
		t.Fatalf("期望 ErrParentNotFound，得到 %v", err)
	}

	root := mustCreatePrefix(t, s, "10.0.0.0/16", "根网段", nil)

	// 同级重叠 → ErrOverlap（/17 与 /16 根不是同级，先建一个根级前缀制造重叠）。
	if _, err := s.CreatePrefix(ctx, CreatePrefixInput{CIDR: "10.0.0.0/8", Name: "重叠根"}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("期望 ErrOverlap，得到 %v", err)
	}
	// 不相交根级前缀可建。
	mustCreatePrefix(t, s, "172.16.0.0/16", "另一根", nil)

	// 子前缀落在父内：正常；与兄弟子前缀重叠：409。
	child := mustCreatePrefix(t, s, "10.0.1.0/24", "子网A", &root.ID)
	if _, err := s.CreatePrefix(ctx, CreatePrefixInput{CIDR: "10.0.1.128/25", Name: "子网A重叠", ParentID: &root.ID}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("期望兄弟重叠 ErrOverlap，得到 %v", err)
	}
	// 子前缀跳出父网段 → ErrNotContained。
	if _, err := s.CreatePrefix(ctx, CreatePrefixInput{CIDR: "10.1.0.0/24", Name: "越界子网", ParentID: &root.ID}); !errors.Is(err, ErrNotContained) {
		t.Fatalf("期望 ErrNotContained，得到 %v", err)
	}
	// 与兄弟不相交的子前缀可建；孙前缀落在子前缀内可建（跨层级不查重叠）。
	mustCreatePrefix(t, s, "10.0.2.0/24", "子网B", &root.ID)
	mustCreatePrefix(t, s, "10.0.1.0/25", "孙网段", &child.ID)

	// GetPrefix 应带出直接子前缀。
	_, children, err := s.GetPrefix(ctx, root.ID)
	if err != nil {
		t.Fatalf("GetPrefix 失败: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("期望 2 个直接子前缀，得到 %d", len(children))
	}
	if _, _, err := s.GetPrefix(ctx, missing); !errors.Is(err, ErrPrefixNotFound) {
		t.Fatalf("期望 ErrPrefixNotFound，得到 %v", err)
	}
}

// ---------- IP 登记与分配 ----------

func TestCreateIPBranches(t *testing.T) {
	_, s := setup(t)
	ctx := context.Background()
	p := mustCreatePrefix(t, s, "10.0.0.0/24", "业务网段", nil)

	// 前缀不存在 → ErrPrefixNotFound。
	if _, err := s.CreateIP(ctx, CreateIPInput{PrefixID: "nope", IP: "10.0.0.1"}); !errors.Is(err, ErrPrefixNotFound) {
		t.Fatalf("期望 ErrPrefixNotFound，得到 %v", err)
	}
	// IP 非法 → ErrInvalidIP。
	if _, err := s.CreateIP(ctx, CreateIPInput{PrefixID: p.ID, IP: "999.1.1.1"}); !errors.Is(err, ErrInvalidIP) {
		t.Fatalf("期望 ErrInvalidIP，得到 %v", err)
	}
	// IP 不在前缀内 → ErrIPNotInPrefix（HTTP 400）。
	if _, err := s.CreateIP(ctx, CreateIPInput{PrefixID: p.ID, IP: "10.0.1.1"}); !errors.Is(err, ErrIPNotInPrefix) {
		t.Fatalf("期望 ErrIPNotInPrefix，得到 %v", err)
	}
	// 正常登记；重复登记 → ErrDuplicateIP（HTTP 409）。
	if _, err := s.CreateIP(ctx, CreateIPInput{PrefixID: p.ID, IP: "10.0.0.10"}); err != nil {
		t.Fatalf("登记 IP 失败: %v", err)
	}
	if _, err := s.CreateIP(ctx, CreateIPInput{PrefixID: p.ID, IP: "10.0.0.10"}); !errors.Is(err, ErrDuplicateIP) {
		t.Fatalf("期望 ErrDuplicateIP，得到 %v", err)
	}
	// 非法状态 → ErrInvalidStatus。
	if _, err := s.CreateIP(ctx, CreateIPInput{PrefixID: p.ID, IP: "10.0.0.11", Status: "free"}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("期望 ErrInvalidStatus，得到 %v", err)
	}

	// 利用率：/24 总 254，已登记 1。
	util, err := s.UtilizationOf(ctx, p)
	if err != nil {
		t.Fatalf("UtilizationOf 失败: %v", err)
	}
	if util.TotalIPs != "254" || util.UsedIPs != 1 {
		t.Fatalf("利用率快照不符: %+v", util)
	}
}

func TestAllocateSequentialAndExhaustion(t *testing.T) {
	_, s := setup(t)
	ctx := context.Background()
	// /30 仅 2 个可用地址（.1 .2）。
	p := mustCreatePrefix(t, s, "192.168.1.0/30", "点对点", nil)

	// count 非法 → ErrInvalidCount。
	if _, err := s.Allocate(ctx, p.ID, 0, ""); !errors.Is(err, ErrInvalidCount) {
		t.Fatalf("期望 ErrInvalidCount，得到 %v", err)
	}
	// 顺序分配首个空闲 IP。
	ips, err := s.Allocate(ctx, p.ID, 1, "上联口")
	if err != nil {
		t.Fatalf("分配失败: %v", err)
	}
	if len(ips) != 1 || ips[0].IP != "192.168.1.1" {
		t.Fatalf("期望分配 192.168.1.1，得到 %+v", ips)
	}
	// 再分配跳过已用，取 .2。
	ips, err = s.Allocate(ctx, p.ID, 1, "")
	if err != nil {
		t.Fatalf("分配失败: %v", err)
	}
	if ips[0].IP != "192.168.1.2" {
		t.Fatalf("期望分配 192.168.1.2，得到 %s", ips[0].IP)
	}
	// 网段耗尽 → ErrInsufficientIPs（HTTP 409），且不产生部分分配。
	if _, err := s.Allocate(ctx, p.ID, 1, ""); !errors.Is(err, ErrInsufficientIPs) {
		t.Fatalf("期望 ErrInsufficientIPs，得到 %v", err)
	}
	var total int64
	if err := s.db.Model(&store.IPAddress{}).Where("prefix_id = ?", p.ID).Count(&total).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if total != 2 {
		t.Fatalf("耗善后不应新增登记，期望 2 条，得到 %d", total)
	}
}

func TestAllocateSkipsManuallyRegistered(t *testing.T) {
	_, s := setup(t)
	ctx := context.Background()
	p := mustCreatePrefix(t, s, "10.10.0.0/29", "小网段", nil)

	// 手工登记 .1 与 .3 后，顺序分配应取 .2 再取 .4。
	if _, err := s.CreateIP(ctx, CreateIPInput{PrefixID: p.ID, IP: "10.10.0.1"}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if _, err := s.CreateIP(ctx, CreateIPInput{PrefixID: p.ID, IP: "10.10.0.3"}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	ips, err := s.Allocate(ctx, p.ID, 2, "")
	if err != nil {
		t.Fatalf("分配失败: %v", err)
	}
	if ips[0].IP != "10.10.0.2" || ips[1].IP != "10.10.0.4" {
		t.Fatalf("期望分配 .2 与 .4，得到 %s %s", ips[0].IP, ips[1].IP)
	}
}

func TestPatchIP(t *testing.T) {
	_, s := setup(t)
	ctx := context.Background()
	p := mustCreatePrefix(t, s, "10.20.0.0/24", "办公网", nil)
	ip, err := s.CreateIP(ctx, CreateIPInput{PrefixID: p.ID, IP: "10.20.0.8", Description: "打印机"})
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}

	reserved := "reserved"
	desc := "会议室打印机"
	updated, err := s.PatchIP(ctx, ip.ID, PatchIPInput{Status: &reserved, Description: &desc})
	if err != nil {
		t.Fatalf("PatchIP 失败: %v", err)
	}
	if updated.Status != "reserved" || updated.Description != desc {
		t.Fatalf("更新结果不符: %+v", updated)
	}
	// 非法状态 → ErrInvalidStatus；记录不存在 → ErrIPNotFound。
	bad := "offline"
	if _, err := s.PatchIP(ctx, ip.ID, PatchIPInput{Status: &bad}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("期望 ErrInvalidStatus，得到 %v", err)
	}
	if _, err := s.PatchIP(ctx, "nope", PatchIPInput{Status: &reserved}); !errors.Is(err, ErrIPNotFound) {
		t.Fatalf("期望 ErrIPNotFound，得到 %v", err)
	}
}
