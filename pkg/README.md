# pkg — 共享契约与类型

本目录存放前后端共享的契约产物，当前包含：

- `openapi/openapi.yaml` — CMDB 核心 API 契约（OpenAPI 3.0.3），覆盖模型、CI、发现记录、调和预览、发现池等接口骨架。
- `openapi/ipam-dcim.yaml` — IPAM（前缀/IP/分配/利用率）、DCIM（机柜 U 位上下架）与集成输出（Oxidized 设备清单）契约（OpenAPI 3.0.3）。

## 契约先行流程

前后端以契约为唯一事实来源，任何接口变更按以下流程推进：

1. **改契约**：修改 `openapi/openapi.yaml`（或对应分域契约文件），写明变更理由；不得绕过契约直接改实现。
2. **评审**：提交评审（前后端至少各一人），确认字段、校验规则、响应码语义一致后冻结。
3. **并行实现**：后端按契约实现 Gin 路由与校验，前端按契约生成/手写类型与请求层，双方互不等待。
4. **校验**：提交前确保契约可解析，例如在项目根目录执行：

   ```bash
   pnpm dlx js-yaml pkg/openapi/openapi.yaml
   pnpm dlx js-yaml pkg/openapi/ipam-dcim.yaml
   ```
