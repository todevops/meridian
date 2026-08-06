// fixture 模式：DBPROBE_FIXTURE_FILE 指向预置查询应答 JSON，供无库环境演示。
// 文件格式：顶层键为实例地址，值为 查询串 → 应答；
// SQL 应答为 {"columns":[...],"rows":[[...]]}，redis INFO 应答为原始字符串（JSON string）。
// 实例地址缺失 = 连接失败；查询串缺失 = 查询报错（可演示失败不中断路径）。
package dbprobe

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// fixtureStore 是预置查询应答表。
type fixtureStore struct {
	data map[string]map[string]json.RawMessage // 实例地址 → 查询串 → 应答
}

// LoadFixture 读取 fixture 文件。
func LoadFixture(path string) (*fixtureStore, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 fixture 文件失败: %w", err)
	}
	var data map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("解析 fixture 文件 JSON 失败: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("fixture 文件为空")
	}
	return &fixtureStore{data: data}, nil
}

// lookup 按实例地址 + 查询串取应答。
func (s *fixtureStore) lookup(addr, query string) (json.RawMessage, error) {
	queries, ok := s.data[addr]
	if !ok {
		return nil, fmt.Errorf("fixture 无实例 %s（模拟连接失败）", addr)
	}
	raw, ok := queries[query]
	if !ok {
		return nil, fmt.Errorf("fixture 实例 %s 未预置查询 %q 的应答", addr, query)
	}
	return raw, nil
}

// --- database/sql 假驱动（fixture 模式与单测共用，单测以 "fake" 名注册） ---

// fixtureSQLResult 是一条预置 SQL 应答。
type fixtureSQLResult struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// fixtureDriver 实现 driver.Driver：按 DSN（实例地址）查应答表。
// store 指针可换（RegisterFixtureDriver 幂等重指），单进程单采集器、测试串行，无并发问题。
type fixtureDriver struct {
	mu    sync.Mutex
	store *fixtureStore
}

func (d *fixtureDriver) Open(name string) (driver.Conn, error) {
	d.mu.Lock()
	store := d.store
	d.mu.Unlock()
	if store == nil {
		return nil, errors.New("fixture 驱动未绑定应答表")
	}
	if _, ok := store.data[name]; !ok {
		return nil, fmt.Errorf("fixture 无实例 %s（模拟连接失败）", name)
	}
	return &fixtureConn{store: store, addr: name}, nil
}

// fixtureConn 实现 driver.Conn + Pinger + QueryerContext。
type fixtureConn struct {
	store *fixtureStore
	addr  string
}

func (c *fixtureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fixture 驱动不支持 Prepare（仅 QueryContext）")
}
func (c *fixtureConn) Close() error { return nil }
func (c *fixtureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fixture 驱动不支持事务")
}
func (c *fixtureConn) Ping(context.Context) error {
	return nil // Open 已校验实例存在，连接即成功
}

func (c *fixtureConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	raw, err := c.store.lookup(c.addr, query)
	if err != nil {
		return nil, err
	}
	var res fixtureSQLResult
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // 数值保持精确，端口等整型不被 float64 化
	if err := dec.Decode(&res); err != nil {
		return nil, fmt.Errorf("fixture 实例 %s 查询 %q 应答须为 {columns,rows}: %w", c.addr, query, err)
	}
	rows := make([][]driver.Value, 0, len(res.Rows))
	for _, r := range res.Rows {
		vals := make([]driver.Value, len(r))
		for i, v := range r {
			vals[i] = driverValue(v)
		}
		rows = append(rows, vals)
	}
	return &fixtureRows{cols: res.Columns, rows: rows}, nil
}

// driverValue 把 JSON 值转为 driver.Value。
func driverValue(v any) driver.Value {
	switch t := v.(type) {
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
		f, _ := t.Float64()
		return f
	case float64:
		return t
	case string:
		return t
	case bool:
		return t
	default:
		return nil
	}
}

// fixtureRows 实现 driver.Rows。
type fixtureRows struct {
	cols []string
	rows [][]driver.Value
	pos  int
}

func (r *fixtureRows) Columns() []string { return r.cols }
func (r *fixtureRows) Close() error      { return nil }
func (r *fixtureRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

// --- fixture redis 客户端 ---

// fixtureRedisClient 从应答表取 INFO 原始文本（键为 "INFO <section>"）。
type fixtureRedisClient struct {
	store *fixtureStore
	addr  string
}

func (f *fixtureRedisClient) Info(_ context.Context, section string) (string, error) {
	raw, err := f.store.lookup(f.addr, "INFO "+section)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("fixture 实例 %s 的 INFO %s 应答须为 JSON 字符串: %w", f.addr, section, err)
	}
	return s, nil
}

func (f *fixtureRedisClient) Close() error { return nil }

// --- 驱动注册 ---

var registerFixtureOnce sync.Once

// RegisterFixtureDriver 以 "dbprobe-fixture" 为名注册（幂等，重复调用换绑应答表）。
// 返回驱动名，供 sql.Open 使用。
func RegisterFixtureDriver(store *fixtureStore) string {
	const name = "dbprobe-fixture"
	registerFixtureOnce.Do(func() { sql.Register(name, fixtureDriverGlobal) })
	fixtureDriverGlobal.mu.Lock()
	fixtureDriverGlobal.store = store
	fixtureDriverGlobal.mu.Unlock()
	return name
}

var fixtureDriverGlobal = &fixtureDriver{}
