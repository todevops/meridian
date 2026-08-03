# collectors — Meridian 自研采集器

七款数据源采集器，拉取源端清单映射为标准发现记录（契约 `DiscoveryRecord`，见 `pkg/openapi/openapi.yaml`），
批量 `POST {MERIDIAN_API_URL}/api/v1/discovery-records`。vsphere 采集器依赖 govmomi，k8s 采集器依赖 client-go rest（仅用 RESTClient list，无 informer），其余为纯 Go 标准库实现。

## 构建与运行

```bash
go build ./cmd/collector
./collector -collector=all                 # 全部采集器
./collector -collector=aliyun,librenms     # 逗号分隔多选
./collector -collector=ipscan -dry-run     # 只打印记录，不上报、不变更模型
go test ./...                              # 单测（httptest 夹具 + 内嵌 nmap XML + vcsim 内存 vCenter）
```

运行结束（含 dry-run）于 stdout 末行打印 `CMDB_PRODUCED=<成功上报总条数>`，2A 任务调度器据此统计产出。

## 采集器一览

| 采集器 | 数据源 | 端点 | model_candidate | 关键属性 |
|---|---|---|---|---|
| aliyun | 阿里云 ECS | `GET {ALIYUN_API_URL}/?Action=DescribeInstances&PageSize=500&PageNumber=1` | host | host_type=cloud, cloud_provider=aliyun, cloud_instance_id, ip（首个私网）, ident, spec, zone, status, tags |
| volc | 火山 CloudControl | `POST {VOLC_API_URL}/?Action=ListResources&Version=2021-01-01` | host（ECS 类）/ k8s_workload（VKE 占位，注记 cluster） | 同上，cloud_provider=volc；未建模资源类型跳过 |
| dbdiscover | TSDB（Prometheus 协议） | `GET {TSDB_API_URL}/api/v1/label/instance/values?match[]=<指标>` | db_instance | type（mysql/redis/kafka/elasticsearch）, ip, port, source=tsdb_label |
| librenms | LibreNMS | `GET {LIBRENMS_API_URL}/api/v0/devices` + `GET .../devices/{hostname}/links`（X-Auth-Token） | network_device / network_link | 设备：sysname, ip, vendor, model（缺省回退 hardware）, serial, source=librenms；链路：local_device/local_port/remote_device/remote_port/protocol（缺省 lldp）, source=lldp，links 端点 404 容错跳过 |
| ipscan | nmap | `NMAP_FROM_FILE` 文件 或 `exec nmap -sn -oX - {NMAP_SCAN_TARGET}` | host | ip, mac, black_device_risk（仅未登记存活）, last_seen_alive（取扫描时间） |
| vsphere | vCenter（govmomi） | `{VSPHERE_URL}/sdk` | esxi_cluster / esxi_host / virtual_machine | 见下 |
| k8s | K8s apiserver（client-go rest） | `/version` + list nodes/namespaces/services/deployments/statefulsets/daemonsets/ingresses（Bearer Token 或 kubeconfig） | k8s_cluster / host / k8s_namespace / k8s_workload / k8s_service | 见下 |

### k8s 说明

- k8s_cluster：name=集群名（`K8S_CLUSTER_NAME`）、version（/version gitVersion）、node_count。
- Node → host 记录（host_type=k8s_node、ident=node 名、ip=InternalIP 缺省回退 ExternalIP、labels 白名单 env/environment/biz/business/owner、ready 取 Ready 条件）；关电/NotReady 节点容错建档（ready=false）不中断采集，与既有主机 CI 经调和键合并。
- k8s_namespace：cluster/name；k8s_workload：cluster/namespace/kind（Deployment/StatefulSet/DaemonSet）/name/replicas（DaemonSet 取 desiredNumberScheduled，spec.replicas 缺省按 K8s 语义为 1）/image（Pod 模板首容器）/labels 白名单 app/env/environment；k8s_service：Service 记 selector、Ingress 记 host（规则域名逗号拼接）。
- 周期任务型：单次运行即一轮全量 list 上报，增量/对账由调度周期保证；Pod 一律不落库（详情页实况直查 apiserver）。

### vsphere 说明

- 三类对象：esxi_cluster（name/moid/host_count）、esxi_host（hardware_uuid 主键，model/cpu_cores/mem_mb，关系属性 parent_cluster_moid）、virtual_machine（instance_uuid 主键，ip/os/vcpu/mem_mb/power_state，关系属性 parent_host_uuid，由主机 moid 翻译为 hardware_uuid）。
- 关电或无 VMware Tools 的 VM 无 IP/OS：对应属性省略，instance_uuid 主键不受影响。
- 无 hardware_uuid 的主机、无 instance_uuid 的 VM 无法调和，跳过并日志告警。

### dbdiscover 的模型确保

启动时 `GET /api/v1/models?keyword=db_instance`，若模型 `reconcile_keys != ["type","ip","port"]` 则
`PATCH /api/v1/models/{id}` 修正；模型不存在（应由种子数据创建）只告警不失败。
dry-run 模式下只打印意图不变更，CMDB 不可达降级为告警。

### ipscan 说明

- 输入二选一：`NMAP_FROM_FILE`（已有 `nmap -oX` 结果文件，优先）或 `NMAP_SCAN_TARGET`（现扫网段，需本机已装 nmap；缺失时报错提示安装或改用 from-file）。
- 只处理在线主机；MAC 厂商命中网络设备关键词（cisco/huawei/h3c/juniper 等）的主机视为网络设备，交由 LibreNMS 通道发现，不重复上报。
- **IPAM 比对**（配置了 CMDB 地址时）：逐前缀 `GET /api/v1/ipam/prefixes` + `GET /api/v1/ipam/ips?prefix_id=` 拉登记 IP，四类结论——已登记且存活（跳过）；已登记不存活（打印回收线索清单）；未登记存活（生成 host 发现记录，`black_device_risk=true` 进发现池）；已登记存活但实测 MAC 与关联 CI 的 mac 属性不一致（打印 MAC 变更告警行）。IPAM 不可达时降级为全量存活上报并日志注明。

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `MERIDIAN_API_URL` | `http://localhost:8080` | CMDB API 地址 |
| `MERIDIAN_TOKEN` | 空 | CMDB Bearer 令牌（优先于账密登录） |
| `MERIDIAN_USERNAME` / `MERIDIAN_PASSWORD` | 空 | CMDB 登录账密（换 token） |
| `ALIYUN_API_URL` | `:19005` | 阿里云 mock（简写自动补 `http://localhost`） |
| `VOLC_API_URL` | `:19006` | 火山 CloudControl mock |
| `TSDB_API_URL` | `:19004` | TSDB mock |
| `LIBRENMS_API_URL` | `:19003` | LibreNMS mock |
| `LIBRENMS_API_TOKEN` | 无（必填） | LibreNMS X-Auth-Token，对接 mock 时任意非空值 |
| `NMAP_FROM_FILE` | 空 | nmap -oX 结果文件路径 |
| `NMAP_SCAN_TARGET` | 空 | ipscan 现扫网段，如 `192.168.1.0/24` |
| `VSPHERE_URL` | `:19007` | vCenter SDK 地址（简写自动补 `https://` 与 `/sdk`；vcsim 为 http 时写完整 URL） |
| `VSPHERE_USERNAME` / `VSPHERE_PASSWORD` | 空 | vCenter 账密（vcsim 默认 user/pass） |
| `VSPHERE_INSECURE` | `true` | 跳过 TLS 证书校验 |
| `K8S_API_URL` | `:19009` | K8s apiserver 地址（简写自动补 `http://localhost`） |
| `K8S_TOKEN` | `dev-k8s-token` | K8s Bearer Token |
| `K8S_CLUSTER_NAME` | `volc-prod-k8s` | 集群名（k8s_cluster 记录与其余记录的 cluster 属性） |
| `K8S_INSECURE` | `true` | 跳过 TLS 证书校验 |
| `K8S_KUBECONFIG` | 空 | kubeconfig 文件路径（设置后优先于 url+token） |

## 响应形状兼容说明

- aliyun：兼容顶层数组（mock 简化）与 `{"Instances":{"Instance":[...]}}`（官方）两种响应；
  `PrivateIpAddress` 兼容字符串 / 数组 / `{"IpAddress":[...]}`；`Tags` 兼容字典 / `{"Tag":[...]}` / `[{"Key","Value"}]`。
- volc：`Configuration` 兼容字符串内嵌 JSON（官方）与直接对象（mock）；`Tags` 兼容 `[{"Key","Value"}]` 与字典。
- TSDB label values：容忍 `status` 为 `success` 或 `ok`；其他非空状态视为错误。

## 手工 dry-run 验证（不占端口）

```bash
NMAP_FROM_FILE=./testdata/nmap-sample.xml go run ./cmd/collector -collector=ipscan -dry-run
```
