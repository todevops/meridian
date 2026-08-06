// mysql 实例直连探测：database/sql（生产驱动 mysql，测试/fixture 注入假驱动）。
package dbprobe

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"

	_ "github.com/go-sql-driver/mysql" // 注册 "mysql" 驱动
)

// mysql 查询串。fixture 文件以此为主键，改动需同步 fixture。
const (
	qMySQLVersion = "SELECT VERSION()"
	qMySQLReplica = "SHOW REPLICA STATUS" // MySQL 8.0.22+；旧版回退 SHOW SLAVE STATUS
	qMySQLSlave   = "SHOW SLAVE STATUS"
	qMySQLSchemas = "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA"
)

// mysqlDSN 构造 go-sql-driver 连接串（只读账号，超时收紧避免拖垮整轮采集）。
// ponytail: 口令含 @ : / 等特殊字符时 DSN 需转义，本期货到口令从受控凭据文件出，约定不含特殊字符。
func mysqlDSN(c credEntry) string {
	return fmt.Sprintf("%s:%s@tcp(%s)/?timeout=5s&readTimeout=5s", c.Username, c.Password, c.InstanceAddr)
}

// probeMySQL 直连 mysql 实例，补采版本、主库地址与 schema 数。
// openDB 默认 sql.Open；driverName 生产为 "mysql"，测试/fixture 注入内存假驱动名
// （假驱动以实例地址本身作为 DSN 主键）。
func probeMySQL(ctx context.Context, openDB func(driverName, dsn string) (*sql.DB, error), driverName, dsn string) (version, masterAddr string, schemaCount int, err error) {
	db, err := openDB(driverName, dsn)
	if err != nil {
		return "", "", 0, fmt.Errorf("打开连接失败: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return "", "", 0, fmt.Errorf("连接失败: %w", err)
	}
	if err := db.QueryRowContext(ctx, qMySQLVersion).Scan(&version); err != nil {
		return "", "", 0, fmt.Errorf("查询版本失败: %w", err)
	}
	masterAddr, err = queryMasterAddr(ctx, db)
	if err != nil {
		return "", "", 0, err
	}
	schemaCount, err = querySchemaCount(ctx, db)
	if err != nil {
		return "", "", 0, err
	}
	return version, masterAddr, schemaCount, nil
}

// queryMasterAddr 查询复制拓扑：有复制行 → 返回主库地址（host:port）；无行 → 空串（主库或单机）。
func queryMasterAddr(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, qMySQLReplica)
	if err != nil {
		// MySQL < 8.0.22 无 SHOW REPLICA STATUS，回退旧语法
		rows, err = db.QueryContext(ctx, qMySQLSlave)
		if err != nil {
			return "", fmt.Errorf("查询复制状态失败: %w", err)
		}
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", fmt.Errorf("读取复制状态列失败: %w", err)
	}
	hostIdx, portIdx := -1, -1
	for i, c := range cols {
		switch strings.ToLower(c) {
		case "source_host", "master_host":
			hostIdx = i
		case "source_port", "master_port":
			portIdx = i
		}
	}
	if !rows.Next() {
		return "", rows.Err()
	}
	vals, err := scanRow(rows, len(cols))
	if err != nil {
		return "", fmt.Errorf("读取复制状态行失败: %w", err)
	}
	if hostIdx < 0 {
		return "", nil // 列名不识别时保守视为无拓扑信息
	}
	host := asString(vals[hostIdx])
	if host == "" {
		return "", nil
	}
	port := "3306"
	if portIdx >= 0 && asString(vals[portIdx]) != "" {
		port = asString(vals[portIdx])
	}
	return net.JoinHostPort(host, port), nil
}

// 不计入 schema_count 的 mysql 系统库。
var systemSchemas = map[string]bool{
	"information_schema": true,
	"performance_schema": true,
	"mysql":              true,
	"sys":                true,
}

// querySchemaCount 统计业务库数量（排除系统库）。
func querySchemaCount(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, qMySQLSchemas)
	if err != nil {
		return 0, fmt.Errorf("查询库清单失败: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return 0, fmt.Errorf("读取库名失败: %w", err)
		}
		if !systemSchemas[strings.ToLower(name)] {
			n++
		}
	}
	return n, rows.Err()
}

// scanRow 按列数动态扫描当前行。
func scanRow(rows *sql.Rows, n int) ([]any, error) {
	vals := make([]any, n)
	ptrs := make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	return vals, nil
}

// asString 容忍 string / []byte / 数值等驱动返回类型。
func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprint(t)
	}
}
