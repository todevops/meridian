// Package dbprobe 实现数据库实例直连补采采集器（4A DBMS 治理深化）：
// 从 CMDB 读取 db_instance 实例清单（GET /api/v1/cis?model_id=db_instance，MERIDIAN_* 认证），
// 使用本地凭据文件（DBPROBE_CRED_FILE，0600）中的只读账号直连 mysql/redis，
// 补采精确版本、主从角色、集群拓扑与 schema 数，产出 db_instance 标准发现记录。
// 单实例连接/查询失败记日志不中断整体采集。
package dbprobe

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"

	"collectors/internal/record"
)

const (
	// Source 是发现来源系统标识。
	Source = "dbprobe"
	// CollectorName 是采集器标识。
	CollectorName = "db-prober"
)

// instance 是待探测实例（来自 CMDB 实例清单或凭据文件回退）。
type instance struct {
	addr string // instance_addr（ip:port）
	typ  string // component_type（cred 未指定类型时的回退）
}

// probeResult 是单实例探测结果（拓扑归组前的原始产出）。
type probeResult struct {
	addr        string
	typ         string
	version     string
	masterAddr  string   // 从库身份时其主库地址
	selfMaster  bool     // redis INFO 自报 master 且有从库
	role        string   // 归组后填充：master|slave|standalone
	clusterName string   // 归组后填充：主从集群名（=主库地址）
	clusterMate []string // 归组后填充：同集群其他实例地址
	schemaCount int      // mysql 有效
}

// Collector 是数据库实例直连补采采集器。
type Collector struct {
	apiURL    string
	cmdbToken string
	credFile  string
	dryRun    bool
	http      *http.Client
	// 可注入依赖：生产默认 sql.Open("mysql", ...) 与 go-redis 拨号；测试/fixture 替换。
	openSQL   func(driverName, dsn string) (*sql.DB, error)
	sqlDriver string
	dialRedis func(addr, username, password string) redisClient
	now       func() time.Time
	logf      func(format string, args ...any)
}

// New 创建采集器。credFile 为本地凭据文件（0600）；dryRun 下 CMDB 清单不可达时回退凭据文件清单。
func New(apiURL, cmdbToken, credFile string, dryRun bool, logf func(format string, args ...any)) *Collector {
	return &Collector{
		apiURL:    record.NormalizeBaseURL(apiURL),
		cmdbToken: cmdbToken,
		credFile:  credFile,
		dryRun:    dryRun,
		http:      &http.Client{Timeout: 30 * time.Second},
		openSQL:   sql.Open,
		sqlDriver: "mysql",
		dialRedis: dialRedis,
		now:       time.Now,
		logf:      logf,
	}
}

// UseFixture 切换为 fixture 应答模式（DBPROBE_FIXTURE_FILE）：SQL 走内存假驱动，
// redis 走内存 INFO 应答，不发起任何真实数据库连接。
func (c *Collector) UseFixture(store *fixtureStore) {
	c.sqlDriver = RegisterFixtureDriver(store)
	c.dialRedis = func(addr, _, _ string) redisClient {
		return &fixtureRedisClient{store: store, addr: addr}
	}
}

// Name 返回采集器名。
func (c *Collector) Name() string { return "dbprobe" }

// Collect 读取实例清单 → 逐实例直连探测 → 拓扑归组 → 产出发现记录。
func (c *Collector) Collect(ctx context.Context) ([]record.Record, error) {
	creds, err := loadCreds(c.credFile)
	if err != nil {
		return nil, err
	}
	instances, err := c.fetchInstances(ctx)
	if err != nil {
		if !c.dryRun {
			return nil, err
		}
		c.logf("[dry-run] 从 CMDB 获取实例清单失败，回退为凭据文件清单: %v", err)
		instances = credInstances(creds)
	}
	var results []*probeResult
	for _, inst := range instances {
		cred, ok := findCred(creds, inst.addr)
		if !ok {
			c.logf("实例 %s 无匹配凭据，跳过", inst.addr)
			continue
		}
		res, err := c.probe(ctx, inst, cred)
		if err != nil {
			c.logf("实例 %s 直连探测失败（不中断）: %v", inst.addr, err)
			continue
		}
		results = append(results, res)
	}
	applyTopology(results)
	recs := make([]record.Record, 0, len(results))
	for _, r := range results {
		recs = append(recs, c.toRecord(r))
	}
	return recs, nil
}

// probe 按类型分发探测（单实例超时收紧，避免拖垮整轮采集）。
func (c *Collector) probe(ctx context.Context, inst instance, cred credEntry) (*probeResult, error) {
	typ := cred.Type
	if typ == "" {
		typ = inst.typ
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res := &probeResult{addr: inst.addr, typ: typ}
	switch typ {
	case "mysql":
		dsn := mysqlDSN(cred)
		if c.sqlDriver != "mysql" {
			dsn = inst.addr // 假驱动/fixture 以实例地址本身为 DSN 主键
		}
		v, m, n, err := probeMySQL(ctx, c.openSQL, c.sqlDriver, dsn)
		if err != nil {
			return nil, err
		}
		res.version, res.masterAddr, res.schemaCount = v, m, n
	case "redis":
		cli := c.dialRedis(inst.addr, cred.Username, cred.Password)
		defer cli.Close()
		v, m, self, err := probeRedis(ctx, cli)
		if err != nil {
			return nil, err
		}
		res.version, res.masterAddr, res.selfMaster = v, m, self
	default:
		return nil, fmt.Errorf("不支持的实例类型 %q（仅支持 mysql|redis）", typ)
	}
	return res, nil
}

// applyTopology 按 masterAddr 分组推断角色与集群：
// 从库角色由复制行直接确定；被从库指认或自报有从库的为 master，其余为 standalone。
// 集群以主库地址命名（cluster_name=主库地址），组内 ≥2 地址时互填 cluster_mates。
func applyTopology(results []*probeResult) {
	groups := map[string][]string{} // 主库地址 → 从库地址列表
	referenced := map[string]bool{}
	for _, r := range results {
		if r.masterAddr != "" {
			r.role = "slave"
			referenced[r.masterAddr] = true
			groups[r.masterAddr] = append(groups[r.masterAddr], r.addr)
		}
	}
	for _, r := range results {
		if r.masterAddr == "" {
			if referenced[r.addr] || r.selfMaster {
				r.role = "master"
			} else {
				r.role = "standalone"
			}
		}
	}
	for master, slaves := range groups {
		members := append(slices.Clone(slaves), master)
		slices.Sort(members)
		for _, r := range results {
			if r.masterAddr != master && r.addr != master {
				continue
			}
			r.clusterName = master
			r.clusterMate = nil
			for _, m := range members {
				if m != r.addr {
					r.clusterMate = append(r.clusterMate, m)
				}
			}
		}
	}
}

// toRecord 映射为 db_instance 标准发现记录。
func (c *Collector) toRecord(r *probeResult) record.Record {
	attrs := map[string]any{
		"instance_addr":  r.addr,
		"component_type": r.typ,
		"version":        r.version,
		"role":           r.role,
		"source":         Source,
	}
	if ip, port, err := net.SplitHostPort(r.addr); err == nil {
		attrs["ip"] = ip
		if n, err := strconv.Atoi(port); err == nil && n > 0 {
			attrs["port"] = n
		}
	}
	if r.masterAddr != "" {
		attrs["master_addr"] = r.masterAddr
	}
	if r.clusterName != "" {
		attrs["cluster_name"] = r.clusterName
		attrs["cluster_mates"] = r.clusterMate
	}
	if r.typ == "mysql" {
		attrs["schema_count"] = r.schemaCount
	}
	return record.Record{
		Source:         Source,
		Collector:      CollectorName,
		ModelCandidate: "db_instance",
		Attributes:     attrs,
		OccurredAt:     c.now(),
	}
}

// ciItem 是 CMDB CI（仅取本采集器关心的字段）。
type ciItem struct {
	ID         string         `json:"id"`
	Attributes map[string]any `json:"attributes"`
	Status     string         `json:"status"`
}

// ciListResponse 与契约 CIListResponse 对应（仅取关心字段）。
type ciListResponse struct {
	Items []ciItem `json:"items"`
}

// fetchInstances 分页拉取全部 db_instance CI（剔除已退役），映射为待探测实例清单。
func (c *Collector) fetchInstances(ctx context.Context) ([]instance, error) {
	headers := map[string]string{}
	if c.cmdbToken != "" {
		headers["Authorization"] = "Bearer " + c.cmdbToken
	}
	var out []instance
	const pageSize = 200 // 服务端 page_size 上限 200
	for page := 1; ; page++ {
		u := fmt.Sprintf("%s/api/v1/cis?model_id=db_instance&page=%d&page_size=%d",
			c.apiURL, page, pageSize)
		var resp ciListResponse
		if err := record.DoJSON(ctx, c.http, http.MethodGet, u, headers, nil, &resp); err != nil {
			return nil, fmt.Errorf("查询 db_instance 实例清单失败: %w", err)
		}
		for _, item := range resp.Items {
			if item.Status == "retired" {
				continue
			}
			addr := record.StrField(item.Attributes, "instance_addr")
			if addr == "" {
				c.logf("CI %s 缺少 instance_addr 属性，跳过", item.ID)
				continue
			}
			out = append(out, instance{
				addr: addr,
				typ:  record.StrField(item.Attributes, "component_type"),
			})
		}
		if len(resp.Items) < pageSize {
			return out, nil
		}
	}
}

// credInstances 以凭据文件条目回退构造实例清单（dry-run 离线场景）。
func credInstances(creds []credEntry) []instance {
	out := make([]instance, 0, len(creds))
	for _, e := range creds {
		out = append(out, instance{addr: e.InstanceAddr, typ: e.Type})
	}
	return out
}
