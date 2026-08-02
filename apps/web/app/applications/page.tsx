"use client"

// 应用归属页（F-028）：按业务线分组展示应用系统，卡片标注负责人与部署主机数。
// 归属数据来自 CI 关系：biz_app -belongs_to-> biz_line、biz_app -deployed_on-> host。
// 顶部「未归属主机」为前端估算：host 总数 - 有 deployed_on 入向关系的主机数。

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  AppWindow as AppWindowIcon,
  TriangleAlert as TriangleAlertIcon,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@workspace/ui/components/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { CIDetailDrawer } from "@/components/ci-detail-drawer"
import {
  listCIRelations,
  type CI,
  type CIRelation,
} from "@/lib/api"
import { listAllCIs, resolveModelId } from "@/lib/cis"
import { attrText, pickAttr } from "@/lib/format"

/** 未归属业务线的应用所在分组 key */
const UNGROUPED = "__ungrouped__"

/** 等级徽标样式：业务线 critical/high/normal 与应用 L1/L2/L3 共用三档 */
const LEVEL_STYLES: Record<string, { className: string }> = {
  critical: { className: "bg-red-500/15 text-red-700 dark:text-red-400" },
  l1: { className: "bg-red-500/15 text-red-700 dark:text-red-400" },
  high: { className: "bg-amber-500/15 text-amber-700 dark:text-amber-400" },
  l2: { className: "bg-amber-500/15 text-amber-700 dark:text-amber-400" },
  normal: { className: "bg-muted text-muted-foreground" },
  l3: { className: "bg-muted text-muted-foreground" },
}

function LevelBadge({ level }: { level: string }) {
  if (level === "—") return <Badge variant="outline">未评级</Badge>
  const style = LEVEL_STYLES[level.toLowerCase()] ?? {
    className: "bg-muted text-muted-foreground",
  }
  return <Badge className={style.className}>{level}</Badge>
}

/** 出向 deployed_on 关系（部署主机清单） */
function deployedHosts(relations: CIRelation[]): CIRelation[] {
  return relations.filter(
    (rel) => rel.relation_code === "deployed_on" && rel.direction === "outgoing",
  )
}

/** 出向 belongs_to 关系对端（所属业务线 id），无则 null */
function ownerLineId(relations: CIRelation[]): string | null {
  const rel = relations.find(
    (item) => item.relation_code === "belongs_to" && item.direction === "outgoing",
  )
  return rel ? rel.peer_ci.id : null
}

/** 应用详情抽屉中部署主机跳转主机详情页，其余关系不跳转 */
function appPeerHref(rel: CIRelation): string | null {
  if (rel.relation_code === "deployed_on") return `/hosts/${rel.peer_ci.id}`
  return null
}

export default function ApplicationsPage() {
  const [lines, setLines] = useState<CI[]>([])
  const [apps, setApps] = useState<CI[]>([])
  /** 应用 id -> 关系列表（部署主机数与归属分组均由此推导） */
  const [relationsMap, setRelationsMap] = useState<Map<string, CIRelation[]>>(
    new Map(),
  )
  const [hostTotal, setHostTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<CI | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [lineModelId, appModelId, hostModelId] = await Promise.all([
        resolveModelId("biz_line"),
        resolveModelId("biz_app"),
        resolveModelId("host"),
      ])
      const [lineCIs, appCIs, hostCIs] = await Promise.all([
        listAllCIs({ model_id: lineModelId }),
        listAllCIs({ model_id: appModelId }),
        listAllCIs({ model_id: hostModelId }),
      ])
      // 逐应用拉关系；单个应用失败不阻塞整页（按无关系兜底）
      const entries = await Promise.all(
        appCIs.map(async (app) => {
          try {
            const res = await listCIRelations(app.id)
            return [app.id, res.items] as const
          } catch {
            return [app.id, [] as CIRelation[]] as const
          }
        }),
      )
      setLines(lineCIs)
      setApps(appCIs)
      setHostTotal(hostCIs.length)
      setRelationsMap(new Map(entries))
    } catch {
      setError("加载应用归属数据失败")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  /** 业务线分组：lineId（或 UNGROUPED）-> 应用列表 */
  const groups = useMemo(() => {
    const map = new Map<string, CI[]>()
    for (const line of lines) map.set(line.id, [])
    map.set(UNGROUPED, [])
    for (const app of apps) {
      const lineId = ownerLineId(relationsMap.get(app.id) ?? [])
      const key = lineId && map.has(lineId) ? lineId : UNGROUPED
      map.get(key)!.push(app)
    }
    return map
  }, [lines, apps, relationsMap])

  /** 已归属主机集合：被任一应用 deployed_on 指向的主机 id */
  const attachedHostIds = useMemo(() => {
    const set = new Set<string>()
    for (const relations of relationsMap.values()) {
      for (const rel of deployedHosts(relations)) set.add(rel.peer_ci.id)
    }
    return set
  }, [relationsMap])

  const stats = [
    { label: "业务线", value: lines.length },
    { label: "应用", value: apps.length },
    { label: "已归属主机", value: attachedHostIds.size },
    { label: "未归属主机", value: Math.max(0, hostTotal - attachedHostIds.size) },
  ]

  /** 选中应用的未归属提示（无业务线归属 / 无部署主机） */
  const selectedHints = useMemo(() => {
    if (!selected) return []
    const relations = relationsMap.get(selected.id) ?? []
    const hints: string[] = []
    if (!ownerLineId(relations)) hints.push("该应用未归属任何业务线")
    if (deployedHosts(relations).length === 0) hints.push("该应用未挂载部署主机")
    return hints
  }, [selected, relationsMap])

  const renderAppCard = (app: CI) => {
    const relations = relationsMap.get(app.id) ?? []
    const hostCount = deployedHosts(relations).length
    return (
      <button
        key={app.id}
        type="button"
        onClick={() => setSelected(app)}
        className="flex flex-col gap-2 rounded-lg border p-3 text-left transition-colors hover:border-primary/50 hover:bg-muted/40"
      >
        <span className="flex items-center justify-between gap-2">
          <span className="flex min-w-0 items-center gap-2">
            <AppWindowIcon className="size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 truncate text-sm font-medium">
              {pickAttr(app.attributes, ["name", "code"])}
            </span>
          </span>
          <LevelBadge level={attrText(app.attributes.level)} />
        </span>
        <span className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
          <span>负责人：{attrText(app.attributes.owner)}</span>
          <span>部署主机：{hostCount} 台</span>
        </span>
      </button>
    )
  }

  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <header>
        <h1 className="text-xl font-semibold">应用归属</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          按业务线分组的应用系统清单与部署主机归属，点击应用卡片查看详情
        </p>
      </header>

      {/* 顶部统计 */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        {stats.map((stat) => (
          <Card key={stat.label}>
            <CardContent className="flex flex-col gap-1 py-4">
              <span className="text-2xl font-semibold">
                {loading ? "—" : stat.value}
              </span>
              <span className="text-xs text-muted-foreground">{stat.label}</span>
            </CardContent>
          </Card>
        ))}
      </div>

      {loading ? (
        <div className="flex flex-col gap-2">
          {Array.from({ length: 4 }).map((_, index) => (
            <Skeleton key={index} className="h-28 w-full" />
          ))}
        </div>
      ) : error ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
          <p className="text-xs text-destructive">{error}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      ) : apps.length === 0 && lines.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
          <p className="text-xs text-muted-foreground">
            暂无业务线与应用数据，请先在模型管理中登记 biz_line / biz_app 实例
          </p>
        </div>
      ) : (
        <>
          {/* 业务线分组 */}
          {lines.map((line) => {
            const lineApps = groups.get(line.id) ?? []
            return (
              <Card key={line.id}>
                <CardHeader>
                  <CardTitle className="flex flex-wrap items-center gap-2 text-base">
                    {pickAttr(line.attributes, ["name", "code"])}
                    <Badge variant="outline">
                      编码：{attrText(line.attributes.code)}
                    </Badge>
                    <LevelBadge level={attrText(line.attributes.level)} />
                  </CardTitle>
                  <CardDescription>
                    负责人：{attrText(line.attributes.owner)} · 应用{" "}
                    {lineApps.length} 个
                  </CardDescription>
                </CardHeader>
                <CardContent>
                  {lineApps.length === 0 ? (
                    <p className="text-xs text-muted-foreground">
                      该业务线下暂无应用
                    </p>
                  ) : (
                    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                      {lineApps.map(renderAppCard)}
                    </div>
                  )}
                </CardContent>
              </Card>
            )
          })}

          {/* 未归属业务线的应用 */}
          {(groups.get(UNGROUPED) ?? []).length > 0 ? (
            <Card className="border-dashed">
              <CardHeader>
                <CardTitle className="text-base">未归属业务线</CardTitle>
                <CardDescription>
                  以下应用缺少 belongs_to 关系，待治理归位
                </CardDescription>
              </CardHeader>
              <CardContent>
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                  {(groups.get(UNGROUPED) ?? []).map(renderAppCard)}
                </div>
              </CardContent>
            </Card>
          ) : null}
        </>
      )}

      <CIDetailDrawer
        ci={selected}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        hrefForPeer={appPeerHref}
        extra={
          selected && selectedHints.length > 0 ? (
            <Card className="border-dashed border-amber-500/50">
              <CardContent className="flex flex-col gap-2 py-3">
                {selectedHints.map((hint) => (
                  <p
                    key={hint}
                    className="flex items-center gap-2 text-xs text-amber-700 dark:text-amber-400"
                  >
                    <TriangleAlertIcon className="size-3.5 shrink-0" /> {hint}
                  </p>
                ))}
              </CardContent>
            </Card>
          ) : null
        }
      />
    </div>
  )
}
