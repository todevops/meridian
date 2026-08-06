// dbprobe 单测：内存假 SQL 驱动（sql.Register("fake", ...)）+ stub redis + httptest 夹具。
package dbprobe

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"collectors/internal/record"
)

// --- 测试辅助 ---

// fakeDriver 是内存假 SQL 驱动（复用 fixture 应答表实现），全局注册一次名为 "fake"。
var (
	fakeDriver = &fixtureDriver{}
	fakeOnce   sync.Once
)

// useFake 注册/换绑假驱动应答表（测试串行执行，未使用 t.Parallel）。
func useFake(store *fixtureStore) {
	fakeOnce.Do(func() { sql.Register("fake", fakeDriver) })
	fakeDriver.mu.Lock()
	fakeDriver.store = store
	fakeDriver.mu.Unlock()
}

// writeCreds 落盘凭据文件并返回路径。
func writeCreds(t *testing.T, entries []credEntry) string {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// storeOf 构造内存应答表：地址 → 查询串 → 应答（SQL 应答传 fixtureSQLResult，redis 传字符串）。
func storeOf(t *testing.T, data map[string]map[string]any) *fixtureStore {
	t.Helper()
	out := map[string]map[string]json.RawMessage{}
	for addr, queries := range data {
		out[addr] = map[string]json.RawMessage{}
		for q, v := range queries {
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			out[addr][q] = raw
		}
	}
	return &fixtureStore{data: out}
}

// sqlResult 是构造 SQL 应答的简写。
func sqlResult(cols []string, rows ...[]any) fixtureSQLResult {
	return fixtureSQLResult{Columns: cols, Rows: rows}
}

// logCapture 收集日志行。
type logCapture struct{ lines []string }

func (l *logCapture) logf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logCapture) contains(sub string) bool {
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// stubRedis 是内存 redis 客户端。
type stubRedis struct {
	sections map[string]string
}

func (s stubRedis) Info(_ context.Context, section string) (string, error) {
	v, ok := s.sections[section]
	if !ok {
		return "", fmt.Errorf("未知 section %q", section)
	}
	return v, nil
}

func (s stubRedis) Close() error { return nil }

// --- 凭据文件 ---

func TestCheckPerm(t *testing.T) {
	if err := checkPerm("linux", 0o600); err != nil {
		t.Fatalf("0600 应通过: %v", err)
	}
	if err := checkPerm("linux", 0o644); err == nil {
		t.Fatal("0644 应拒绝")
	}
	// Windows 无 POSIX 权限位语义，跳过位校验
	if err := checkPerm("windows", 0o666); err != nil {
		t.Fatalf("Windows 应跳过位校验: %v", err)
	}
}

func TestLoadCreds(t *testing.T) {
	entries := []credEntry{
		{InstanceAddr: "10.0.0.11:3306", Type: "mysql", Username: "readonly", Password: "s3cret"},
		{InstanceAddr: "10.0.0.21:6379", Type: "redis", Username: "", Password: ""},
	}
	// username 必填，修正第二条款
	entries[1].Username = "probe"
	got, err := loadCreds(writeCreds(t, entries))
	if err != nil {
		t.Fatalf("合法凭据文件应加载成功: %v", err)
	}
	if len(got) != 2 || got[0].InstanceAddr != "10.0.0.11:3306" {
		t.Fatalf("加载结果不符: %+v", got)
	}
	if _, err := loadCreds(""); err == nil {
		t.Fatal("空路径应报错")
	}
	if _, err := loadCreds(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("文件不存在应报错")
	}
	bad := writeCreds(t, []credEntry{{Type: "mysql", Username: "u"}})
	if _, err := loadCreds(bad); err == nil {
		t.Fatal("缺 instance_addr 应报错")
	}
}

// --- mysql 探测（内存假驱动） ---

func TestProbeMySQLMaster(t *testing.T) {
	useFake(storeOf(t, map[string]map[string]any{
		"10.0.0.11:3306": {
			qMySQLVersion: sqlResult([]string{"VERSION()"}, []any{"8.0.36"}),
			qMySQLReplica: sqlResult([]string{"Source_Host", "Source_Port"}), // 无复制行 = 主库/单机
			qMySQLSchemas: sqlResult([]string{"SCHEMA_NAME"},
				[]any{"orders"}, []any{"pay"}, []any{"mysql"}, []any{"information_schema"}),
		},
	}))
	version, masterAddr, schemaCount, err := probeMySQL(context.Background(), sql.Open, "fake", "10.0.0.11:3306")
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if version != "8.0.36" || masterAddr != "" || schemaCount != 2 {
		t.Fatalf("结果不符: version=%q master=%q schemas=%d", version, masterAddr, schemaCount)
	}
}

func TestProbeMySQLSlaveFallbackToSlaveStatus(t *testing.T) {
	// 旧版 mysql：无 SHOW REPLICA STATUS（fixture 未预置 = 查询报错），回退 SHOW SLAVE STATUS
	useFake(storeOf(t, map[string]map[string]any{
		"10.0.0.12:3306": {
			qMySQLVersion: sqlResult([]string{"VERSION()"}, []any{"5.7.44"}),
			qMySQLSlave:   sqlResult([]string{"Master_Host", "Master_Port"}, []any{"10.0.0.11", 3306}),
			qMySQLSchemas: sqlResult([]string{"SCHEMA_NAME"}, []any{"orders"}),
		},
	}))
	version, masterAddr, schemaCount, err := probeMySQL(context.Background(), sql.Open, "fake", "10.0.0.12:3306")
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if version != "5.7.44" || masterAddr != "10.0.0.11:3306" || schemaCount != 1 {
		t.Fatalf("结果不符: version=%q master=%q schemas=%d", version, masterAddr, schemaCount)
	}
}

func TestProbeMySQLConnectFailure(t *testing.T) {
	useFake(storeOf(t, map[string]map[string]any{}))
	if _, _, _, err := probeMySQL(context.Background(), sql.Open, "fake", "10.9.9.9:3306"); err == nil {
		t.Fatal("未知实例应连接失败")
	}
}

// --- redis 探测 ---

func TestParseInfo(t *testing.T) {
	kv := parseInfo("# Server\r\nredis_version:7.2.4\r\n\r\nrole:master\nconnected_slaves:2\n")
	if kv["redis_version"] != "7.2.4" || kv["role"] != "master" || kv["connected_slaves"] != "2" {
		t.Fatalf("解析不符: %v", kv)
	}
	if _, ok := kv["# Server"]; ok {
		t.Fatal("段标题不应入表")
	}
}

func TestProbeRedisSlave(t *testing.T) {
	cli := stubRedis{sections: map[string]string{
		"server":      "# Server\r\nredis_version:7.2.4\r\n",
		"replication": "# Replication\r\nrole:slave\r\nmaster_host:10.0.0.20\r\nmaster_port:6379\r\n",
	}}
	version, masterAddr, selfMaster, err := probeRedis(context.Background(), cli)
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if version != "7.2.4" || masterAddr != "10.0.0.20:6379" || selfMaster {
		t.Fatalf("结果不符: version=%q master=%q selfMaster=%v", version, masterAddr, selfMaster)
	}
}

func TestProbeRedisMaster(t *testing.T) {
	cli := stubRedis{sections: map[string]string{
		"server":      "redis_version:6.2.14\r\n",
		"replication": "role:master\r\nconnected_slaves:1\r\n",
	}}
	_, masterAddr, selfMaster, err := probeRedis(context.Background(), cli)
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if masterAddr != "" || !selfMaster {
		t.Fatalf("结果不符: master=%q selfMaster=%v", masterAddr, selfMaster)
	}
	// 无从库的 master 不算集群主库
	cli2 := stubRedis{sections: map[string]string{
		"server":      "redis_version:6.2.14\r\n",
		"replication": "role:master\r\nconnected_slaves:0\r\n",
	}}
	if _, _, self, err := probeRedis(context.Background(), cli2); err != nil || self {
		t.Fatalf("无从库 master 应 selfMaster=false: self=%v err=%v", self, err)
	}
}

// --- 拓扑归组 ---

func TestApplyTopology(t *testing.T) {
	results := []*probeResult{
		{addr: "10.0.0.11:3306", typ: "mysql"},                               // 被 B 指认 → master
		{addr: "10.0.0.12:3306", typ: "mysql", masterAddr: "10.0.0.11:3306"}, // slave
		{addr: "10.0.0.21:6379", typ: "redis"},                               // 无人指认 → standalone
		{addr: "10.0.0.22:6379", typ: "redis", masterAddr: "10.0.0.20:6379"}, // 主库未纳管：仅从库入组
	}
	applyTopology(results)
	byAddr := map[string]*probeResult{}
	for _, r := range results {
		byAddr[r.addr] = r
	}
	m := byAddr["10.0.0.11:3306"]
	if m.role != "master" || m.clusterName != "10.0.0.11:3306" ||
		len(m.clusterMate) != 1 || m.clusterMate[0] != "10.0.0.12:3306" {
		t.Fatalf("主库归组不符: %+v", m)
	}
	s := byAddr["10.0.0.12:3306"]
	if s.role != "slave" || s.clusterName != "10.0.0.11:3306" ||
		len(s.clusterMate) != 1 || s.clusterMate[0] != "10.0.0.11:3306" {
		t.Fatalf("从库归组不符: %+v", s)
	}
	st := byAddr["10.0.0.21:6379"]
	if st.role != "standalone" || st.clusterName != "" || st.clusterMate != nil {
		t.Fatalf("单机实例不应入组: %+v", st)
	}
	orphan := byAddr["10.0.0.22:6379"]
	if orphan.role != "slave" || orphan.clusterName != "10.0.0.20:6379" ||
		len(orphan.clusterMate) != 1 || orphan.clusterMate[0] != "10.0.0.20:6379" {
		t.Fatalf("主库未纳管的从库归组不符: %+v", orphan)
	}
}

// --- Collect 端到端（httptest CMDB + fixture 应答） ---

// newCMDBStub 返回一个提供 db_instance CI 清单的 httptest 服务。
func newCMDBStub(t *testing.T, items []ciItem) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/cis") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ciListResponse{Items: items})
	}))
}

func TestCollectWithFixture(t *testing.T) {
	items := []ciItem{
		{ID: "1", Status: "active", Attributes: map[string]any{"instance_addr": "10.0.0.11:3306", "component_type": "mysql"}},
		{ID: "2", Status: "active", Attributes: map[string]any{"instance_addr": "10.0.0.12:3306", "component_type": "mysql"}},
		{ID: "3", Status: "active", Attributes: map[string]any{"instance_addr": "10.0.0.21:6379", "component_type": "redis"}},
		{ID: "4", Status: "retired", Attributes: map[string]any{"instance_addr": "10.0.0.99:3306", "component_type": "mysql"}}, // 退役应跳过
		{ID: "5", Status: "active", Attributes: map[string]any{"instance_addr": "10.0.0.50:3306", "component_type": "mysql"}},  // 无凭据应跳过
	}
	srv := newCMDBStub(t, items)
	defer srv.Close()

	creds := writeCreds(t, []credEntry{
		{InstanceAddr: "10.0.0.11:3306", Type: "mysql", Username: "readonly", Password: "p1"},
		{InstanceAddr: "10.0.0.12:3306", Type: "mysql", Username: "readonly", Password: "p2"},
		{InstanceAddr: "10.0.0.21:6379", Type: "redis", Username: "probe", Password: "p3"},
	})
	logs := &logCapture{}
	c := New(srv.URL, "token", creds, false, logs.logf)
	c.UseFixture(storeOf(t, map[string]map[string]any{
		"10.0.0.11:3306": {
			qMySQLVersion: sqlResult([]string{"VERSION()"}, []any{"8.0.36"}),
			qMySQLReplica: sqlResult([]string{"Source_Host", "Source_Port"}),
			qMySQLSchemas: sqlResult([]string{"SCHEMA_NAME"}, []any{"orders"}, []any{"pay"}),
		},
		"10.0.0.12:3306": {
			qMySQLVersion: sqlResult([]string{"VERSION()"}, []any{"8.0.36"}),
			qMySQLReplica: sqlResult([]string{"Source_Host", "Source_Port"}, []any{"10.0.0.11", 3306}),
			qMySQLSchemas: sqlResult([]string{"SCHEMA_NAME"}, []any{"orders"}, []any{"pay"}),
		},
		"10.0.0.21:6379": {
			"INFO server":      "redis_version:7.2.4\r\n",
			"INFO replication": "role:master\r\nconnected_slaves:0\r\n",
		},
	}))

	recs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("应产出 3 条记录（退役/无凭据跳过），实得 %d: %+v", len(recs), recs)
	}
	if !logs.contains("无匹配凭据") {
		t.Fatalf("应记录无凭据跳过日志: %v", logs.lines)
	}
	byAddr := map[string]record.Record{}
	for _, r := range recs {
		byAddr[r.Attributes["instance_addr"].(string)] = r
		if r.Source != Source || r.Collector != CollectorName || r.ModelCandidate != "db_instance" {
			t.Fatalf("记录头不符: %+v", r)
		}
	}
	master := byAddr["10.0.0.11:3306"].Attributes
	if master["version"] != "8.0.36" || master["role"] != "master" ||
		master["cluster_name"] != "10.0.0.11:3306" || master["schema_count"] != 2 ||
		master["ip"] != "10.0.0.11" || master["port"] != 3306 {
		t.Fatalf("主库记录不符: %+v", master)
	}
	mates, ok := master["cluster_mates"].([]string)
	if !ok || len(mates) != 1 || mates[0] != "10.0.0.12:3306" {
		t.Fatalf("主库 cluster_mates 不符: %+v", master["cluster_mates"])
	}
	slave := byAddr["10.0.0.12:3306"].Attributes
	if slave["role"] != "slave" || slave["master_addr"] != "10.0.0.11:3306" ||
		slave["cluster_name"] != "10.0.0.11:3306" {
		t.Fatalf("从库记录不符: %+v", slave)
	}
	redisRec := byAddr["10.0.0.21:6379"].Attributes
	if redisRec["version"] != "7.2.4" || redisRec["role"] != "standalone" {
		t.Fatalf("redis 记录不符: %+v", redisRec)
	}
	if _, ok := redisRec["schema_count"]; ok {
		t.Fatal("redis 记录不应携带 schema_count")
	}
}

func TestCollectInstanceFailureContinues(t *testing.T) {
	items := []ciItem{
		{ID: "1", Status: "active", Attributes: map[string]any{"instance_addr": "10.0.0.11:3306", "component_type": "mysql"}},
		{ID: "2", Status: "active", Attributes: map[string]any{"instance_addr": "10.0.0.66:3306", "component_type": "mysql"}}, // fixture 未预置 = 连接失败
	}
	srv := newCMDBStub(t, items)
	defer srv.Close()
	creds := writeCreds(t, []credEntry{
		{InstanceAddr: "10.0.0.11:3306", Type: "mysql", Username: "readonly", Password: "p1"},
		{InstanceAddr: "10.0.0.66:3306", Type: "mysql", Username: "readonly", Password: "p2"},
	})
	logs := &logCapture{}
	c := New(srv.URL, "", creds, false, logs.logf)
	c.UseFixture(storeOf(t, map[string]map[string]any{
		"10.0.0.11:3306": {
			qMySQLVersion: sqlResult([]string{"VERSION()"}, []any{"8.0.36"}),
			qMySQLReplica: sqlResult([]string{"Source_Host"}),
			qMySQLSchemas: sqlResult([]string{"SCHEMA_NAME"}, []any{"orders"}),
		},
	}))
	recs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("单实例失败不应中断整体采集: %v", err)
	}
	if len(recs) != 1 || recs[0].Attributes["instance_addr"] != "10.0.0.11:3306" {
		t.Fatalf("应仅产出健康实例记录: %+v", recs)
	}
	if !logs.contains("10.0.0.66:3306 直连探测失败（不中断）") {
		t.Fatalf("应记录失败不中断日志: %v", logs.lines)
	}
	// 日志不得出现明文口令
	for _, l := range logs.lines {
		if strings.Contains(l, "p1") || strings.Contains(l, "p2") {
			t.Fatalf("日志泄露口令明文: %q", l)
		}
	}
}

func TestCollectDryRunFallbackToCredFile(t *testing.T) {
	// CMDB 不可达 + dry-run：回退凭据文件清单
	creds := writeCreds(t, []credEntry{
		{InstanceAddr: "10.0.0.21:6379", Type: "redis", Username: "probe", Password: "p3"},
	})
	logs := &logCapture{}
	c := New("http://127.0.0.1:1", "", creds, true, logs.logf)
	c.UseFixture(storeOf(t, map[string]map[string]any{
		"10.0.0.21:6379": {
			"INFO server":      "redis_version:7.2.4\r\n",
			"INFO replication": "role:slave\r\nmaster_host:10.0.0.20\r\nmaster_port:6379\r\n",
		},
	}))
	recs, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("dry-run 回退不应失败: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("应产出 1 条记录: %+v", recs)
	}
	attrs := recs[0].Attributes
	if attrs["role"] != "slave" || attrs["master_addr"] != "10.0.0.20:6379" {
		t.Fatalf("记录不符: %+v", attrs)
	}
	if !logs.contains("回退为凭据文件清单") {
		t.Fatalf("应记录回退日志: %v", logs.lines)
	}
}
