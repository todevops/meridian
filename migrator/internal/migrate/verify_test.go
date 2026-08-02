// verify 模式单测：进程内 NetBox 夹具 + 进程内 CMDB 夹具（复用 migrate_test.go 夹具），
// 覆盖三条路径——全一致（迁移后立即对账 100%）、CMDB 缺失、关键字段差异。
package migrate

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"migrator/internal/cmdb"
	"migrator/internal/netbox"
)

// runMigrationAndVerify 起夹具、跑迁移、再执行对账，返回对账报告与夹具。
func runMigrationAndVerify(t *testing.T) (*VerifyReport, *fakeCMDB) {
	t.Helper()
	nbStub := newNetboxFixture(t, false)
	t.Cleanup(nbStub.Close)
	fake := newFakeCMDB()
	cmStub := httptest.NewServer(fake.handler())
	t.Cleanup(cmStub.Close)

	ctx := context.Background()
	m := New(netbox.NewClient(nbStub.URL, "token"), cmdb.NewClient(cmStub.URL))
	if _, err := m.Run(ctx, nbStub.URL, cmStub.URL); err != nil {
		t.Fatalf("迁移致命错误: %v", err)
	}
	v := NewVerifier(netbox.NewClient(nbStub.URL, "token"), cmdb.NewClient(cmStub.URL))
	report, err := v.Run(ctx, nbStub.URL, cmStub.URL)
	if err != nil {
		t.Fatalf("对账致命错误: %v", err)
	}
	return report, fake
}

// verifyEntityStat 从对账报告取实体统计。
func verifyEntityStat(t *testing.T, r *VerifyReport, key string) VerifyEntityReport {
	t.Helper()
	for _, e := range r.Entities {
		if e.Entity == key {
			return e
		}
	}
	t.Fatalf("对账报告缺少实体 %s", key)
	return VerifyEntityReport{}
}

// TestVerifyConsistent 全一致路径：迁移后立即对账，七类全部匹配，一致率 100%。
func TestVerifyConsistent(t *testing.T) {
	report, _ := runMigrationAndVerify(t)

	if !report.FullyConsistent() {
		t.Fatalf("期望完全一致，实际: %s", report.Summary())
	}
	if report.ConsistencyRate != 100 {
		t.Fatalf("一致率应为 100，实际 %.2f", report.ConsistencyRate)
	}
	// 夹具七类实体数：站点 2、机架 1、设备 2、VLAN 2、VM 2、前缀 3、IP 3，合计 15。
	if report.TotalEntities != 15 || report.Consistent != 15 {
		t.Fatalf("总数/一致数应为 15/15，实际 %d/%d", report.TotalEntities, report.Consistent)
	}
	for _, key := range []string{"sites", "racks", "devices", "vlans", "virtual_machines", "prefixes", "ip_addresses"} {
		e := verifyEntityStat(t, report, key)
		if e.Matched != e.NetboxCount || e.MissingCount != 0 || e.DiffCount != 0 {
			t.Fatalf("实体 %s 应全部匹配，实际 NetBox=%d 匹配=%d 缺失=%d 差异=%d",
				key, e.NetboxCount, e.Matched, e.MissingCount, e.DiffCount)
		}
	}

	// 报告可落盘且摘要含一致率。
	path := filepath.Join(t.TempDir(), "verify-report.json")
	if err := report.WriteJSON(path); err != nil {
		t.Fatalf("写入对账报告失败: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("对账报告文件不存在: %v", err)
	}
	if !strings.Contains(report.Summary(), "100.00%") {
		t.Fatalf("摘要应含 100.00%%，实际:\n%s", report.Summary())
	}
}

// TestVerifyMissing 缺失路径：删掉一个 CI 与一个 IP 后对账，缺失计数正确且判不一致。
func TestVerifyMissing(t *testing.T) {
	report, fake := runMigrationAndVerify(t)
	_ = report

	// 制造缺失：删除 room CI（netbox_id=2）与一条 IP。
	kept := fake.cis[:0]
	for _, ci := range fake.cis {
		attrs, _ := ci["attributes"].(map[string]any)
		if ci["model_code"] == "room" && attrs["netbox_id"] == "2" {
			continue
		}
		kept = append(kept, ci)
	}
	fake.cis = kept
	fake.ips = fake.ips[1:]

	// 重新对账（复用夹具，NetBox 侧不变）。
	nbStub := newNetboxFixture(t, false)
	t.Cleanup(nbStub.Close)
	cmStub := httptest.NewServer(fake.handler())
	t.Cleanup(cmStub.Close)
	v := NewVerifier(netbox.NewClient(nbStub.URL, "token"), cmdb.NewClient(cmStub.URL))
	report2, err := v.Run(context.Background(), nbStub.URL, cmStub.URL)
	if err != nil {
		t.Fatalf("对账致命错误: %v", err)
	}

	if report2.FullyConsistent() {
		t.Fatalf("存在缺失时不应判一致: %s", report2.Summary())
	}
	sites := verifyEntityStat(t, report2, "sites")
	if sites.MissingCount != 1 || sites.Matched != 1 {
		t.Fatalf("sites 应缺失 1 匹配 1，实际缺失=%d 匹配=%d", sites.MissingCount, sites.Matched)
	}
	if len(sites.Missing) != 1 || sites.Missing[0].NetboxID != "2" || sites.Missing[0].Name != "无Slug机房" {
		t.Fatalf("sites 缺失明细不正确: %+v", sites.Missing)
	}
	ips := verifyEntityStat(t, report2, "ip_addresses")
	if ips.MissingCount != 1 || ips.Matched != 2 {
		t.Fatalf("ip_addresses 应缺失 1 匹配 2，实际缺失=%d 匹配=%d", ips.MissingCount, ips.Matched)
	}
	// 一致率：15 总数中 13 一致。
	if report2.Consistent != 13 || report2.ConsistencyRate != 86.67 {
		t.Fatalf("一致数/一致率应为 13/86.67，实际 %d/%.2f", report2.Consistent, report2.ConsistencyRate)
	}
}

// TestVerifyFieldDiff 字段差异路径：篡改一个 CI 关键字段后对账，差异明细正确且判不一致。
func TestVerifyFieldDiff(t *testing.T) {
	_, fake := runMigrationAndVerify(t)

	// 篡改 device（netbox_id=100）的 serial_no。
	mutated := false
	for _, ci := range fake.cis {
		attrs, _ := ci["attributes"].(map[string]any)
		if ci["model_code"] == "network_device" && attrs["netbox_id"] == "100" {
			attrs["serial_no"] = "SN-TAMPERED"
			mutated = true
		}
	}
	if !mutated {
		t.Fatal("未找到待篡改的设备 CI")
	}

	nbStub := newNetboxFixture(t, false)
	t.Cleanup(nbStub.Close)
	cmStub := httptest.NewServer(fake.handler())
	t.Cleanup(cmStub.Close)
	v := NewVerifier(netbox.NewClient(nbStub.URL, "token"), cmdb.NewClient(cmStub.URL))
	report, err := v.Run(context.Background(), nbStub.URL, cmStub.URL)
	if err != nil {
		t.Fatalf("对账致命错误: %v", err)
	}

	if report.FullyConsistent() {
		t.Fatalf("存在字段差异时不应判一致: %s", report.Summary())
	}
	devices := verifyEntityStat(t, report, "devices")
	if devices.DiffCount != 1 || devices.Matched != 1 || devices.MissingCount != 0 {
		t.Fatalf("devices 应差异 1 匹配 1 缺失 0，实际差异=%d 匹配=%d 缺失=%d",
			devices.DiffCount, devices.Matched, devices.MissingCount)
	}
	if len(devices.FieldDiffs) != 1 {
		t.Fatalf("差异明细应为 1 条，实际 %d 条", len(devices.FieldDiffs))
	}
	d := devices.FieldDiffs[0]
	if d.NetboxID != "100" || d.Name != "core-sw-01" || d.Field != "serial_no" ||
		d.NetboxValue != "SN-1" || d.CMDBValue != "SN-TAMPERED" {
		t.Fatalf("差异明细不正确: %+v", d)
	}
	// 仅 devices 类受影响，其余类仍全部一致。
	for _, key := range []string{"sites", "racks", "vlans", "virtual_machines", "prefixes", "ip_addresses"} {
		e := verifyEntityStat(t, report, key)
		if e.Matched != e.NetboxCount {
			t.Fatalf("实体 %s 不应受影响，实际匹配=%d 总数=%d", key, e.Matched, e.NetboxCount)
		}
	}
}
