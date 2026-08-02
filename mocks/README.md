# Meridian Mock 平台（mocks）

二期并行开发的官方接口 mock 平台：单二进制 `mockd` 并行启动 8 个 mock 系统，
数据全部来自 `fixtures/*.json`（内嵌进二进制，任意目录可运行）；
vcsim 为 govmomi/simulator 进程内 vCenter 模拟器（不依赖 fixture）。

## 端口与系统一览

| 端口 | 系统 | 鉴权 | 端点 |
|---|---|---|---|
| :19001 | n9e | `Authorization: Bearer <非空>`，否则 401（dashboards 除外） | `GET /api/n9e/targets`、`PUT /api/n9e/targets/{id}/tags`、`PUT /api/n9e/targets/{id}/note`、`GET /api/n9e/alert-cur-events?ident=`、`GET /dashboards/host?ident=`（HTML，无需鉴权） |
| :19002 | NetBox | `Authorization: Token <非空>`，否则 403 | `GET /api/dcim/sites/`、`/api/dcim/racks/`、`/api/dcim/devices/`、`/api/ipam/prefixes/`、`/api/ipam/ip-addresses/`、`/api/ipam/vlans/`、`/api/virtualization/virtual-machines/` |
| :19003 | LibreNMS | `X-Auth-Token: <非空>`，否则 401 | `GET /api/v0/devices`、`GET /api/v0/devices/{hostname}/ports` |
| :19004 | TSDB（Prometheus 兼容） | 无 | `GET /api/v1/query?query=<m>`、`GET /api/v1/label/instance/values?match[]=<m>` |
| :19005 | 阿里云 | 无 | 任意方法任意路径 → ECS 数组 |
| :19006 | 火山引擎 CloudControl | 无 | `POST /?Action=ListResources&Version=2021-01-01` |
| :19007 | vcsim（vCenter 模拟器） | 任意凭据均可登录，约定 `user:pass` | `POST /sdk`（SOAP）、`GET /sdk/vimServiceVersions.xml`、`GET /about` |
| :19008 | Oxidized | 无（只读端点） | `GET /nodes`、`GET /node/fetch/{name}`；启动时默认执行一次性上报流程（见下） |

每个端口均可用环境变量覆盖（如 `MOCK_N9E_ADDR=:29001`），对应关系见
`internal/mocksys/mocksys.go` 的 `Load`。

## 构建与启动

```bash
cd cmdb/mocks
go build ./...
go run ./cmd/mockd          # 前台运行，Ctrl+C 优雅退出
# 或： go build -o mockd.exe ./cmd/mockd && ./mockd.exe
```

启动日志会逐行打出 8 个系统的监听地址；vcsim 额外打印完整 SDK URL 与默认凭据。

## Oxidized 一次性上报流程（:19008）

mockd 启动后，Oxidized mock 默认（`OXIDIZED_ONCE=true`）执行一次性流程，每步结果打日志：

1. 取 CMDB 访问令牌：优先 `MERIDIAN_TOKEN`，否则用 `MERIDIAN_USERNAME`/`MERIDIAN_PASSWORD`
   （默认 `admin`/`admin123`）向 `{CMDB_API_URL}`（默认 `http://127.0.0.1:8080`）登录换取；
2. `GET /api/v1/integrations/oxidized/devices` 拉设备清单（CMDB 未就绪时 3 秒间隔最多重试 20 次），
   并以真实清单刷新本 mock 的 `GET /nodes`；
3. 对每台设备 `POST /api/v1/integrations/oxidized/events`（头 `X-Oxidized-Token`，
   可用 `OXIDIZED_TOKEN` 覆盖，默认 `dev-oxidized-token`）上报一条 `backup` 事件（时间按 5 分钟间隔错开）；
4. 对第一台设备补报一条 `change` 事件（演示配置变更回写）。

`OXIDIZED_ONCE=false` 时跳过上述流程，仅挂 `GET /nodes` 与 `GET /node/fetch/{name}` 两个只读端点供 curl。

## curl 示例

```bash
# ---- n9e（:19001）：3 条 Target，其中两条 ident 同为 web-dup（撞键演示），
#      db-mysql-01 心跳停滞 9 天（疑似下线演示）；update_at 启动时平移到当前时间 ----
curl -s -H "Authorization: Bearer dev-token" http://127.0.0.1:19001/api/n9e/targets
curl -i http://127.0.0.1:19001/api/n9e/targets                      # 反例：无 Bearer → 401

# n9e 写端点（F-070 上行联调）：覆写 tags / note，写后 GET targets 读回可见（内存态）
# 提示：Windows Git Bash 下 curl -d 内联中文会按 GBK argv 编码损坏，
#       含中文的 body 请写入 UTF-8 文件后用 --data-binary @file 发送。
curl -s -X PUT -H "Authorization: Bearer dev-token" -H 'Content-Type: application/json' \
  -d '{"tags":"env=prod role=web app=mall-front owner=zhangsan"}' \
  http://127.0.0.1:19001/api/n9e/targets/101/tags
curl -s -X PUT -H "Authorization: Bearer dev-token" -H 'Content-Type: application/json' \
  -d '{"note":"电商前台 Web 节点 A（负责人：张三）"}' \
  http://127.0.0.1:19001/api/n9e/targets/101/note
curl -s -H "Authorization: Bearer dev-token" http://127.0.0.1:19001/api/n9e/targets   # 读回验证

# n9e 当前告警（F-063 嵌入联调）：fixture 2 条（web-dup CPU 告警 + db-mysql-01 心跳失联），
# ident 过滤；first_trigger_time 启动时平移到当前时间附近
curl -s -H "Authorization: Bearer dev-token" "http://127.0.0.1:19001/api/n9e/alert-cur-events?ident=web-dup"

# n9e 仪表盘（F-063 iframe 嵌入联调）：返回简易 HTML，标题含 ident，无需鉴权
curl -s "http://127.0.0.1:19001/dashboards/host?ident=web-dup"

# ---- NetBox（:19002）：{count,next,previous,results} 分页，支持 limit/offset ----
curl -s -H "Authorization: Token dev-token" http://127.0.0.1:19002/api/dcim/devices/
curl -s -H "Authorization: Token dev-token" "http://127.0.0.1:19002/api/ipam/ip-addresses/?limit=5&offset=10"
curl -i http://127.0.0.1:19002/api/dcim/sites/                      # 反例：无 Token → 403

# ---- LibreNMS（:19003）：4 台设备（华为/H3C/Cisco/锐捷）与端口清单 ----
curl -s -H "X-Auth-Token: dev-token" http://127.0.0.1:19003/api/v0/devices
curl -s -H "X-Auth-Token: dev-token" http://127.0.0.1:19003/api/v0/devices/bj-core-sw-01/ports
curl -i http://127.0.0.1:19003/api/v0/devices                       # 反例：无 X-Auth-Token → 401

# ---- TSDB（:19004）：mysql_up（2 实例）/postgresql_up（2 实例）/mongodb_up/redis_up/kafka_brokers/elasticsearch_cluster_health ----
curl -s "http://127.0.0.1:19004/api/v1/query?query=mysql_up"
curl -s "http://127.0.0.1:19004/api/v1/query?query=postgresql_up"
curl -s "http://127.0.0.1:19004/api/v1/label/instance/values?match[]=mysql_up"

# ---- 阿里云（:19005）：任意路径均返回 2 台 ECS（DescribeInstances 字段风格） ----
curl -s http://127.0.0.1:19005/

# ---- 火山引擎（:19006）：2 ECS + 1 VKE，Configuration 为 JSON 字符串 ----
curl -s -X POST "http://127.0.0.1:19006/?Action=ListResources&Version=2021-01-01"

# ---- vcsim（:19007）：1 DC / 2 集群 / 每集群 3 ESXi / 每 ESXi 5 VM（共 30 台，每 ESXi 第 5 台关电无 Guest IP） ----
curl -s http://127.0.0.1:19007/sdk/vimServiceVersions.xml          # → 200，证明 SDK 端点在线
curl -s http://127.0.0.1:19007/about                               # → 200，JSON 列出全部方法与类型
# 裸 POST /sdk 返回 500 + SOAP Fault（未带合法方法名），属预期行为，同样证明端点在线：
curl -s -X POST http://127.0.0.1:19007/sdk
# 正式接入：govmomi 客户端指向 http://user:pass@127.0.0.1:19007/sdk 即可（任意凭据均可登录）

# ---- Oxidized（:19008）：节点清单与配置抓取（只读、无需鉴权）----
curl -s http://127.0.0.1:19008/nodes                                # 节点清单（一次性流程跑完后反映 CMDB 真实清单）
curl -s http://127.0.0.1:19008/node/fetch/bj-core-sw-01             # 该节点最新配置文本
curl -i http://127.0.0.1:19008/node/fetch/no-such-node              # 反例：未知节点 → 404
```

## 数据设计说明

- 各 fixture 在同一套虚拟拓扑内互相呼应：北京亦庄/上海张江两机房、
  `10.30.0.0/24`（网管）、`10.30.1.0/24`（服务器）、`10.30.2.0/24`（数据库）网段，
  NetBox 设备与 LibreNMS 设备同名同 IP，可直接演示多源调和。
- n9e 两条 `ident=web-dup` 的 Target 用于演示 ident 撞键调和场景；
  `fixtures/n9e-alerts.json` 的 2 条当前告警（web-dup CPU 越限 + db-mysql-01 心跳失联）
  与 targets 的心跳停滞叙事呼应，供告警嵌入联调。
- LibreNMS 的 `bj-acc-sw-01`（10.30.0.5）在 NetBox 中无记录，演示“发现池新增”。
- NetBox IP 状态使用官方取值 `active/reserved/deprecated`，
  “已分配”语义以 `active` + `assigned_object_type/assigned_object_id` 表达。
- `fixtures/volcengine-resources.json` 中 `Configuration` 以 JSON 对象书写便于维护，
  响应时按官方协议序列化为 JSON 字符串。
- vcsim 的 30 台 VM 中，每台 ESXi 的第 5 台（按名称排序，共 6 台）处于关电态且无 Guest IP，
  其余 VM 持有 `10.30.4.0/24` 虚拟化网段的静态 Guest IP，用于演示 vSphere 采集的电源状态分支。

## 目录结构

```
mocks/
├── cmd/mockd/main.go        # 入口：信号处理 + 启动全部系统
├── embed.go                 # go:embed 内嵌 fixtures/
├── fixtures/*.json          # 15 个数据文件
└── internal/mocksys/        # 8 个系统的 handler 实现（每系统一个文件）
```
