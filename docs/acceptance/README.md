# 阶段二浏览器端验收报告（browser-harness）

> 日期：2026-08-03 ｜ 工具：browser-harness（browser-use 官方 Python 版的忠实 JS 端口，本机无 Python 故采用；已为其修复 3 处 Windows 适配：命名管道 IPC）｜ 浏览器：Edge headless（CDP :9222）｜ 环境：server:8080（SQLite）+ mockd:19001-19007（含 vcsim）+ Next.js 生产构建 :3000
> 验收数据：13 模型种子 + 六采集器 53 条发现记录 + pipeline 迁移 15 条 + 凭据/任务各 1 条

## 验收结论：**通过**（14 项页面级用例全部通过，过程中发现并修复 1 个后端缺陷）

## 用例结果

| 编号 | 用例 | 结果 | 证据 |
|---|---|---|---|
| T01 | 登录与六组分组导航（F-090） | ✅ 登录跳转 /，六组导航齐备，发现池徽标计数生效 | 01-home.png |
| T02 | 模型管理 13 模型（含 biz_line/k8s_namespace/esxi 系列） | ✅ 全部渲染 | 02-models.png |
| T03 | 主机列表（n9e/云/撞键样本） | ✅ db-mysql-01、ali-mall-front-01、volc-order-api-01、web-dup 均在列 | 03-hosts.png |
| T04 | 主机详情（3C 分组/关系/审计） | ✅ 行点击进详情，Core/关系/审计齐备 | 04-host-detail.png |
| T05 | 发现池（Tab 待处理/已入库/已忽略 + 黑设备记录 + 确认入库按钮） | ✅ 含 192.168.10.5 黑设备记录 | 05-pool.png |
| T06 | IPAM（前缀表/利用率/分配入口） | ✅ 迁移前缀 10.30.x/24 在列 | 06-ipam.png |
| T07/T08 | 机柜列表 + U 位矩阵 | ✅ 列表正常；矩阵直达 URL 正常（U42/挂载）。**已知小问题**：机柜卡片点击不跳转，需直接访问 /dcim/{id}（登记前端小修） | 07-dcim.png / 08-rack-units.png |
| T09 | 凭据管理（列表/轮换/审计入口，无明文） | ✅ | 09-integrations.png |
| T10 | 采集任务（exec 任务列表/手动运行/运行历史） | ✅ aliyun-ecs-sync 在列且运行成功 | 10-discovery.png |
| T11 | 虚拟化三级视图（F-029） | ✅ 集群→ESXi→VM 树与 VM 表（含关电无 IP 行）。**过程中发现并已修复后端缺陷**（见下） | 11-virtualization.png |
| T12 | 云资源 ECS（双云厂商徽标） | ✅ | 12-cloud.png |
| T13 | 网络设备台账（迁移+LibreNMS 双来源） | ✅ 含 bj-acc-sw-01 自动发现设备 | 13-network-devices.png |
| T14 | DBMS 台账（实例/集群统计卡） | ✅ mysql/redis/kafka/elasticsearch 在列 | 14-dbms.png |

## 验收中发现并修复的缺陷

**D-2B-01（已修复并回归）**：虚拟化从属关系 `esxi_host.belongs_to→esxi_cluster` 在真实采集数据下不建立。
- 根因：linker 规则三仅从 VM 记录的 `parent_cluster_moid` 推导集群归属，而 vSphere 采集器（正确实现）把该字段携带在 **ESXi 主机记录** 上，VM 记录不含此字段。
- 修复：新增 `linkEsxiHostCluster`（主机侧按自身 parent_cluster_moid 挂接）与 `linkEsxiCluster`（集群侧反向补挂）两条规则 + 2 个单测（主机先建档/集群先建档两顺序）。
- 回归：server 10 包 / collectors 9 包 / migrator 3 包 / mocks 1 包测试全绿；浏览器复验三级视图通过。

## 规格退出标准映射（阶段二 spec §1）

| 退出标准 | 状态 |
|---|---|
| 2. 六类数据源全部在线（n9e/vSphere/阿里云/火山/SNMP/IP 扫描） | ✅ 本轮全部实跑产出（mock 口径） |
| 3. 未登记存活 IP 进发现池并告警（AC-F043-01） | ✅ 发现池 + AlertEvent + 页面可见 |
| 4. Oxidized 设备源 API 联调 | ✅（2A 已验，本轮回归） |
| 5. 数据库/中间件实例自动发现（F-023） | ✅ 5 实例建档并自动挂接主机 |
| 6. VM 从属随采集动态维护（AC-F029-02） | ✅（修复 D-2B-01 后实测） |
| 7. 分组导航（AC-F090） | ✅ T01 |
| 8. D-01/D-02 修复 | ✅（2A 已验） |
| 1. NetBox 迁移一致率与双轨退役 | 🟡 管道与幂等就绪，待真实 NetBox 环境启动双轨 |

## 遗留小项

1. 机柜列表卡片点击不跳转（直连 URL 正常）——前端小修。
2. vcsim VM 内存显示 0 GB（模拟器上报值过小，换算取整所致，非缺陷）。
3. alerts 告警事件暂无独立页面（API 已备，建议 2C 与 n9e 告警嵌入一并落地）。
