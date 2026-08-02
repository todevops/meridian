<!-- BEGIN:nextjs-agent-rules -->
# This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` before writing any code. Heed deprecation notices.
<!-- END:nextjs-agent-rules -->

# Meridian CMDB — AI Agent 指南

> 本文件面向 AI 编码代理，描述本仓库（`cmdb/` 目录，即 Meridian 项目根）的架构、命令与约定。
> 上一级目录仅存放需求文档（PRD、建设方案、阶段二规格说明）与便携工具链（`.tools/`），不是代码仓库。

## 项目概览

Meridian（子午线）是纯自研的企业级 CMDB（配置管理数据库）平台 monorepo，用于替代 NetBox，覆盖 IPAM、DCIM、NMS、DBMS、K8s 等功能域。技术栈：**Go 后端（Gin + GORM）+ Next.js 前端 + 插件化发现引擎**，前后端以 OpenAPI 契约为唯一事实来源。

注意：项目对外品牌名已从 `cmdb` 更名为 `Meridian`（见上级目录 `RENAME_PLAN.md`），但**代码目录名保留 `cmdb/`**，Go module 为 `meridian/server`、`meridian/mocks`，会话 cookie 名为 `meridian_token`，脚本/API 地址环境变量前缀为 `MERIDIAN_`。新增命名应沿用 Meridian 前缀，不要回退到 cmdb。

## 目录结构

```
cmdb/
├── apps/web/            # 前端：Next.js 16 + React 19 + shadcn/ui + Tailwind CSS 4（包名 web）
├── packages/
│   ├── ui/              # 共享 UI 组件库（@workspace/ui，shadcn/ui）
│   ├── eslint-config/   # 共享 ESLint 配置（@workspace/eslint-config）
│   └── typescript-config/ # 共享 tsconfig（@workspace/typescript-config）
├── server/              # Go 后端 API（module meridian/server，Gin + GORM + Casbin）
├── collectors/          # 五款数据源采集器（纯 Go 标准库，无外部依赖）
├── migrator/            # NetBox → Meridian 一次性迁移工具
├── mocks/               # 官方接口 mock 平台（单二进制 mockd，6 个 mock 系统）
├── pkg/openapi/         # 前后端共享契约（OpenAPI 3.0.3，见 pkg/README.md）
├── scripts/             # demo.sh / seed-models.sh / auth-login.sh 及种子与样例数据
├── docker-compose.yml   # 本地开发依赖：PostgreSQL 16 / Redis 7 / NATS JetStream
└── .github/workflows/ci.yml
```

## 工具链与常用命令

工具链为**项目内便携版**（Node 24 / Go 1.26 / pnpm 10），位于上级目录 `.tools/`。**每条命令前必须先加载环境**：

```bash
source ../.tools/env.sh    # 在 cmdb/ 目录下执行；同时配置国内镜像与 PNPM_HOME
```

### 前端（pnpm workspace + turbo 编排）

```bash
pnpm install
pnpm dev                 # turbo dev（当前仅 apps/web，http://localhost:3000）
pnpm --filter web dev    # 只启动前端
pnpm build               # turbo build（产出 apps/web/.next）
pnpm lint                # turbo lint（ESLint 9 flat config，各 workspace 自有 eslint.config.js）
pnpm typecheck           # tsc --noEmit
pnpm format              # prettier --write
```

### 后端 server（Go module 在 `server/` 子目录）

```bash
cd server
go run ./cmd/server                        # 默认监听 :8080
DB_SQLITE_PATH=./meridian-dev.db go run ./cmd/server   # SQLite 本地开发（PG_DSN 为空时生效）
go build ./... && go vet ./... && go test ./...        # CI 同款检查
```

本地依赖服务（可选，SQLite 模式不需要）：`docker compose up -d`（PostgreSQL :5432 / Redis :6379 / NATS :4222+8222）。
环境变量：`cp .env.example .env`，server 启动时自动加载 `.env` 或 `../.env`。

### collectors / migrator / mocks（各自独立 Go module）

```bash
cd collectors && go build ./cmd/collector && go test ./...
./collector -collector=all                 # 或 -collector=aliyun,librenms；ipscan 支持 -dry-run
cd mocks && go run ./cmd/mockd             # 6 个 mock 系统：n9e :19001 / NetBox :19002 / LibreNMS :19003 / TSDB :19004 / 阿里云 :19005 / 火山 :19006
cd migrator && go run ./cmd/migrate        # NetBox → CMDB 迁移，需 NETBOX_API_URL / NETBOX_TOKEN
```

### 一键演示与种子数据

```bash
bash scripts/demo.sh          # 无需 Docker：SQLite 临时库 + 种子模型 + 样例发现记录，结束自动清理
bash scripts/seed-models.sh   # 向运行中的 server（默认 :8080）导入八层种子模型（定义见 scripts/seed/）
```

脚本统一先经 `scripts/auth-login.sh` 登录；可用 `MERIDIAN_AUTH_USER` / `MERIDIAN_AUTH_PASSWORD` 覆盖账号。

## 架构与模块划分

### server/internal（后端）

- `httpapi/` — Gin 路由与 handler：`models`（模型）、`cis`（CI 实例）、`relations`、`discovery`（发现记录）、`pool`（发现池）、`ipam`、`dcim`、`search`（全局搜索）、`users`/`roles`/`auth`（认证与 RBAC）、`oxidized`（集成输出）。
- `auth/` — JWT（httpOnly cookie `meridian_token` 或 Bearer）+ Casbin RBAC；权限点为代码内固定目录 `auth/catalog.go`，策略经 gorm-adapter 持久化到 `casbin_rule` 表。
- `reconcile/` — 调和引擎：发现记录 → CI，支持撞键检测、冲突入池、增量更新。
- `discovery/`、`n9e/`（n9e 消费器）、`stream/`（NATS JetStream，不可达时自动跳过订阅）、`ipam/`、`dcim/`、`store/`、`db/`（GORM，PostgreSQL 或 glebarez/sqlite）、`validation/`、`config/`（全部配置来自环境变量）。

### apps/web（前端）

- App Router 页面：`/`（全局搜索 landing）、`models`、`hosts`、`ipam`、`dcim`、`pool`、`settings/users`、`settings/roles`、`login`。
- `proxy.ts` — Next 16 的 Proxy（原 middleware），做无 cookie 预检跳登录页；cookie 名 `meridian_token` 必须与后端一致。
- `lib/api.ts` — 请求层，`401` 自动跳 `/login`；`components/` 为页面级组件，`components/ui` 与 `@workspace/ui` 为 shadcn/ui 基础组件。

### 数据流

采集器（collectors）/ n9e 消费器 → `POST /api/v1/discovery-records` → 调和引擎（reconcile）→ CI 实例；撞键/冲突进发现池（pool）人工确认。mock 平台（mocks）为采集器与迁移工具提供源端数据，fixture 在同一套虚拟拓扑内互相呼应，可直接演示多源调和。

## 契约先行流程（重要约定）

前后端以 `pkg/openapi/*.yaml` 为**唯一事实来源**。任何接口变更：先改契约并写明理由 → 评审冻结 → 前后端并行实现 → 提交前校验契约可解析：

```bash
pnpm dlx js-yaml pkg/openapi/openapi.yaml
pnpm dlx js-yaml pkg/openapi/ipam-dcim.yaml
```

不得绕过契约直接改实现。

## 代码风格

- **文档与注释语言：中文**。Go 代码包注释、README、提交说明均用中文，新代码保持一致。
- Go：标准 `gofmt`/`go vet`；collectors、mocks 刻意零三方依赖（纯标准库），新增依赖需谨慎。server 才有外部依赖（Gin/GORM/Casbin/JWT/NATS 等）。
- 前端 Prettier（根 `.prettierrc`）：无分号、双引号、2 空格、printWidth 80、`prettier-plugin-tailwindcss`（`cn`/`cva` 函数内类名排序，样式表为 `packages/ui/src/styles/globals.css`）。
- ESLint 9 flat config，规则在各 workspace 的 `eslint.config.js`；根 `.eslintrc.js` 仅做 ignore。
- TypeScript 严格模式，共享 tsconfig 来自 `@workspace/typescript-config`。
- shadcn/ui 组件集中在 `packages/ui`（`@workspace/ui`）与 `apps/web/components/ui`，页面组件放 `apps/web/components`。
- **Next.js 16 有破坏性变更**（如 middleware → proxy），写代码前先查 `node_modules/next/dist/docs/`。

## 测试

- 后端与 Go 工具均用 `go test ./...`：server 的 `httpapi`/`auth`/`reconcile`/`ipam`/`dcim`/`n9e`/`validation` 有单测；collectors 用 httptest 夹具 + 内嵌 nmap XML；migrator 有 client/migrate 单测。
- 前端**目前无单元测试**，质量门禁为 `pnpm lint` + `pnpm typecheck` + `pnpm build`。
- CI（`.github/workflows/ci.yml`，push 到 main 及 PR 触发）：web 任务跑 `pnpm install --frozen-lockfile && pnpm lint && pnpm build`（Node 24）；server 任务在 `server/` 目录跑 `go build ./... && go vet ./... && go test ./...`（Go 1.26.x）。提交前本地至少跑通对应任务。

## 认证、RBAC 与安全注意事项

- 除 `/healthz`、`/readyz`、`/api/v1/auth/login` 外，所有 `/api/v1` 接口需登录并按权限点鉴权。
- 内置账号（首次启动种子）：`admin`（默认初始密码 `admin123`，环境变量 `ADMIN_INITIAL_PASSWORD` 可改）、`collector`（默认 `collector123`，仅供采集器上报，权限点 `discovery:write`）。内置角色 admin/operator/viewer/collector 不可删除，admin 角色权限点不可修改。
- `JWT_SECRET` 生产环境**必须显式配置**（缺省为固定开发值并打印警告）；会话有效期 `TOKEN_TTL_HOURS` 默认 24 小时。
- 密码以 bcrypt 哈希存储；token 经 httpOnly cookie 或 Bearer 携带。
- `.env` 含数据库口令等敏感信息，已在 `.gitignore`，不要提交；docker-compose 中的 `cmdb_dev_password` 仅为本地开发口令。
- 演示登录：浏览器访问 http://localhost:3000，先以 admin 登录再体验各页面。
