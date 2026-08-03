"use client"

// 主机详情：属性分组卡片（Core/Capability/Context）+ 关系面板 + 审计时间线 + 生命周期状态流转

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import {
  ArrowLeft as ArrowLeftIcon,
  ArrowRight as ArrowRightIcon,
  ArrowLeft as IncomingIcon,
  Clock3 as Clock3Icon,
} from "lucide-react"

import { N9EPanel } from "@/components/n9e-panel"
import { RelationGraph } from "@/components/relation-graph"
import { Badge } from "@/components/ui/badge"
import { Button } from "@workspace/ui/components/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ApiError,
  getCI,
  getModel,
  listAuditLogs,
  listCIRelations,
  transitionCILifecycle,
  type AttributeDefinition,
  type AuditLogItem,
  type CI,
  type CIRelation,
  type CIStatus,
  type Model,
} from "@/lib/api"
import { auditActionLabel, CI_STATUS_LABELS } from "@/lib/labels"
import { attrText, formatDateTime, pickAttr } from "@/lib/format"

const STATUS_VARIANTS: Record<CIStatus, "success" | "secondary" | "outline"> = {
  active: "success",
  discovered: "secondary",
  retired: "outline",
}

/** 生命周期状态流转的本地兜底映射（后端未下发 lifecycle 字段时使用） */
const LIFECYCLE_TRANSITIONS: Record<CIStatus, CIStatus[]> = {
  discovered: ["active", "retired"],
  active: ["retired"],
  retired: [],
}

/** 从 CI 的 lifecycle 字段提取合法目标态；字段缺失或形状不符时回退本地映射表 */
function allowedTransitions(ci: CI): string[] {
  const raw = (ci as unknown as { lifecycle?: unknown }).lifecycle
  if (Array.isArray(raw)) {
    return raw.filter((v): v is string => typeof v === "string")
  }
  if (typeof raw === "object" && raw !== null) {
    for (const key of ["allowed", "allowed_transitions", "targets", "to"]) {
      const value = (raw as Record<string, unknown>)[key]
      if (Array.isArray(value)) {
        return value.filter((v): v is string => typeof v === "string")
      }
    }
  }
  return LIFECYCLE_TRANSITIONS[ci.status]
}

/** 对端 CI 的展示名候选属性编码 */
const PEER_NAME_CODES = ["hostname", "ident", "name", "ip"]

type GroupKey = "core" | "capability" | "context"

const GROUP_META: { key: GroupKey; title: string; description: string }[] = [
  { key: "core", title: "Core 核心属性", description: "身份类属性：标识、寻址与建档信息" },
  { key: "capability", title: "Capability 能力属性", description: "场景类属性：运行状态、规格与归属信息" },
  { key: "context", title: "Context 上下文属性", description: "补充类属性：采集附带的可选信息" },
]

interface DisplayAttr {
  code: string
  label: string
  value: string
}

/**
 * 属性分组规则：
 * 1. 模型定义带 group 字段（契约外可选扩展，core/capability/context）时按定义归组；
 * 2. 否则按来源兜底：人工登记（source 为空或 manual）归 Core，采集器上报归 Capability；
 * 3. CI 上存在但模型未定义的属性一律归 Context。
 */
function groupOf(def: AttributeDefinition): GroupKey {
  const group = def.group?.trim().toLowerCase()
  if (group === "core" || group === "capability" || group === "context") return group
  const source = def.source?.trim().toLowerCase()
  if (!source || source === "manual" || source === "人工") return "core"
  return "capability"
}

export function HostDetail({ id }: { id: string }) {
  const [ci, setCI] = useState<CI | null>(null)
  const [model, setModel] = useState<Model | null>(null)
  const [relations, setRelations] = useState<CIRelation[]>([])
  const [audits, setAudits] = useState<AuditLogItem[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [transitioning, setTransitioning] = useState(false)
  const [transitionError, setTransitionError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const ciData = await getCI(id)
      setCI(ciData)
      // 模型定义、关系列表与审计时间线并行加载；三者失败不阻塞 CI 本体展示
      const [modelResult, relationsResult, auditsResult] = await Promise.allSettled([
        getModel(ciData.model_id),
        listCIRelations(id),
        listAuditLogs({ ci_id: id, page: 1, page_size: 20 }),
      ])
      setModel(modelResult.status === "fulfilled" ? modelResult.value : null)
      setRelations(relationsResult.status === "fulfilled" ? relationsResult.value.items : [])
      setAudits(auditsResult.status === "fulfilled" ? auditsResult.value.items : null)
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err.status === 404
            ? "主机不存在或已被删除"
            : err.message
          : "加载主机详情失败",
      )
    } finally {
      setLoading(false)
    }
  }, [id])

  useEffect(() => {
    void load()
  }, [load])

  /** 生命周期状态流转 */
  const onTransition = useCallback(
    async (to: string) => {
      if (!to || transitioning) return
      setTransitioning(true)
      setTransitionError(null)
      try {
        const updated = await transitionCILifecycle(id, to)
        setCI((prev) => (prev ? { ...prev, ...updated } : updated))
      } catch (err) {
        setTransitionError(err instanceof ApiError ? err.message : "状态流转失败")
      } finally {
        setTransitioning(false)
      }
    },
    [id, transitioning],
  )

  const groups = useMemo(() => {
    const result: Record<GroupKey, DisplayAttr[]> = { core: [], capability: [], context: [] }
    if (!ci) return result
    const defs = model?.attributes ?? []
    const definedCodes = new Set(defs.map((def) => def.code))
    // 先按模型定义顺序输出
    for (const def of defs) {
      result[groupOf(def)].push({
        code: def.code,
        label: `${def.name}（${def.code}）`,
        value: attrText(ci.attributes[def.code]),
      })
    }
    // 模型未定义的属性兜底进 Context
    for (const [code, value] of Object.entries(ci.attributes)) {
      if (!definedCodes.has(code)) {
        result.context.push({ code, label: code, value: attrText(value) })
      }
    }
    return result
  }, [ci, model])

  const relationNames = useMemo(() => {
    const map = new Map<string, string>()
    for (const rel of model?.relations ?? []) map.set(rel.code, rel.name)
    return map
  }, [model])

  if (loading) {
    return (
      <div className="flex w-full flex-col gap-5 p-6">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
    )
  }

  if (error || !ci) {
    return (
      <div className="flex w-full flex-col gap-5 p-6">
        <Link
          href="/hosts"
          className="flex w-fit items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground"
        >
          <ArrowLeftIcon className="size-4" /> 返回主机列表
        </Link>
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-16">
          <p className="text-xs text-destructive">{error ?? "加载主机详情失败"}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      </div>
    )
  }

  const title = pickAttr(ci.attributes, PEER_NAME_CODES)
  // n9e 面板标识：取 CI attributes.ident，缺失时面板自身降级为未配置占位
  const ident = attrText(ci.attributes.ident)
  const transitions = allowedTransitions(ci)

  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <Link
        href="/hosts"
        className="flex w-fit items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground"
      >
        <ArrowLeftIcon className="size-4" /> 返回主机列表
      </Link>

      <header className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="text-xl font-semibold">{title === "—" ? ci.id : title}</h1>
          <Badge variant={STATUS_VARIANTS[ci.status]}>{CI_STATUS_LABELS[ci.status]}</Badge>
          <Badge variant="outline">来源：{ci.source}</Badge>
          {model ? <Badge variant="secondary">模型：{model.name}</Badge> : null}
          {transitions.length > 0 && (
            <Select
              value=""
              onValueChange={(v) => {
                if (v) void onTransition(v)
              }}
              disabled={transitioning}
            >
              <SelectTrigger className="h-8 w-36">
                <SelectValue>
                  {() => (transitioning ? "流转中…" : "状态流转")}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {transitions.map((to) => (
                  <SelectItem key={to} value={to}>
                    {CI_STATUS_LABELS[to as CIStatus] ?? to}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          {transitionError && (
            <span className="text-xs text-destructive">{transitionError}</span>
          )}
        </div>
        <p className="text-xs text-muted-foreground">
          创建于 {formatDateTime(ci.created_at)} · 更新于 {formatDateTime(ci.updated_at)}
        </p>
      </header>

      {/* 属性分组卡片 */}
      {GROUP_META.map((meta) => (
        <Card key={meta.key}>
          <CardHeader>
            <CardTitle>{meta.title}</CardTitle>
            <CardDescription>{meta.description}</CardDescription>
          </CardHeader>
          <CardContent>
            {groups[meta.key].length === 0 ? (
              <p className="text-xs text-muted-foreground">暂无属性</p>
            ) : (
              <dl className="grid grid-cols-1 gap-x-8 gap-y-2.5 sm:grid-cols-2">
                {groups[meta.key].map((attr) => (
                  <div key={attr.code} className="flex items-baseline justify-between gap-4 text-xs">
                    <dt className="shrink-0 text-muted-foreground">{attr.label}</dt>
                    <dd className="min-w-0 text-right break-all">{attr.value}</dd>
                  </div>
                ))}
              </dl>
            )}
          </CardContent>
        </Card>
      ))}

      {/* n9e 监控嵌入面板（监控视图 + 当前告警） */}
      <N9EPanel ident={ident === "—" ? "" : ident} />

      <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
        {/* 关系面板 */}
        <Card>
          <CardHeader>
            <CardTitle>关系（{relations.length}）</CardTitle>
            <CardDescription>当前 CI 与其他 CI 的关联，出向为当前 CI 指向对端</CardDescription>
          </CardHeader>
          <CardContent>
            {relations.length === 0 ? (
              <p className="text-xs text-muted-foreground">暂无关系</p>
            ) : (
              <div className="flex flex-col gap-3">
                {/* F-021：一跳局部拓扑，边按关系类型着色带方向，点击对端跳详情 */}
                <RelationGraph
                  ci={ci}
                  relations={relations}
                  relationNames={relationNames}
                  hrefForPeer={(rel) => `/hosts/${rel.peer_ci.id}`}
                />
                {/* 原关系列表保留为折叠明细 */}
                <details className="group">
                  <summary className="cursor-pointer text-xs text-muted-foreground select-none hover:text-foreground">
                    关系明细列表（点击展开）
                  </summary>
                  <ul className="mt-2 flex flex-col gap-2">
                    {relations.map((rel, index) => {
                      const peerName = pickAttr(rel.peer_ci.attributes, PEER_NAME_CODES)
                      return (
                        <li
                          key={`${rel.relation_code}-${rel.peer_ci.id}-${index}`}
                          className="flex items-center justify-between gap-3 rounded-lg border px-3 py-2 text-xs"
                        >
                          <span className="flex items-center gap-2">
                            <Badge variant="secondary">
                              {relationNames.get(rel.relation_code) ?? rel.relation_code}
                            </Badge>
                            {rel.direction === "outgoing" ? (
                              <span className="flex items-center gap-1 text-muted-foreground">
                                <ArrowRightIcon className="size-3.5" /> 出向
                              </span>
                            ) : (
                              <span className="flex items-center gap-1 text-muted-foreground">
                                <IncomingIcon className="size-3.5" /> 入向
                              </span>
                            )}
                          </span>
                          <Link
                            href={`/hosts/${rel.peer_ci.id}`}
                            className="min-w-0 truncate font-medium text-primary hover:underline"
                          >
                            {peerName === "—" ? rel.peer_ci.id : peerName}
                          </Link>
                        </li>
                      )
                    })}
                  </ul>
                </details>
              </div>
            )}
          </CardContent>
        </Card>

        {/* 审计时间线（F-004）：回放该 CI 最近 20 条写操作与调和历史 */}
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Clock3Icon className="size-4" /> 审计时间线
            </CardTitle>
            <CardDescription>
              该 CI 的属性变更、状态流转与来源记录；完整回放见
              <Link href={`/audit`} className="ml-1 text-primary underline-offset-2 hover:underline">
                审计日志
              </Link>
            </CardDescription>
          </CardHeader>
          <CardContent>
            {audits === null ? (
              <div className="flex flex-col gap-2">
                {Array.from({ length: 3 }).map((_, i) => (
                  <Skeleton key={i} className="h-8 w-full" />
                ))}
              </div>
            ) : audits.length === 0 ? (
              <p className="text-xs text-muted-foreground">暂无审计记录</p>
            ) : (
              <ul className="flex flex-col gap-2">
                {audits.map((item) => {
                  const changes =
                    item.changes && Object.keys(item.changes).length > 0
                      ? JSON.stringify(item.changes, null, 2)
                      : ""
                  const summary = changes
                    ? JSON.stringify(item.changes)
                    : ""
                  return (
                    <li
                      key={item.id}
                      className="flex flex-col gap-1 rounded-lg border px-3 py-2 text-xs"
                    >
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge variant="secondary">{auditActionLabel(item.action)}</Badge>
                        <span className="text-muted-foreground">
                          {item.operator ?? "系统"} · {item.source}
                        </span>
                        <span className="ml-auto text-muted-foreground">
                          {formatDateTime(item.created_at)}
                        </span>
                      </div>
                      {summary && (
                        <p
                          className="truncate font-mono text-xs text-muted-foreground"
                          title={changes}
                        >
                          {summary.length > 120 ? `${summary.slice(0, 120)}…` : summary}
                        </p>
                      )}
                    </li>
                  )
                })}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
