"use client"

// 应用系统聚合页（F-027，SRE 主入口）：左侧两级业务树（业务线→应用，可折叠），
// 右侧选中应用进入一屏聚合视图（五类资源分区 + 依赖拓扑）；
// 未选中时保留既有分组列表能力（按业务线分组的应用卡片总览）。
// 选中态经 ?app=<id> 查询参数承载，保证导航高亮与可分享链接。

import { Suspense, useCallback, useEffect, useMemo, useState } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { AppWindow as AppWindowIcon } from "lucide-react"

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
import { ApplicationTree } from "@/components/application-tree"
import { ApplicationAggregateView } from "@/components/application-aggregate"
import {
  getApplicationTree,
  type AppTreeApp,
  type ApplicationTree as ApplicationTreeData,
} from "@/lib/api"
import { attrText } from "@/lib/format"

/** 等级徽标样式：业务线 critical/high/normal 与应用 L1/L2/L3 共用三档 */
const LEVEL_STYLES: Record<string, string> = {
  critical: "bg-red-500/15 text-red-700 dark:text-red-400",
  l1: "bg-red-500/15 text-red-700 dark:text-red-400",
  high: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  l2: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  normal: "bg-muted text-muted-foreground",
  l3: "bg-muted text-muted-foreground",
}

function LevelBadge({ level }: { level: string }) {
  if (level === "—") return <Badge variant="outline">未评级</Badge>
  const className =
    LEVEL_STYLES[level.toLowerCase()] ?? "bg-muted text-muted-foreground"
  return <Badge className={className}>{level}</Badge>
}

function ApplicationsView() {
  const router = useRouter()
  const searchParams = useSearchParams()
  const selectedAppId = searchParams.get("app")

  const [tree, setTree] = useState<ApplicationTreeData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setTree(await getApplicationTree())
    } catch {
      setError("加载业务树失败")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const onToggle = useCallback((key: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(key)) {
        next.delete(key)
      } else {
        next.add(key)
      }
      return next
    })
  }, [])

  const onSelectApp = useCallback(
    (appId: string) => {
      router.push(`/applications?app=${encodeURIComponent(appId)}`)
    },
    [router],
  )

  const onSelectOverview = useCallback(() => {
    router.push("/applications")
  }, [router])

  /** 选中应用在树上的摘要与所属业务线名（头卡即时展示与兜底） */
  const selectedHint = useMemo(() => {
    if (!tree || !selectedAppId) return { app: null, lineName: undefined } as const
    for (const line of tree.lines) {
      const app = line.apps.find((item) => item.id === selectedAppId)
      if (app) return { app, lineName: attrText(line.name) } as const
    }
    const app = tree.unassigned.find((item) => item.id === selectedAppId)
    return { app: app ?? null, lineName: undefined } as const
  }, [tree, selectedAppId])

  /** 总览统计：业务线 / 应用 / 归属主机（各业务线汇总求和）/ 未归属应用 */
  const stats = useMemo(() => {
    const lines = tree?.lines ?? []
    const unassigned = tree?.unassigned ?? []
    const appTotal =
      lines.reduce((sum, line) => sum + line.app_count, 0) + unassigned.length
    const hostTotal = lines.reduce((sum, line) => sum + line.host_count, 0)
    return [
      { label: "业务线", value: lines.length },
      { label: "应用", value: appTotal },
      { label: "归属主机", value: hostTotal },
      { label: "未归属应用", value: unassigned.length },
    ]
  }, [tree])

  /** 总览模式的应用卡片：点击选中进入聚合视图 */
  const renderAppCard = (app: AppTreeApp) => (
    <button
      key={app.id}
      type="button"
      onClick={() => onSelectApp(app.id)}
      className="flex flex-col gap-2 rounded-lg border p-3 text-left transition-colors hover:border-primary/50 hover:bg-muted/40"
    >
      <span className="flex items-center justify-between gap-2">
        <span className="flex min-w-0 items-center gap-2">
          <AppWindowIcon className="size-4 shrink-0 text-muted-foreground" />
          <span className="min-w-0 truncate text-sm font-medium">
            {attrText(app.name) === "—" ? attrText(app.code) : attrText(app.name)}
          </span>
        </span>
        <LevelBadge level={attrText(app.level)} />
      </span>
      <span className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
        <span>编码：{attrText(app.code)}</span>
        <span>负责人：{attrText(app.owner)}</span>
      </span>
    </button>
  )

  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <header>
        <h1 className="text-xl font-semibold">应用系统</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          业务线 → 应用两级导航，选中应用查看主机、数据库、K8s、IP 与云资源的一屏聚合及依赖拓扑
        </p>
      </header>

      {loading ? (
        <div className="flex gap-5">
          <Skeleton className="h-96 w-72 shrink-0" />
          <div className="flex flex-1 flex-col gap-3">
            {Array.from({ length: 3 }).map((_, index) => (
              <Skeleton key={index} className="h-32 w-full" />
            ))}
          </div>
        </div>
      ) : error || !tree ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
          <p className="text-xs text-destructive">{error ?? "加载业务树失败"}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      ) : (
        <div className="flex items-start gap-5">
          <ApplicationTree
            lines={tree.lines}
            unassigned={tree.unassigned}
            collapsed={collapsed}
            onToggle={onToggle}
            selectedAppId={selectedAppId}
            onSelectApp={onSelectApp}
            onSelectOverview={onSelectOverview}
          />

          {selectedAppId ? (
            <ApplicationAggregateView
              appId={selectedAppId}
              appHint={selectedHint.app}
              lineName={selectedHint.lineName}
              onSelectApp={onSelectApp}
            />
          ) : (
            /* 概览模式：保留既有按业务线分组的列表能力 */
            <div className="flex min-w-0 flex-1 flex-col gap-5">
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                {stats.map((stat) => (
                  <Card key={stat.label}>
                    <CardContent className="flex flex-col gap-1 py-4">
                      <span className="text-2xl font-semibold">{stat.value}</span>
                      <span className="text-xs text-muted-foreground">
                        {stat.label}
                      </span>
                    </CardContent>
                  </Card>
                ))}
              </div>

              {tree.lines.length === 0 && tree.unassigned.length === 0 ? (
                <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
                  <p className="text-xs text-muted-foreground">
                    暂无业务线与应用数据，请先在模型管理中登记 biz_line / biz_app 实例
                  </p>
                </div>
              ) : (
                <>
                  {tree.lines.map((line) => (
                    <Card key={line.id}>
                      <CardHeader>
                        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
                          {attrText(line.name) === "—"
                            ? attrText(line.code)
                            : attrText(line.name)}
                          <Badge variant="outline">
                            编码：{attrText(line.code)}
                          </Badge>
                          <LevelBadge level={attrText(line.level)} />
                        </CardTitle>
                        <CardDescription>
                          负责人：{attrText(line.owner)} · 应用 {line.app_count} 个
                          · 主机 {line.host_count} 台
                        </CardDescription>
                      </CardHeader>
                      <CardContent>
                        {line.apps.length === 0 ? (
                          <p className="text-xs text-muted-foreground">
                            该业务线下暂无应用
                          </p>
                        ) : (
                          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                            {line.apps.map(renderAppCard)}
                          </div>
                        )}
                      </CardContent>
                    </Card>
                  ))}

                  {tree.unassigned.length > 0 ? (
                    <Card className="border-dashed">
                      <CardHeader>
                        <CardTitle className="text-base">未归属业务线</CardTitle>
                        <CardDescription>
                          以下应用缺少 belongs_to 关系，待治理归位
                        </CardDescription>
                      </CardHeader>
                      <CardContent>
                        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                          {tree.unassigned.map(renderAppCard)}
                        </div>
                      </CardContent>
                    </Card>
                  ) : null}
                </>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export default function ApplicationsPage() {
  return (
    <Suspense
      fallback={
        <div className="flex w-full flex-col gap-5 p-6">
          <Skeleton className="h-8 w-64" />
          <Skeleton className="h-96 w-full" />
        </div>
      }
    >
      <ApplicationsView />
    </Suspense>
  )
}
