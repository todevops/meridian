# Meridian

纯自研企业级 CMDB（配置管理数据库）平台 monorepo，用于替换 NetBox 并覆盖 **IPAM、DCIM、NMS、DBMS、K8s 元数据管理**五大功能域，并向 UModel 输出实体图作为 AIOps 底座。技术栈：**Go 后端（Gin + GORM + Casbin）+ Next.js 16 前端（React 19 + shadcn/ui + Tailwind）+ 插件化发现引擎（开源组件 + 官方 SDK 双轨）**，前后端以 `pkg/openapi/` 契约为唯一事实来源。

## 功能总览

| 域 | 能力 |
|---|---|
| 资产中心 | 主机（n9e 心跳消费零 Agent）、虚拟化三级（集群→ESXi→VM）、云资源（阿里云/火山 ECS/VPC/RDS/SLB）、网络设备台账、数据库/中间件台账（版本/主从/EOL）、K8s 元数据（集群/命名空间/工作负载/Service/Ingress，Pod 实况直查不落库）、应用系统聚合页（SRE 第一入口） |
| 基础设施 | IPAM（前缀树/分配/冲突检测/利用率）、DCIM（机房/机柜/U 位挂载/容量总览）、网络拓扑（LLDP 自动成图 + 主机接入端口定位） |
| 发现与治理 | 自研发现引擎（8 款采集器 + n9e 消费器）、调和引擎（UID 优先链 + 冲突入池）、发现池工作台、自动入库白名单、稽核规则与整改待办、数据质量看板、生命周期状态机（退役三方会签 + n9e/JumpServer/IPAM 联动）、黑设备告警 |
| 平台配置 | 模型引擎（运行时自定义模型/属性/关系/校验）、集成管理（AES-GCM 加密凭据库）、采集任务调度（builtin/exec 执行器）、审计日志查询 |
| 系统管理 | 用户/角色（Casbin RBAC）、数据范围权限（业务系统维度最小授权，越权 404） |
| 集成输出 | NetBox 迁移（direct/pipeline/verify 三模式）、n9e 双向集成（消费+回写）、Oxidized 配置备份（设备源供给+事件回写）、JumpServer 资产同步、UModel 实体/关联输出 |

## 目录结构

```
cmdb/
├── apps/web/            # 前端（Next.js 16 + React 19 + shadcn/ui，包名 web）
├── packages/            # ui / eslint-config / typescript-config 共享包
├── server/              # Go 后端 API（module meridian/server）
├── collectors/          # 八款采集器（aliyun/volc/dbdiscover/librenms/ipscan/vsphere/k8s/dbprobe）
├── migrator/            # NetBox 迁移工具（direct / pipeline / verify 三模式）
├── mocks/               # 官方接口 mock 平台（单二进制 mockd，11 系统 :19001-:19011）
├── pkg/openapi/         # 前后端共享契约（OpenAPI 3.0.3，见 pkg/README.md）
├── scripts/             # demo.sh / seed-models.sh / auth-login.sh + 种子与样例数据
├── docs/acceptance/     # 各阶段浏览器验收报告与截图证据
├── docker-compose.yml   # 本地开发依赖：PostgreSQL 16 / Redis 7 / NATS JetStream
└── .github/workflows/   # CI（web lint+build / server build+vet+test）
```

## 快速开始

工具链为项目内便携版（Node 24 / Go 1.26 / pnpm），位于 `../.tools`，每条命令前先加载环境：

```bash
source ../.tools/env.sh
```

**一键演示**（无需 Docker，SQLite 临时库，结束自动清理）：

```bash
bash scripts/demo.sh    # 起 server → 导入 18 个种子模型 → 样例发现记录调和 → 打印主机清单
```

**手工体验**：

```bash
# 终端 1：后端（SQLite 本地开发库）
cd server && DB_SQLITE_PATH=./meridian-dev.db go run ./cmd/server

# 终端 2：导入种子模型 + 启动 mocks（可选，供采集器联调）
bash scripts/seed-models.sh
cd mocks && go run ./cmd/mockd    # n9e/NetBox/LibreNMS/TSDB/双云/vcsim/Oxidized/K8s/JumpServer/UModel

# 终端 3：前端
pnpm install && pnpm --filter web dev
```

浏览器访问 http://localhost:3000 ，以 `admin` / `admin123` 登录（可用 `ADMIN_INITIAL_PASSWORD` 覆盖）。

**跑一次全量采集**（需 mockd 在线）：

```bash
cd collectors && go build -o collector ./cmd/collector
MERIDIAN_API_URL=http://localhost:8080 MERIDIAN_USERNAME=admin MERIDIAN_PASSWORD=admin123 \
LIBRENMS_API_TOKEN=dev VSPHERE_URL=http://localhost:19007/sdk VSPHERE_USERNAME=user VSPHERE_PASSWORD=pass \
K8S_API_URL=http://localhost:19009 K8S_TOKEN=dev-k8s-token \
./collector -collector=all        # 加 -dry-run 只打印不上报
```

**NetBox 迁移与对账**：

```bash
cd migrator
NETBOX_API_URL=http://localhost:19002 NETBOX_TOKEN=dev \
MERIDIAN_API_URL=http://localhost:8080 MERIDIAN_USERNAME=admin MERIDIAN_PASSWORD=admin123 \
go run ./cmd/migrate -mode=pipeline    # 迁移（幂等，重跑=更新）
go run ./cmd/migrate -mode=verify      # 双轨对账（一致率 100% 退出码 0）
```

## 常用命令

```bash
# 前端
pnpm lint / pnpm typecheck / pnpm build        # 质量门禁（CI 同款）

# 后端与各工具（均为独立 Go module）
cd server     && go build ./... && go vet ./... && go test ./...
cd collectors && go build ./... && go vet ./... && go test ./...
cd migrator   && go test ./...
cd mocks      && go test ./...

# 契约校验
pnpm dlx js-yaml pkg/openapi/openapi.yaml
pnpm dlx js-yaml pkg/openapi/ipam-dcim.yaml
```

## 认证、权限与数据范围

- 除 `/healthz`、`/readyz`、`/api/v1/auth/login` 外全部接口需登录并按权限点鉴权；会话经 httpOnly cookie（`meridian_token`）或 Bearer 携带。
- 内置角色：`admin`（全量）、`operator`（日常维护）、`viewer`（只读）、`collector`（仅采集上报）、`system_owner`（全量只读 + 强制数据范围）。
- **数据范围权限**（F-005）：用户绑定业务系统（`scope_app_ids`）后，列表/搜索/详情沿关系链裁剪至本系统资产，越权直访返回 404（不泄露存在性）；IPAM/DCIM 共享设施一期全量只读不裁剪。
- 凭据经 AES-256-GCM 加密存凭据库（`/integrations` 纳管轮换），明文永不回传；数据库凭据/连接串/Secret 内容零入库（安全红线）。

## 页面地图

```
总览        /            全局搜索落地页
            /dashboard   运营仪表盘（数据质量看板）
资产中心    /applications  应用系统（两级业务树 + 聚合视图 + 依赖拓扑 + 影响面）
            /hosts         主机（统一视图：VM/物理机/云主机/K8s Node）
            /virtualization 虚拟化三级视图
            /network/devices 网络设备台账（含配置备份卡）
            /dbms          数据库与中间件（集群统计 + 版本分布 + EOL 导出）
            /k8s           容器云（集群→命名空间→工作负载 + Pod 实况）
            /cloud         云资源（ECS/VPC/RDS/SLB）
基础设施    /ipam          IPAM 地址管理
            /dcim          机房与机柜（U 位矩阵挂载）
            /topology      网络拓扑（LLDP 成图 + 主机接入定位）
发现与治理  /pool          发现池工作台
            /alerts        告警事件
            /discovery     采集任务与数据源
            /governance    稽核与整改（规则/待办/待退役）
平台配置    /models        模型管理
            /integrations  集成管理（凭据纳管）
            /audit         审计日志
系统管理    /settings/users  用户管理（数据范围绑定）
            /settings/roles  角色管理
```

## 验证与验收

- 全模块单测（server/collectors/migrator/mocks）+ 前端 lint/typecheck/build 为提交门禁。
- 各阶段浏览器验收（browser-harness 驱动真实浏览器）报告与截图证据见 `docs/acceptance/`。
- 生态联调零真实依赖：`mocks/` 覆盖全部外部系统官方接口形态（n9e/NetBox/LibreNMS/TSDB/双云/vcsim/Oxidized/K8s apiserver/JumpServer/UModel EntityStore）。

## 更多文档

- 架构与开发约定：`AGENTS.md`
- 接口契约：`pkg/openapi/`（契约先行：改契约 → 评审 → 前后端并行）
- 需求与方案（上级目录）：`企业CMDB产品需求文档PRD.md`、`企业CMDB系统建设方案.md`、各阶段实施规格说明
