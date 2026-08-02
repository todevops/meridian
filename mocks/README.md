# CMDB Mock 平台（mocks）

二期并行开发的官方接口 mock 平台：单二进制 `mockd` 并行启动 6 个 mock 系统，
数据全部来自 `fixtures/*.json`（内嵌进二进制，任意目录可运行），零三方依赖。

## 端口与系统一览

| 端口 | 系统 | 鉴权 | 端点 |
|---|---|---|---|
| :19001 | n9e | `Authorization: Bearer <非空>`，否则 401 | `GET /api/n9e/targets` |
| :19002 | NetBox | `Authorization: Token <非空>`，否则 403 | `GET /api/dcim/sites/`、`/api/dcim/racks/`、`/api/dcim/devices/`、`/api/ipam/prefixes/`、`/api/ipam/ip-addresses/`、`/api/ipam/vlans/`、`/api/virtualization/virtual-machines/` |
| :19003 | LibreNMS | `X-Auth-Token: <非空>`，否则 401 | `GET /api/v0/devices`、`GET /api/v0/devices/{hostname}/ports` |
| :19004 | TSDB（Prometheus 兼容） | 无 | `GET /api/v1/query?query=<m>`、`GET /api/v1/label/instance/values?match[]=<m>` |
| :19005 | 阿里云 | 无 | 任意方法任意路径 → ECS 数组 |
| :19006 | 火山引擎 CloudControl | 无 | `POST /?Action=ListResources&Version=2021-01-01` |

每个端口均可用环境变量覆盖（如 `MOCK_N9E_ADDR=:29001`），对应关系见
`internal/mocksys/mocksys.go` 的 `Load`。

## 构建与启动

```bash
cd cmdb/mocks
go build ./...
go run ./cmd/mockd          # 前台运行，Ctrl+C 优雅退出
# 或： go build -o mockd.exe ./cmd/mockd && ./mockd.exe
```

启动日志会逐行打出 6 个系统的监听地址。

## curl 示例

```bash
# ---- n9e（:19001）：3 条 Target，其中两条 ident 同为 web-dup（撞键演示），
#      db-mysql-01 心跳停滞 9 天（疑似下线演示）；update_at 启动时平移到当前时间 ----
curl -s -H "Authorization: Bearer dev-token" http://127.0.0.1:19001/api/n9e/targets
curl -i http://127.0.0.1:19001/api/n9e/targets                      # 反例：无 Bearer → 401

# ---- NetBox（:19002）：{count,next,previous,results} 分页，支持 limit/offset ----
curl -s -H "Authorization: Token dev-token" http://127.0.0.1:19002/api/dcim/devices/
curl -s -H "Authorization: Token dev-token" "http://127.0.0.1:19002/api/ipam/ip-addresses/?limit=5&offset=10"
curl -i http://127.0.0.1:19002/api/dcim/sites/                      # 反例：无 Token → 403

# ---- LibreNMS（:19003）：4 台设备（华为/H3C/Cisco/锐捷）与端口清单 ----
curl -s -H "X-Auth-Token: dev-token" http://127.0.0.1:19003/api/v0/devices
curl -s -H "X-Auth-Token: dev-token" http://127.0.0.1:19003/api/v0/devices/bj-core-sw-01/ports
curl -i http://127.0.0.1:19003/api/v0/devices                       # 反例：无 X-Auth-Token → 401

# ---- TSDB（:19004）：mysql_up（2 实例）/redis_up/kafka_brokers/elasticsearch_cluster_health ----
curl -s "http://127.0.0.1:19004/api/v1/query?query=mysql_up"
curl -s "http://127.0.0.1:19004/api/v1/label/instance/values?match[]=mysql_up"

# ---- 阿里云（:19005）：任意路径均返回 2 台 ECS（DescribeInstances 字段风格） ----
curl -s http://127.0.0.1:19005/

# ---- 火山引擎（:19006）：2 ECS + 1 VKE，Configuration 为 JSON 字符串 ----
curl -s -X POST "http://127.0.0.1:19006/?Action=ListResources&Version=2021-01-01"
```

## 数据设计说明

- 各 fixture 在同一套虚拟拓扑内互相呼应：北京亦庄/上海张江两机房、
  `10.30.0.0/24`（网管）、`10.30.1.0/24`（服务器）、`10.30.2.0/24`（数据库）网段，
  NetBox 设备与 LibreNMS 设备同名同 IP，可直接演示多源调和。
- n9e 两条 `ident=web-dup` 的 Target 用于演示 ident 撞键调和场景。
- LibreNMS 的 `bj-acc-sw-01`（10.30.0.5）在 NetBox 中无记录，演示“发现池新增”。
- NetBox IP 状态使用官方取值 `active/reserved/deprecated`，
  “已分配”语义以 `active` + `assigned_object_type/assigned_object_id` 表达。
- `fixtures/volcengine-resources.json` 中 `Configuration` 以 JSON 对象书写便于维护，
  响应时按官方协议序列化为 JSON 字符串。

## 目录结构

```
mocks/
├── cmd/mockd/main.go        # 入口：信号处理 + 启动全部系统
├── embed.go                 # go:embed 内嵌 fixtures/
├── fixtures/*.json          # 13 个数据文件
└── internal/mocksys/        # 6 个系统的 handler 实现（每系统一个文件）
```
