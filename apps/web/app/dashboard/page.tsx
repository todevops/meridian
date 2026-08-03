"use client"

// 运营仪表盘（F-080）：数据质量五指标看板。
// 按模型切换查看：属性完整率 / 关联完整率 / 孤岛 CI / 数据鲜度 / 监控覆盖率（双指标并排）；
// 每张卡可下钻打开抽屉，展示该模型对应指标的缺失 CI 清单（标准 CI 分页）。

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { Gauge as GaugeIcon } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@workspace/ui/components/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerHeader,
  DrawerTitle,
} from "@/components/ui/drawer"
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
  getQualityDashboard,
  getQualityDrilldown,
  type CI,
  type ModelQualityMetric,
  type Paged,
  type QualityMetricKey,
} from "@/lib/api"
import { attrText, pickAttr } from "@/lib/format"

const DRILLDOWN_PAGE_SIZE = 10

/** CI 展示名候选属性编码 */
const CI_NAME_CODES = ["hostname", "ident", "name", "ip"]

/** 百分比字段兼容 0-1 小数与 0-100 百分数两种口径 */
function toPct(value: number | undefined): number | null {
  if (value === undefined || value === null || Number.isNaN(value)) return null
  return value <= 1 ? value * 100 : value
}

function pctText(value: number | undefined): string {
  const pct = toPct(value)
  return pct === null ? "—" : `${pct.toFixed(1)}%`
}

interface MetricDef {
  key: QualityMetricKey
  label: string
  description: string
  /** 取卡片主数值（百分数口径，越高越好；孤岛为计数返回 null） */
  valueOf: (m: ModelQualityMetric) => number | null
  /** 计数型指标（孤岛 CI）直接展示数量 */
  countOf?: (m: ModelQualityMetric) => number
  /** 下钻不可用时的提示（如该模型无监控覆盖指标） */
  unavailable?: (m: ModelQualityMetric) => boolean
}

const METRICS: MetricDef[] = [
  {
    key: "completeness",
    label: "属性完整率",
    description: "必填与关键属性的填写完整程度",
    valueOf: (m) => toPct(m.completeness),
  },
  {
    key: "relation_completeness",
    label: "关联完整率",
    description: "模型定义关系的建立完整程度",
    valueOf: (m) => toPct(m.relation_completeness),
  },
  {
    key: "orphan",
    label: "孤岛 CI",
    description: "与其他 CI 无任何关系的实例数量",
    valueOf: () => null,
    countOf: (m) => m.orphan_count,
  },
  {
    key: "stale",
    label: "数据鲜度",
    description: "属性在鲜度阈值内有更新的 CI 占比",
    valueOf: (m) => {
      const stale = toPct(m.stale_pct)
      return stale === null ? null : 100 - stale
    },
  },
  {
    key: "no_heartbeat",
    label: "监控覆盖率",
    description: "在用主机已接入 n9e 监控心跳的占比",
    valueOf: (m) => {
      const missing = toPct(m.no_heartbeat_pct)
      return missing === null ? null : 100 - missing
    },
    unavailable: (m) => m.no_heartbeat_pct === undefined,
  },
]

export default function DashboardPage() {
  const [models, setModels] = useState<ModelQualityMetric[] | null>(null)
  const [monitor, setMonitor] = useState<{ no_heartbeat_pct: number; no_ci_pct: number } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [modelId, setModelId] = useState<string>("")

  // 下钻抽屉状态
  const [drillMetric, setDrillMetric] = useState<QualityMetricKey | null>(null)
  const [drillPage, setDrillPage] = useState(1)
  const [drillData, setDrillData] = useState<Paged<CI> | null>(null)
  const [drillError, setDrillError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      const res = await getQualityDashboard()
      setModels(res.models)
      setMonitor(res.monitor)
      setModelId((prev) =>
        prev && res.models.some((m) => m.model_id === prev)
          ? prev
          : (res.models[0]?.model_id ?? "")
      )
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "加载质量看板失败")
      setModels([])
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const current = useMemo(
    () => models?.find((m) => m.model_id === modelId) ?? null,
    [models, modelId]
  )

  // 下钻清单加载
  useEffect(() => {
    if (!drillMetric || !modelId) return
    let cancelled = false
    setDrillData(null)
    setDrillError(null)
    getQualityDrilldown({
      model_id: modelId,
      metric: drillMetric,
      page: drillPage,
      page_size: DRILLDOWN_PAGE_SIZE,
    })
      .then((res) => {
        if (!cancelled) setDrillData(res)
      })
      .catch((e) => {
        if (!cancelled)
          setDrillError(e instanceof ApiError ? e.message : "加载下钻清单失败")
      })
    return () => {
      cancelled = true
    }
  }, [drillMetric, modelId, drillPage])

  const openDrilldown = (metric: QualityMetricKey) => {
    setDrillPage(1)
    setDrillMetric(metric)
  }

  const drillTotalPages = drillData
    ? Math.max(1, Math.ceil(drillData.total / DRILLDOWN_PAGE_SIZE))
    : 1

  const drillMetricDef = METRICS.find((m) => m.key === drillMetric)

  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">运营仪表盘</h1>
          <p className="mt-1 text-xs text-muted-foreground">
            数据质量五指标看板，按模型查看并可下钻缺失清单
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={() => void load()}>
          刷新
        </Button>
      </header>

      {error && (
        <Card className="border-destructive/50">
          <CardContent className="flex items-center justify-between gap-4 py-3 text-xs">
            <span className="text-destructive">{error}</span>
            <Button variant="outline" size="sm" onClick={() => void load()}>
              重试
            </Button>
          </CardContent>
        </Card>
      )}

      {models === null ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="h-36 w-full" />
          ))}
        </div>
      ) : models.length === 0 ? (
        <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed py-16">
          <GaugeIcon className="size-8 text-muted-foreground" />
          <p className="text-xs text-muted-foreground">
            暂无质量指标数据，等待指标引擎产出
          </p>
        </div>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-3">
            <span className="text-xs text-muted-foreground">按模型查看</span>
            <Select value={modelId} onValueChange={(v) => v && setModelId(v)}>
              <SelectTrigger className="w-56">
                <SelectValue>
                  {(v: string) => {
                    const m = models.find((item) => item.model_id === v)
                    return m ? `${m.name}（${m.code}）` : "选择模型"
                  }}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {models.map((m) => (
                  <SelectItem key={m.model_id} value={m.model_id}>
                    {m.name}（{m.code}）
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {current && (
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {METRICS.map((metric) => {
                const value = metric.valueOf(current)
                const unavailable = metric.unavailable?.(current) ?? false
                return (
                  <Card key={metric.key}>
                    <CardHeader className="pb-2">
                      <CardTitle className="flex items-center justify-between text-sm">
                        {metric.label}
                        {metric.key === "no_heartbeat" && unavailable ? (
                          <Badge variant="secondary">不适用</Badge>
                        ) : null}
                      </CardTitle>
                      <CardDescription>{metric.description}</CardDescription>
                    </CardHeader>
                    <CardContent className="flex flex-col gap-3">
                      {metric.countOf ? (
                        <p className="text-2xl font-semibold tabular-nums">
                          {metric.countOf(current)}
                          <span className="ml-1 text-xs font-normal text-muted-foreground">
                            个
                          </span>
                        </p>
                      ) : unavailable ? (
                        <p className="text-2xl font-semibold text-muted-foreground">
                          —
                        </p>
                      ) : (
                        <>
                          <p className="text-2xl font-semibold tabular-nums">
                            {value === null ? "—" : `${value.toFixed(1)}%`}
                          </p>
                          <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
                            <div
                              className="h-full rounded-full bg-primary transition-all"
                              style={{
                                width: `${Math.min(100, Math.max(0, value ?? 0))}%`,
                              }}
                            />
                          </div>
                        </>
                      )}

                      {/* 监控覆盖率卡附带全局双指标（CMDB 无心跳 / n9e 无 CI） */}
                      {metric.key === "no_heartbeat" && monitor && (
                        <div className="grid grid-cols-2 gap-2 rounded-lg border p-2">
                          <div className="flex flex-col">
                            <span className="text-xs text-muted-foreground">
                              CMDB 无心跳
                            </span>
                            <span className="text-sm font-medium tabular-nums">
                              {pctText(monitor.no_heartbeat_pct)}
                            </span>
                          </div>
                          <div className="flex flex-col">
                            <span className="text-xs text-muted-foreground">
                              n9e 无 CI
                            </span>
                            <span className="text-sm font-medium tabular-nums">
                              {pctText(monitor.no_ci_pct)}
                            </span>
                          </div>
                        </div>
                      )}

                      <Button
                        variant="outline"
                        size="sm"
                        className="w-fit"
                        disabled={unavailable}
                        onClick={() => openDrilldown(metric.key)}
                      >
                        下钻缺失清单
                      </Button>
                    </CardContent>
                  </Card>
                )
              })}
            </div>
          )}
        </>
      )}

      {/* 下钻抽屉：缺失 CI 清单 */}
      <Drawer
        open={drillMetric !== null}
        onOpenChange={(open) => {
          if (!open) setDrillMetric(null)
        }}
      >
        <DrawerContent>
          <DrawerHeader>
            <DrawerTitle>
              {current ? `${current.name}（${current.code}）` : ""}
              {drillMetricDef ? ` · ${drillMetricDef.label}` : ""}下钻
            </DrawerTitle>
            <DrawerDescription>
              该指标下的缺失 / 异常 CI 清单，点击跳转 CI 详情处理
            </DrawerDescription>
          </DrawerHeader>

          {drillError ? (
            <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
              <p className="text-xs text-destructive">{drillError}</p>
            </div>
          ) : drillData === null ? (
            <div className="flex flex-col gap-2">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : drillData.items.length === 0 ? (
            <p className="py-12 text-center text-xs text-muted-foreground">
              没有缺失项，该指标数据完好
            </p>
          ) : (
            <ul className="flex flex-col gap-2">
              {drillData.items.map((ci) => {
                const name = pickAttr(ci.attributes, CI_NAME_CODES)
                return (
                  <li
                    key={ci.id}
                    className="flex items-center justify-between gap-3 rounded-lg border px-3 py-2 text-xs"
                  >
                    <div className="flex min-w-0 flex-col">
                      <span className="truncate font-medium">
                        {name === "—" ? ci.id : name}
                      </span>
                      <span className="truncate text-muted-foreground">
                        {attrText(ci.attributes.ip) !== "—"
                          ? attrText(ci.attributes.ip)
                          : ci.id}
                      </span>
                    </div>
                    <Link
                      href={`/hosts/${ci.id}`}
                      className="shrink-0 text-primary underline-offset-2 hover:underline"
                    >
                      查看 CI
                    </Link>
                  </li>
                )
              })}
            </ul>
          )}

          {drillData && drillData.total > 0 && (
            <div className="mt-auto flex items-center justify-between border-t pt-3 text-xs text-muted-foreground">
              <span>共 {drillData.total} 条</span>
              <div className="flex items-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={drillPage <= 1}
                  onClick={() => setDrillPage((p) => p - 1)}
                >
                  上一页
                </Button>
                <span>
                  第 {drillPage} / {drillTotalPages} 页
                </span>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={drillPage >= drillTotalPages}
                  onClick={() => setDrillPage((p) => p + 1)}
                >
                  下一页
                </Button>
              </div>
            </div>
          )}
        </DrawerContent>
      </Drawer>
    </div>
  )
}
