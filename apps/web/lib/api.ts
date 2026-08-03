// CMDB API 强类型客户端
// 契约真源：pkg/openapi/openapi.yaml，本文件类型与该契约一一对应。
// 所有请求走同源 /api，开发期由 next.config rewrites 代理到 Go 后端（默认 http://localhost:8080）。

/** 属性数据类型（契约六枚举） */
export type AttributeType =
  | "string"
  | "number"
  | "bool"
  | "enum"
  | "ip"
  | "date"

/** 属性定义 */
export interface AttributeDefinition {
  /** 属性显示名 */
  name: string
  /** 属性编码（模型内唯一） */
  code: string
  type: AttributeType
  /** 是否必填 */
  required?: boolean
  /** 是否模型内唯一 */
  unique?: boolean
  /** type 为 enum 时的候选值 */
  enum_values?: string[]
  /** 字符串格式校验正则 */
  regex?: string
  /** 属性来源（人工/采集器标识） */
  source?: string
  /** 属性分组（Core/Capability/Context），契约外的可选扩展；后端未返回时前端按 source 兜底分组 */
  group?: string
}

/** 关系基数约束 */
export type RelationCardinality = "one_to_one" | "one_to_many" | "many_to_many"

/** 关系方向（相对当前模型/CI） */
export type RelationDirection = "outgoing" | "incoming"

/** 关系定义 */
export interface RelationDefinition {
  name: string
  code: string
  /** 对端模型编码 */
  target_model: string
  cardinality: RelationCardinality
  direction: RelationDirection
}

/** 模型 */
export interface Model {
  id: string
  name: string
  code: string
  attributes: AttributeDefinition[]
  relations: RelationDefinition[]
  created_at: string
  updated_at: string
}

export interface ModelCreateRequest {
  name: string
  code: string
  attributes?: AttributeDefinition[]
  relations?: RelationDefinition[]
}

/** 全字段可选，仅更新传入字段 */
export interface ModelPatchRequest {
  name?: string
  attributes?: AttributeDefinition[]
  relations?: RelationDefinition[]
}

/** CI 生命周期状态 */
export type CIStatus = "discovered" | "active" | "retired"

/** CI 实例 */
export interface CI {
  id: string
  model_id: string
  /** 属性键值对，键为属性编码 */
  attributes: Record<string, unknown>
  status: CIStatus
  /** 建档来源（manual/采集器标识） */
  source: string
  created_at: string
  updated_at: string
}

export interface CICreateRequest {
  model_id: string
  attributes: Record<string, unknown>
  status?: CIStatus
  source?: string
}

/** 全字段可选，attributes 为增量合并 */
export interface CIPatchRequest {
  attributes?: Record<string, unknown>
  status?: CIStatus
  source?: string
}

/** CI 关系（含对端 CI 摘要） */
export interface CIRelation {
  relation_code: string
  /** 相对当前 CI 的方向 */
  direction: RelationDirection
  peer_ci: CI
}

/** 标准发现记录 */
export interface DiscoveryRecord {
  /** 发现来源系统 */
  source: string
  /** 采集器标识 */
  collector: string
  /** 候选模型编码 */
  model_candidate: string
  attributes: Record<string, unknown>
  occurred_at: string
}

export interface DiscoveryRecordBatchResponse {
  accepted: number
  rejected: number
  errors: { index: number; message: string }[]
}

/** 分页响应 */
export interface Paged<T> {
  items: T[]
  total: number
  page: number
  page_size: number
}

/** 契约错误响应体 */
export interface ApiErrorBody {
  code: string
  message: string
  details?: Record<string, unknown>
}

/** API 调用失败时抛出的错误，保留契约错误码与字段级详情 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly details?: Record<string, unknown>

  constructor(status: number, body: ApiErrorBody) {
    super(body.message)
    this.name = "ApiError"
    this.status = status
    this.code = body.code
    this.details = body.details
  }
}

type Query = Record<string, string | number | undefined>

const BASE = "/api"

function buildQuery(query?: Query): string {
  if (!query) return ""
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value))
  }
  const qs = params.toString()
  return qs ? `?${qs}` : ""
}

async function request<T>(
  path: string,
  init?: { method?: string; query?: Query; body?: unknown }
): Promise<T> {
  const res = await fetch(`${BASE}${path}${buildQuery(init?.query)}`, {
    method: init?.method ?? "GET",
    headers:
      init?.body !== undefined
        ? { "Content-Type": "application/json" }
        : undefined,
    body: init?.body !== undefined ? JSON.stringify(init.body) : undefined,
  })
  if (!res.ok) {
    let body: ApiErrorBody = {
      code: `HTTP_${res.status}`,
      message: `请求失败（${res.status}）`,
    }
    try {
      const parsed: unknown = await res.json()
      if (
        typeof parsed === "object" &&
        parsed !== null &&
        "code" in parsed &&
        "message" in parsed
      ) {
        body = parsed as ApiErrorBody
      }
    } catch {
      // 非 JSON 错误响应，使用默认错误体
    }
    // 会话失效（非登录接口本身）：跳转登录页并携带回跳地址
    if (
      res.status === 401 &&
      !path.startsWith("/v1/auth/login") &&
      typeof window !== "undefined"
    ) {
      const here = window.location.pathname + window.location.search
      if (!window.location.pathname.startsWith("/login")) {
        window.location.href = `/login?redirect=${encodeURIComponent(here)}`
      }
    }
    throw new ApiError(res.status, body)
  }
  // 部分写操作（如忽略发现记录、卸载 U 位）可能返回空响应体，先读文本再解析
  const text = await res.text()
  return text ? (JSON.parse(text) as T) : (undefined as T)
}

// ---------- models ----------

export interface ListModelsParams {
  page?: number
  page_size?: number
  /** 按名称或编码模糊过滤 */
  keyword?: string
}

export function listModels(
  params: ListModelsParams = {}
): Promise<Paged<Model>> {
  return request<Paged<Model>>("/v1/models", { query: { ...params } })
}

export function getModel(modelId: string): Promise<Model> {
  return request<Model>(`/v1/models/${encodeURIComponent(modelId)}`)
}

export function createModel(body: ModelCreateRequest): Promise<Model> {
  return request<Model>("/v1/models", { method: "POST", body })
}

export function patchModel(
  modelId: string,
  body: ModelPatchRequest
): Promise<Model> {
  return request<Model>(`/v1/models/${encodeURIComponent(modelId)}`, {
    method: "PATCH",
    body,
  })
}

// ---------- cis ----------

export interface ListCIsParams {
  /** 按所属模型过滤 */
  model_id?: string
  status?: CIStatus
  /** 全文关键字（匹配全部属性值，大小写不敏感） */
  keyword?: string
  page?: number
  page_size?: number
}

export function listCIs(params: ListCIsParams = {}): Promise<Paged<CI>> {
  return request<Paged<CI>>("/v1/cis", { query: { ...params } })
}

export function createCI(body: CICreateRequest): Promise<CI> {
  return request<CI>("/v1/cis", { method: "POST", body })
}

export function getCI(ciId: string): Promise<CI> {
  return request<CI>(`/v1/cis/${encodeURIComponent(ciId)}`)
}

export function patchCI(ciId: string, body: CIPatchRequest): Promise<CI> {
  return request<CI>(`/v1/cis/${encodeURIComponent(ciId)}`, {
    method: "PATCH",
    body,
  })
}

export function listCIRelations(
  ciId: string
): Promise<{ items: CIRelation[] }> {
  return request<{ items: CIRelation[] }>(
    `/v1/cis/${encodeURIComponent(ciId)}/relations`
  )
}

export function createCIRelation(
  ciId: string,
  body: { relation_code: string; peer_ci_id: string }
): Promise<CIRelation> {
  return request<CIRelation>(`/v1/cis/${encodeURIComponent(ciId)}/relations`, {
    method: "POST",
    body,
  })
}

export function deleteCIRelation(
  ciId: string,
  relationCode: string,
  peerCiId: string
): Promise<void> {
  return request<void>(
    `/v1/cis/${encodeURIComponent(ciId)}/relations/${encodeURIComponent(relationCode)}/${encodeURIComponent(peerCiId)}`,
    { method: "DELETE" }
  )
}

// ---------- discovery ----------

export function createDiscoveryRecords(
  records: DiscoveryRecord[]
): Promise<DiscoveryRecordBatchResponse> {
  return request<DiscoveryRecordBatchResponse>("/v1/discovery-records", {
    method: "POST",
    body: { records },
  })
}

// ---------- discovery pool（发现池） ----------

/** 发现池条目状态 */
export type PoolStatus = "pending" | "confirmed" | "ignored"

/** 调和动作 */
export type ReconcileAction = "create" | "update" | "conflict"

/** 发现池条目 */
export interface PoolItem {
  id: string
  /** 发现来源系统 */
  source: string
  /** 采集器标识 */
  collector: string
  /** 候选模型编码 */
  model_candidate: string
  attributes: Record<string, unknown>
  reconcile_action: ReconcileAction
  /** 调和判定理由 */
  reasons: string[]
  status: PoolStatus
  created_at: string
}

export interface ListPoolParams {
  status?: PoolStatus
  page?: number
  page_size?: number
}

export function listDiscoveryPool(
  params: ListPoolParams = {}
): Promise<Paged<PoolItem>> {
  return request<Paged<PoolItem>>("/v1/discovery-pool", {
    query: { ...params },
  })
}

export interface ConfirmPoolItemRequest {
  /** 目标模型 id；缺省时由后端按候选模型解析 */
  model_id?: string
  /** 确认入库时的最终属性（缺省沿用发现属性） */
  attributes?: Record<string, unknown>
}

/** 确认入库：201 返回新建/更新后的 CI */
export function confirmPoolItem(
  itemId: string,
  body: ConfirmPoolItemRequest = {}
): Promise<CI> {
  return request<CI>(
    `/v1/discovery-pool/${encodeURIComponent(itemId)}/confirm`,
    {
      method: "POST",
      body,
    }
  )
}

export function ignorePoolItem(itemId: string): Promise<void> {
  return request<void>(
    `/v1/discovery-pool/${encodeURIComponent(itemId)}/ignore`,
    {
      method: "POST",
    }
  )
}

// ---------- IPAM ----------

/** 前缀利用率统计 */
export interface PrefixUtilization {
  total_ips: number
  used_ips: number
  /** 利用率，约定为 0-1 小数；展示层兼容 0-100 百分数 */
  utilization: number
}

/** IPAM 前缀（子网） */
export interface IpamPrefix {
  id: string
  cidr: string
  name: string
  vlan_id?: number | null
  description?: string
  parent_id?: string | null
  utilization?: PrefixUtilization
  /** 仅详情接口返回 */
  children?: IpamPrefix[]
  created_at?: string
  updated_at?: string
}

export interface ListPrefixesParams {
  keyword?: string
  page?: number
  page_size?: number
}

export function listPrefixes(
  params: ListPrefixesParams = {}
): Promise<Paged<IpamPrefix>> {
  return request<Paged<IpamPrefix>>("/v1/ipam/prefixes", {
    query: { ...params },
  })
}

export function getPrefix(prefixId: string): Promise<IpamPrefix> {
  return request<IpamPrefix>(
    `/v1/ipam/prefixes/${encodeURIComponent(prefixId)}`
  )
}

export interface PrefixCreateRequest {
  cidr: string
  name: string
  vlan_id?: number
  description?: string
  parent_id?: string
}

export function createPrefix(body: PrefixCreateRequest): Promise<IpamPrefix> {
  return request<IpamPrefix>("/v1/ipam/prefixes", { method: "POST", body })
}

export interface AllocateIPsRequest {
  count: number
  description?: string
}

/**
 * 自动分配 IP。契约未固定响应形状，统一规整为已分配的 IP 字符串列表用于展示；
 * 分配后的权威数据以重新拉取的 IP 列表为准。
 */
export async function allocateIPs(
  prefixId: string,
  body: AllocateIPsRequest
): Promise<string[]> {
  const res = await request<unknown>(
    `/v1/ipam/prefixes/${encodeURIComponent(prefixId)}/allocate`,
    {
      method: "POST",
      body,
    }
  )
  return extractAllocatedIPs(res)
}

function extractAllocatedIPs(res: unknown): string[] {
  // 兼容三种返回：["10.0.0.1"]、[{ip: "10.0.0.1", ...}]、{items: [...]}
  const list = Array.isArray(res)
    ? res
    : Array.isArray((res as { items?: unknown[] })?.items)
      ? (res as { items: unknown[] }).items
      : []
  return list
    .map((item) => {
      if (typeof item === "string") return item
      if (typeof item === "object" && item !== null && "ip" in item) {
        return String((item as { ip: unknown }).ip)
      }
      return ""
    })
    .filter(Boolean)
}

/** IPAM IP 记录 */
export interface IpamIP {
  id: string
  prefix_id: string
  ip: string
  status: string
  ci_id?: string | null
  description?: string
  created_at?: string
  updated_at?: string
}

export interface ListIPsParams {
  prefix_id?: string
  status?: string
  keyword?: string
  page?: number
  page_size?: number
}

export function listIPs(params: ListIPsParams = {}): Promise<Paged<IpamIP>> {
  return request<Paged<IpamIP>>("/v1/ipam/ips", { query: { ...params } })
}

export interface IPCreateRequest {
  prefix_id: string
  ip: string
  status?: string
  ci_id?: string
  description?: string
}

export function createIP(body: IPCreateRequest): Promise<IpamIP> {
  return request<IpamIP>("/v1/ipam/ips", { method: "POST", body })
}

// ---------- DCIM ----------

/** 机柜单个 U 位的占用情况 */
export interface RackUnit {
  u: number
  occupant_ci_id?: string | null
  occupant_name?: string | null
}

export interface RackUnitsResponse {
  rack_id: string
  u_total: number
  units: RackUnit[]
}

export function getRackUnits(rackCiId: string): Promise<RackUnitsResponse> {
  return request<RackUnitsResponse>(
    `/v1/dcim/racks/${encodeURIComponent(rackCiId)}/units`
  )
}

export interface MountRequest {
  ci_id: string
  u_position: number
  u_height: number
}

export function mountRackUnit(
  rackCiId: string,
  body: MountRequest
): Promise<void> {
  return request<void>(`/v1/dcim/racks/${encodeURIComponent(rackCiId)}/mount`, {
    method: "POST",
    body,
  })
}

export function unmountRackUnit(rackCiId: string, ciId: string): Promise<void> {
  return request<void>(
    `/v1/dcim/racks/${encodeURIComponent(rackCiId)}/unmount`,
    {
      method: "POST",
      body: { ci_id: ciId },
    }
  )
}

// ---------- Oxidized 集成 ----------

/** 供给 Oxidized HTTP source 的网络设备清单条目 */
export interface OxidizedDevice {
  name: string
  ip: string
  model: string
  group: string
}

export function listOxidizedDevices(): Promise<OxidizedDevice[]> {
  return request<OxidizedDevice[]>("/v1/integrations/oxidized/devices")
}

// ---------- 认证 ----------

/** 当前登录用户（含角色与权限点并集） */
export interface CurrentUser {
  id: string
  username: string
  display_name: string
  /** 角色编码数组 */
  roles: string[]
  /** 权限点并集（如 ci:read） */
  permissions: string[]
}

export interface LoginResponse {
  token: string
  user: CurrentUser
}

export function login(
  username: string,
  password: string
): Promise<LoginResponse> {
  return request<LoginResponse>("/v1/auth/login", {
    method: "POST",
    body: { username, password },
  })
}

export function logout(): Promise<{ status: string }> {
  return request<{ status: string }>("/v1/auth/logout", { method: "POST" })
}

export function getCurrentUser(): Promise<CurrentUser> {
  return request<CurrentUser>("/v1/auth/me")
}

// ---------- 用户管理 ----------

export type UserStatus = "active" | "disabled"

/** 用户（不含密码哈希） */
export interface User {
  id: string
  username: string
  display_name: string
  status: UserStatus
  /** 角色编码数组 */
  roles: string[]
  is_builtin: boolean
  created_at: string
  updated_at: string
}

export interface UserCreateRequest {
  username: string
  display_name: string
  password: string
  roles?: string[]
}

/** 全字段可选；password 传入即重置密码，roles 传入即整体替换角色 */
export interface UserPatchRequest {
  display_name?: string
  status?: UserStatus
  password?: string
  roles?: string[]
}

export interface ListUsersParams {
  keyword?: string
  page?: number
  page_size?: number
}

export function listUsers(params: ListUsersParams = {}): Promise<Paged<User>> {
  return request<Paged<User>>("/v1/users", { query: { ...params } })
}

export function createUser(body: UserCreateRequest): Promise<User> {
  return request<User>("/v1/users", { method: "POST", body })
}

export function patchUser(
  userId: string,
  body: UserPatchRequest
): Promise<User> {
  return request<User>(`/v1/users/${encodeURIComponent(userId)}`, {
    method: "PATCH",
    body,
  })
}

// ---------- 角色与权限点 ----------

/** 角色（权限与用户数由服务端实时聚合） */
export interface Role {
  id: string
  code: string
  name: string
  description: string
  /** 权限点编码数组 */
  permissions: string[]
  user_count: number
  is_builtin: boolean
  created_at: string
  updated_at: string
}

export interface RoleCreateRequest {
  code: string
  name: string
  description?: string
  permissions: string[]
}

/** 全字段可选；permissions 传入即整体替换权限点 */
export interface RolePatchRequest {
  name?: string
  description?: string
  permissions?: string[]
}

/** 权限点目录条目 */
export interface PermissionItem {
  code: string
  name: string
  description: string
}

export function listRoles(): Promise<{ items: Role[] }> {
  return request<{ items: Role[] }>("/v1/roles")
}

export function createRole(body: RoleCreateRequest): Promise<Role> {
  return request<Role>("/v1/roles", { method: "POST", body })
}

export function patchRole(
  roleId: string,
  body: RolePatchRequest
): Promise<Role> {
  return request<Role>(`/v1/roles/${encodeURIComponent(roleId)}`, {
    method: "PATCH",
    body,
  })
}

export function deleteRole(roleId: string): Promise<void> {
  return request<void>(`/v1/roles/${encodeURIComponent(roleId)}`, {
    method: "DELETE",
  })
}

export function listPermissions(): Promise<{ items: PermissionItem[] }> {
  return request<{ items: PermissionItem[] }>("/v1/permissions")
}

// ---------- DCIM 容量总览 ----------

/** 单个机房的容量聚合 */
export interface DCIMRoomStat {
  room_id: string
  name: string
  code: string
  address: string
  rack_count: number
  u_total: number
  u_used: number
  power_capacity_kw: number
}

/** 逐机柜明细（room_id 为 null 表示未分配机房） */
export interface DCIMRackStat {
  rack_id: string
  room_id: string | null
  name: string
  u_total: number
  u_used: number
  power_capacity_kw: number
}

/** 未分配机房的机柜聚合 */
export interface DCIMUnassignedStat {
  rack_count: number
  u_total: number
  u_used: number
  power_capacity_kw: number
}

/** DCIM 全局容量总览 */
export interface DCIMOverview {
  room_count: number
  rack_count: number
  u_total: number
  u_used: number
  power_capacity_kw: number
  rooms: DCIMRoomStat[]
  racks: DCIMRackStat[]
  unassigned: DCIMUnassignedStat
}

export function getDCIMOverview(): Promise<DCIMOverview> {
  return request<DCIMOverview>("/v1/dcim/overview")
}

// ---------- 全局搜索 ----------

/** 搜索结果项 */
export interface SearchItem {
  kind: "model" | "ci" | "ipam_prefix" | "ipam_ip"
  id: string
  title: string
  subtitle: string
  matched?: string
  /** 仅 kind=ci 时返回，前端据此决定跳转目标 */
  model_code?: string
}

/** 搜索结果分组（仅非空分组返回） */
export interface SearchGroup {
  kind: "models" | "cis" | "ipam"
  label: string
  items: SearchItem[]
}

export interface SearchResponse {
  query: string
  groups: SearchGroup[]
}

export function globalSearch(
  q: string,
  limit?: number
): Promise<SearchResponse> {
  return request<SearchResponse>("/v1/search", { query: { q, limit } })
}

// ---------- 凭据管理（明文 secret 永不回读，仅可轮换） ----------

/** 凭据类型（契约九枚举） */
export type CredentialType =
  | "vcenter"
  | "aliyun"
  | "volc"
  | "snmp"
  | "db"
  | "kubeconfig"
  | "ssh_ipmi"
  | "n9e"
  | "netbox"

/** 凭据（不含 secret 明文） */
export interface Credential {
  id: string
  name: string
  type: CredentialType
  description?: string
  /** 最近一次轮换时间 */
  last_rotated_at?: string
  /** 被任务引用/使用次数 */
  use_count: number
  created_at: string
  updated_at: string
}

export interface CredentialCreateRequest {
  name: string
  type: CredentialType
  description?: string
  /** 密文键值对，字段随类型而变 */
  secret: Record<string, unknown>
}

/** 全字段可选；secret 不在此更新，走轮换接口 */
export interface CredentialPatchRequest {
  name?: string
  description?: string
}

export interface ListCredentialsParams {
  type?: CredentialType
  page?: number
  page_size?: number
}

export function listCredentials(
  params: ListCredentialsParams = {}
): Promise<Paged<Credential>> {
  return request<Paged<Credential>>("/v1/credentials", {
    query: { ...params },
  })
}

export function createCredential(
  body: CredentialCreateRequest
): Promise<Credential> {
  return request<Credential>("/v1/credentials", { method: "POST", body })
}

export function patchCredential(
  credentialId: string,
  body: CredentialPatchRequest
): Promise<Credential> {
  return request<Credential>(
    `/v1/credentials/${encodeURIComponent(credentialId)}`,
    { method: "PATCH", body }
  )
}

/** 轮换：重新录入完整 secret */
export function rotateCredential(
  credentialId: string,
  secret: Record<string, unknown>
): Promise<Credential> {
  return request<Credential>(
    `/v1/credentials/${encodeURIComponent(credentialId)}/rotate`,
    { method: "POST", body: { secret } }
  )
}

/** 凭据操作审计条目 */
export interface CredentialAudit {
  id: string
  /** 动作（create/rotate/use 等） */
  action: string
  /** 操作者（用户名或采集器标识） */
  operator: string
  /** 来源（web/console/collector 等） */
  source: string
  created_at: string
}

export interface ListCredentialAuditsParams {
  page?: number
  page_size?: number
}

export function listCredentialAudits(
  credentialId: string,
  params: ListCredentialAuditsParams = {}
): Promise<Paged<CredentialAudit>> {
  return request<Paged<CredentialAudit>>(
    `/v1/credentials/${encodeURIComponent(credentialId)}/audits`,
    { query: { ...params } }
  )
}

// ---------- 采集任务 ----------

/** 任务状态 */
export type DiscoveryTaskStatus = "idle" | "running" | "error"

/** 采集任务 */
export interface DiscoveryTask {
  id: string
  name: string
  /** 采集器类型：builtin:<name> 或 exec:<binary> */
  collector_type: string
  /** 关联凭据 id（可选） */
  credential_id?: string | null
  /** 调度间隔（秒） */
  interval_seconds: number
  enabled: boolean
  /** 采集器特定配置（exec 模式含 binary/args/timeout 等） */
  config: Record<string, unknown>
  status: DiscoveryTaskStatus
  last_run_at?: string | null
  last_success_at?: string | null
  last_error?: string | null
  run_count: number
  fail_count: number
  created_at: string
  updated_at: string
}

export interface DiscoveryTaskCreateRequest {
  name: string
  collector_type: string
  credential_id?: string
  interval_seconds: number
  enabled?: boolean
  config?: Record<string, unknown>
}

/** 全字段可选，仅更新传入字段 */
export interface DiscoveryTaskPatchRequest {
  name?: string
  credential_id?: string
  interval_seconds?: number
  enabled?: boolean
  config?: Record<string, unknown>
}

/** 任务运行记录 */
export interface DiscoveryTaskRun {
  id: string
  task_id: string
  started_at: string
  finished_at?: string | null
  success: boolean
  /** 本次产出发现记录条数 */
  produced: number
  error_summary?: string | null
}

export interface ListDiscoveryTasksParams {
  page?: number
  page_size?: number
}

export function listDiscoveryTasks(
  params: ListDiscoveryTasksParams = {}
): Promise<Paged<DiscoveryTask>> {
  return request<Paged<DiscoveryTask>>("/v1/discovery/tasks", {
    query: { ...params },
  })
}

export function createDiscoveryTask(
  body: DiscoveryTaskCreateRequest
): Promise<DiscoveryTask> {
  return request<DiscoveryTask>("/v1/discovery/tasks", {
    method: "POST",
    body,
  })
}

export function patchDiscoveryTask(
  taskId: string,
  body: DiscoveryTaskPatchRequest
): Promise<DiscoveryTask> {
  return request<DiscoveryTask>(
    `/v1/discovery/tasks/${encodeURIComponent(taskId)}`,
    { method: "PATCH", body }
  )
}

/** 手动运行响应：契约未固定形状，原样透传给调用方提取提示文案 */
export type RunDiscoveryTaskResponse = Record<string, unknown>

export function runDiscoveryTask(
  taskId: string
): Promise<RunDiscoveryTaskResponse> {
  return request<RunDiscoveryTaskResponse>(
    `/v1/discovery/tasks/${encodeURIComponent(taskId)}/run`,
    { method: "POST" }
  )
}

export interface ListDiscoveryTaskRunsParams {
  page?: number
  page_size?: number
}

export function listDiscoveryTaskRuns(
  taskId: string,
  params: ListDiscoveryTaskRunsParams = {}
): Promise<Paged<DiscoveryTaskRun>> {
  return request<Paged<DiscoveryTaskRun>>(
    `/v1/discovery/tasks/${encodeURIComponent(taskId)}/runs`,
    { query: { ...params } }
  )
}

// ---------- 告警事件（黑设备/系统事件，AC-F043-01） ----------

export interface AlertEvent {
  id: string
  level: string
  title: string
  source: string
  ci_id?: string
  detail?: string
  acknowledged: boolean
  created_at: string
}

export interface ListAlertsParams {
  acknowledged?: boolean
  page?: number
  page_size?: number
}

export function listAlerts(params: ListAlertsParams = {}): Promise<Paged<AlertEvent>> {
  const query: Record<string, string | number | undefined> = {
    page: params.page,
    page_size: params.page_size,
  }
  if (params.acknowledged !== undefined) query.acknowledged = String(params.acknowledged)
  return request<Paged<AlertEvent>>(`/v1/alerts`, { query })
}

export function ackAlert(alertId: string): Promise<AlertEvent> {
  return request<AlertEvent>(`/v1/alerts/${encodeURIComponent(alertId)}/ack`, {
    method: "POST",
  })
}

// ---------- n9e 集成（监控视图与当前告警，时序数据不入主库） ----------

/** n9e 监控视图地址响应 */
export interface N9EDashboardURLResponse {
  url: string
}

/** n9e 原始告警条目（字段名与 n9e API 一致，仅消费展示所需字段，其余原样保留） */
export interface N9EAlertItem {
  severity?: string | number
  trigger?: string
  /** 首次触发时间（n9e 返回 unix 秒，兼容字符串/ISO） */
  first_trigger_time?: number | string
  [key: string]: unknown
}

export function getN9EDashboardURL(ident: string): Promise<N9EDashboardURLResponse> {
  return request<N9EDashboardURLResponse>("/v1/integrations/n9e/dashboard-url", {
    query: { ident },
  })
}

export function listN9EAlerts(ident: string): Promise<N9EAlertItem[]> {
  return request<N9EAlertItem[]>("/v1/integrations/n9e/alerts", {
    query: { ident },
  })
}

// ---------- 数据质量看板（F-080，五指标 + 下钻缺失清单） ----------

/** 单模型质量指标；百分比字段约定为 0-100 */
export interface ModelQualityMetric {
  model_id: string
  code: string
  name: string
  /** 属性完整率 */
  completeness: number
  /** 关联完整率 */
  relation_completeness: number
  /** 孤岛 CI 数（无任何关系的 CI） */
  orphan_count: number
  /** 数据鲜度：陈旧 CI 占比 */
  stale_pct: number
  /** 监控覆盖率指标：在用主机无 n9e 心跳占比（部分模型不适用，可缺省） */
  no_heartbeat_pct?: number
}

/** 监控覆盖率双指标（全局口径） */
export interface MonitorCoverage {
  /** CMDB 在用主机无 n9e 心跳占比 */
  no_heartbeat_pct: number
  /** n9e targets 无 CMDB CI 占比 */
  no_ci_pct: number
}

export interface QualityDashboard {
  models: ModelQualityMetric[]
  monitor: MonitorCoverage
}

export function getQualityDashboard(): Promise<QualityDashboard> {
  return request<QualityDashboard>("/v1/dashboard/quality")
}

/** 质量指标下钻维度 */
export type QualityMetricKey =
  | "completeness"
  | "relation_completeness"
  | "orphan"
  | "stale"
  | "no_heartbeat"

export interface QualityDrilldownParams {
  model_id: string
  metric: QualityMetricKey
  page?: number
  page_size?: number
}

/** 下钻返回标准 CI 分页：对应指标在该模型下的缺失/异常 CI 清单 */
export function getQualityDrilldown(
  params: QualityDrilldownParams
): Promise<Paged<CI>> {
  return request<Paged<CI>>("/v1/dashboard/quality/drilldown", {
    query: { ...params },
  })
}

// ---------- 稽核规则与整改待办（F-081） ----------

/** 稽核规则（声明式：模型过滤条件 + 断言表达式 + 待办模板） */
export interface GovernanceRule {
  id: string
  name: string
  /** 目标模型编码 */
  model_code: string
  /** 过滤条件表达式（圈定规则适用的 CI 范围） */
  filter: string
  /** 断言表达式（不满足即违规） */
  assertion: string
  /** 生成整改待办时的文案模板 */
  message: string
  enabled: boolean
  /** 演练模式：只评估不生成待办 */
  dry_run: boolean
  /** 最近一次执行时间（手动或定时） */
  last_run_at?: string | null
  created_at?: string
  updated_at?: string
}

export interface GovernanceRuleRequest {
  name: string
  model_code: string
  filter: string
  assertion: string
  message: string
  enabled?: boolean
  dry_run?: boolean
}

/** 全字段可选，仅更新传入字段 */
export type GovernanceRulePatchRequest = Partial<GovernanceRuleRequest>

export interface ListGovernanceRulesParams {
  page?: number
  page_size?: number
}

export function listGovernanceRules(
  params: ListGovernanceRulesParams = {}
): Promise<Paged<GovernanceRule>> {
  return request<Paged<GovernanceRule>>("/v1/governance/rules", {
    query: { ...params },
  })
}

export function createGovernanceRule(
  body: GovernanceRuleRequest
): Promise<GovernanceRule> {
  return request<GovernanceRule>("/v1/governance/rules", {
    method: "POST",
    body,
  })
}

export function patchGovernanceRule(
  ruleId: string,
  body: GovernanceRulePatchRequest
): Promise<GovernanceRule> {
  return request<GovernanceRule>(
    `/v1/governance/rules/${encodeURIComponent(ruleId)}`,
    { method: "PATCH", body }
  )
}

/** 手动执行响应：契约未固定形状，原样透传给调用方提取提示文案 */
export type RunGovernanceRuleResponse = Record<string, unknown>

export function runGovernanceRule(
  ruleId: string
): Promise<RunGovernanceRuleResponse> {
  return request<RunGovernanceRuleResponse>(
    `/v1/governance/rules/${encodeURIComponent(ruleId)}/run`,
    { method: "POST" }
  )
}

/** 整改待办状态 */
export type GovernanceTodoStatus = "open" | "closed"

/** 整改待办 */
export interface GovernanceTodo {
  id: string
  rule_id: string
  /** 冗余的规则名称，便于列表直接展示 */
  rule_name?: string
  ci_id?: string
  title: string
  status: GovernanceTodoStatus
  created_at: string
  closed_at?: string | null
}

export interface ListGovernanceTodosParams {
  status?: GovernanceTodoStatus
  page?: number
  page_size?: number
}

export function listGovernanceTodos(
  params: ListGovernanceTodosParams = {}
): Promise<Paged<GovernanceTodo>> {
  return request<Paged<GovernanceTodo>>("/v1/governance/todos", {
    query: { ...params },
  })
}

export function closeGovernanceTodo(todoId: string): Promise<GovernanceTodo> {
  return request<GovernanceTodo>(
    `/v1/governance/todos/${encodeURIComponent(todoId)}/close`,
    { method: "POST" }
  )
}

// ---------- CI 生命周期（F-026：状态流转 + 退役三方会签 + 联动） ----------

/** 状态流转：返回更新后的 CI */
export function transitionCILifecycle(
  ciId: string,
  to: string
): Promise<CI> {
  return request<CI>(`/v1/cis/${encodeURIComponent(ciId)}/lifecycle`, {
    method: "POST",
    body: { to },
  })
}

/** 待退役候选项：三方会签（心跳停更 + 扫描不存活 + 云无实例）全满足才可退役 */
export interface RetireCandidate {
  ci: CI
  /** 心跳已停更（超过阈值未上报） */
  heartbeat_ok: boolean
  /** 扫描连续不存活 */
  scan_ok: boolean
  /** 云 / vCenter 已无实例 */
  cloud_ok: boolean
  /** 三方会签全部满足，允许执行退役 */
  eligible: boolean
}

export interface RetireCandidatesResponse {
  items: RetireCandidate[]
}

export function listRetireCandidates(): Promise<RetireCandidatesResponse> {
  return request<RetireCandidatesResponse>("/v1/lifecycle/retire-candidates")
}

/** 退役联动动作结果（n9e 摘除 target、JumpServer 禁用、IPAM 回收等） */
export interface RetireActionResult {
  type: string
  ok: boolean
  detail?: string
}

export interface RetireCIResponse {
  ci_id: string
  actions: RetireActionResult[]
}

export function retireCI(ciId: string, confirm: boolean): Promise<RetireCIResponse> {
  return request<RetireCIResponse>("/v1/lifecycle/retire", {
    method: "POST",
    body: { ci_id: ciId, confirm },
  })
}

// ---------- 审计日志（F-004：按 CI/操作者/来源过滤回放） ----------

/** 审计日志条目 */
export interface AuditLogItem {
  id: string
  ci_id?: string
  /** 动作（create/update/delete/lifecycle/confirm 等） */
  action: string
  /** 操作者（用户名或采集器标识） */
  operator?: string
  /** 来源（web/console/collector 等） */
  source: string
  /** 变更内容（字段级前后值，结构随动作而变） */
  changes?: Record<string, unknown> | null
  created_at: string
}

export interface ListAuditLogsParams {
  ci_id?: string
  operator?: string
  source?: string
  /** 时间范围（ISO 时间） */
  from?: string
  to?: string
  page?: number
  page_size?: number
}

export function listAuditLogs(
  params: ListAuditLogsParams = {}
): Promise<Paged<AuditLogItem>> {
  return request<Paged<AuditLogItem>>("/v1/audit", { query: { ...params } })
}

// ---------- 网络拓扑（F-061：LLDP/CDP 邻居链路图 + 主机接入定位） ----------

/** 拓扑节点（网络设备），room 用于按机房分组着色 */
export interface TopologyNode {
  id: string
  name: string
  model_code: string
  room?: string
}

/** 拓扑链路（设备 A 端口 ↔ 设备 B 端口），source 区分 auto（协议证据）/manual（手工） */
export interface TopologyEdge {
  a: string
  b: string
  a_port?: string
  b_port?: string
  source?: string
}

export interface TopologyGraph {
  nodes: TopologyNode[]
  edges: TopologyEdge[]
}

export function getTopology(): Promise<TopologyGraph> {
  return request<TopologyGraph>("/v1/topology")
}

/** 主机接入定位结果（ARP IP→MAC + MAC→端口交叉） */
export interface HostLocation {
  ip: string
  mac?: string
  switch?: string
  port?: string
  protocol?: string
}

/** 按 IP 定位主机接入的交换机端口；无命中时后端返回 404 */
export function getHostLocation(ip: string): Promise<HostLocation> {
  return request<HostLocation>("/v1/topology/host-location", { query: { ip } })
}

// ---------- K8s Pod 实况（F-024：直查 apiserver，不落库） ----------

export interface K8sPod {
  name: string
  namespace: string
  phase: string
  node?: string
  restart_count: number
  age_seconds: number
}

export interface ListK8sPodsParams {
  cluster: string
  namespace: string
  /** label selector，如 app=<name> */
  selector?: string
}

export function listK8sPods(params: ListK8sPodsParams): Promise<K8sPod[]> {
  return request<K8sPod[]>("/v1/k8s/pods", { query: { ...params } })
}
