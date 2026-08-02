# Meridian

纯自研 Meridian CMDB（配置管理数据库）平台 monorepo：Go 后端 + Next.js 前端 + 插件化发现引擎。

## 目录结构

```
cmdb/
├── apps/
│   └── web/               # 前端 Web 应用（Next.js 16 + React 19 + shadcn/ui）
├── packages/
│   ├── ui/                # 共享 UI 组件库（shadcn/ui，包名 @workspace/ui）
│   ├── eslint-config/     # 共享 ESLint 配置（@workspace/eslint-config）
│   └── typescript-config/ # 共享 TypeScript 配置（@workspace/typescript-config）
├── server/                # Go 后端（Gin + GORM，并行开发中）
├── pkg/                   # 前后端共享契约（OpenAPI，见 pkg/README.md）
├── docker-compose.yml     # 本地开发依赖：PostgreSQL / Redis / NATS JetStream
├── .env.example           # 环境变量样例
├── package.json           # pnpm workspace 根（turbo 编排）
├── pnpm-workspace.yaml    # workspace 声明：apps/*、packages/*
└── turbo.json             # turbo 任务编排（dev/build/lint 等）
```

## 快速开始

工具链为项目内便携版（Node 24 / Go 1.26 / pnpm），位于 `../.tools`，每条命令前需先加载环境：

```bash
source ../.tools/env.sh
```

安装依赖并启动前端开发服务器：

```bash
pnpm install
pnpm dev                 # turbo 启动全部应用的 dev（当前仅 web）
# 或只启动前端：
pnpm --filter web dev    # 等价于在 apps/web 下执行 next dev
```

前端构建与检查：

```bash
pnpm build               # turbo build（产出 apps/web/.next）
pnpm lint                # turbo lint
```

后端运行（`server/` 目录由他人并行开发）：

```bash
cd server
go run ./cmd/server
```

本地依赖服务（PostgreSQL / Redis / NATS JetStream）：

```bash
docker compose up -d
```

环境变量按需从样例复制：`cp .env.example .env`。

## 认证与 RBAC

除 `/healthz`、`/readyz` 与 `/api/v1/auth/login` 外，所有 `/api/v1` 接口均需登录并按权限点鉴权。

- **认证**：用户名/密码登录（bcrypt 哈希存储），签发 JWT（无状态会话），经 httpOnly cookie（`meridian_token`）或 `Authorization: Bearer` 携带。
- **鉴权**：Casbin RBAC，策略经 gorm-adapter 持久化到业务库（`casbin_rule` 表）。权限点为代码内固定目录（`server/internal/auth/catalog.go`），角色可自定义（角色→权限点、用户→角色全部由策略承载）。
- **内置账号**（首次启动种子，初始密码见下）：`admin`（角色 admin，全权限）、`collector`（角色 collector，仅 `discovery:write`，供采集器上报）。
- **内置角色**：`admin`（全部权限点）、`operator`（模型/CI/IPAM/DCIM 维护 + 发现上报）、`viewer`（只读）、`collector`（仅上报）。内置角色不可删除，admin 角色权限点不可修改。
- **前端**：`apps/web/proxy.ts`（Next 16 Proxy，原 middleware）做无 cookie 预检跳登录页；`401` 时 `lib/api.ts` 自动跳转 `/login`；系统管理菜单按权限点显示（`/settings/users`、`/settings/roles`）。

相关环境变量（见 `.env.example`）：

| 变量 | 说明 | 默认 |
| --- | --- | --- |
| `JWT_SECRET` | JWT 签名密钥，生产必须显式配置 | 开发默认值（启动打警告） |
| `TOKEN_TTL_HOURS` | 会话有效期（小时） | `24` |
| `ADMIN_INITIAL_PASSWORD` | admin 初始密码（仅首次种子生效） | `admin123` |
| `COLLECTOR_INITIAL_PASSWORD` | collector 初始密码（仅首次种子生效） | `collector123` |

脚本（`scripts/demo.sh`、`scripts/seed-models.sh`）统一先走 `scripts/auth-login.sh` 登录再调业务接口；可用 `MERIDIAN_AUTH_USER` / `MERIDIAN_AUTH_PASSWORD` 覆盖登录账号。

## 搜索

- **全局搜索**：`GET /api/v1/search?q=...`，跨模型/CI/IPAM 分组返回，前端 landing 页（`/`）即全局搜索页；分组按用户权限点裁剪。
- **模块内搜索**：`/api/v1/models` 与 `/api/v1/cis` 支持 `keyword`（CI 为全属性值大小写不敏感匹配），IPAM/用户管理页自带关键字过滤。
- 实现选型：**PostgreSQL 全文检索**（`LOWER(...) LIKE` + 生产可用 `pg_trgm` 索引），不引入 ES 等外部中间件；搜索契约稳定，未来可在 `httpapi/search.go` 一层之后替换为 ES 实现。

## DCIM

`GET /api/v1/dcim/overview` 提供机房/机柜/U 位/电力容量总览（按机房聚合 + 逐机柜明细）；机柜经 `POST /api/v1/cis/{id}/relations`（`located_in`，one_to_one 自动改挂）分配到机房；机柜 U 位容量属性统一为 `u_capacity`（兼容历史 `u_total`）。

## 垂直切片演示

前置：工具链为项目内便携版，每条命令前先加载环境：

```bash
source ../.tools/env.sh
```

一键演示（本机无需 Docker，server 使用 SQLite 临时库，演示结束自动清理）：

```bash
bash scripts/demo.sh
```

`demo.sh` 依次完成：构建并启动 server（`DB_SQLITE_PATH` 指向临时文件）→ 等待 `/healthz` 就绪 → 调用 `scripts/seed-models.sh` 导入 13 个种子模型 → 逐条 POST 三条样例发现记录（新主机建档 / 同 ident 更新 / 同 IP 不同 ident 冲突入池）→ 查询并打印主机 CI 清单 → 停止 server 并清理临时目录。

手工体验（server 与 web 分别启动）：

```bash
# 终端 1：启动后端（SQLite 本地库文件，相对路径以 server/ 为基准）
cd server
DB_SQLITE_PATH=./meridian-dev.db go run ./cmd/server

# 终端 2：导入种子模型（BASE_URL 默认 http://localhost:8080，可用环境变量覆盖）
bash scripts/seed-models.sh

# 终端 3：启动前端
pnpm --filter web dev
```

随后浏览器访问 http://localhost:3000 ，先以 `admin` / `admin123`（或 `ADMIN_INITIAL_PASSWORD` 设定的值）登录，再依次体验模型列表、CI 实例列表、发现记录演示等页面。种子模型定义见 `scripts/seed/`（host 模型含 `reconcile_keys=["ident","ip"]` 及方案 5.1 节 n9e 映射属性），样例发现记录见 `scripts/sample-records/`。

## 阶段二进度

### 迭代 2A（导航 + 凭据 + 迁移管道 + 模型扩展）

- **分组导航（F-090）**：侧边栏重构为六组结构——概览、资产、发现、网络（IPAM）、机房（DCIM）、系统管理，支持分组折叠记忆、发现池待办徽标、按权限点过滤与面包屑；新页面一律按组归位，不再平铺入口。
- **集成管理 `/integrations`（F-005）**：凭据纳管页面，覆盖 vCenter / 阿里云 / 火山 / SNMP / DB / kubeconfig / SSH-IPMI / n9e / NetBox 九类凭据的创建、轮换与使用审计；接口永不回传明文，采集任务仅按引用使用凭据。
- **采集任务 `/discovery`（F-033）**：采集任务与数据源管理页面，任务创建时选择集成凭据，展示状态（idle/running/error）、最近成功时间、失败原因与运行历史，支持手动触发运行。
- **NetBox 迁移管道模式（F-074）**：migrator 新增 `--mode=pipeline`，将迁移数据翻译为标准发现记录批量写入发现管道（限速 + 退避），借调和引擎获得幂等（重跑 = 更新）。
- **模型扩展（F-027/F-028 前置）**：种子模型自 9 个扩至 11 个——新增 `biz_line`（业务线：编码唯一、负责人、等级 critical/high/normal）与 `k8s_namespace`（K8s 命名空间：集群 + 名称，`mounted_to → biz_app` 整挂应用）；`biz_app` 补 `belongs_to → biz_line`、`deployed_on → host`、`depends_on → db_instance`，`k8s_workload` 补 `in_namespace → k8s_namespace`（命名空间挂载后工作负载沿「工作负载 → 命名空间 → 应用」链继承归属），`host` 补 `connected_to → network_device`（接入于）。种子定义见 `scripts/seed/`，导入仍走 `scripts/seed-models.sh`。
