"use client"

// 数据库台账：顶部集群分组统计卡（按 cluster_name 聚合）+ 实例清单；
// 实例由 DB/中间件发现器（时序库标签枚举通道）建档，runs_on 关系挂接宿主机

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  flexRender,
  getCoreRowModel,
  getFilteredRowModel,
  getPaginationRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table"
import {
  Database as DatabaseIcon,
  Download as DownloadIcon,
  Search as SearchIcon,
} from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { CIDetailDrawer } from "@/components/ci-detail-drawer"
import { ImpactCard } from "@/components/impact-card"
import { RelationPeerCell } from "@/components/relation-peer-cell"
import { fetchEolReportCsv, type CI } from "@/lib/api"
import { listAllCIs, resolveModelId } from "@/lib/cis"
import { pickAttr } from "@/lib/format"

const PAGE_SIZE = 20

/** 组件过滤「全部」选项的哨兵值（Select.Item 不允许空串） */
const ALL_COMPONENTS = "__all__"

/** 实例角色中文文案；契约枚举外的值兜底展示原文 */
const ROLE_LABELS: Record<string, string> = {
  master: "主库",
  slave: "从库",
  standalone: "单机",
}

const UNGROUPED = "未分配集群"

function clusterOf(ci: CI): string {
  const name = pickAttr(ci.attributes, ["cluster_name"])
  return name === "—" ? UNGROUPED : name
}

export default function DbmsPage() {
  const [data, setData] = useState<CI[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [keyword, setKeyword] = useState("")
  const [selected, setSelected] = useState<CI | null>(null)
  // EOL 导出过滤条件与下载态
  const [eolComponent, setEolComponent] = useState(ALL_COMPONENTS)
  const [eolVersionPrefix, setEolVersionPrefix] = useState("")
  const [downloading, setDownloading] = useState(false)
  const [downloadError, setDownloadError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const modelId = await resolveModelId("db_instance")
      setData(await listAllCIs({ model_id: modelId }))
    } catch {
      setError("加载数据库实例列表失败")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  /** 集群分组统计：按 cluster_name 聚合实例数与组件类型 */
  const clusterStats = useMemo(() => {
    const map = new Map<string, { count: number; types: Set<string> }>()
    for (const ci of data ?? []) {
      const cluster = clusterOf(ci)
      const entry = map.get(cluster) ?? { count: 0, types: new Set<string>() }
      entry.count += 1
      const type = pickAttr(ci.attributes, ["component_type"])
      if (type !== "—") entry.types.add(type)
      map.set(cluster, entry)
    }
    return [...map.entries()]
      .map(([name, entry]) => ({
        name,
        count: entry.count,
        types: [...entry.types].sort(),
      }))
      .sort((a, b) => b.count - a.count)
  }, [data])

  /** 版本分布：按 component_type + version 聚合实例数，供条形卡渲染 */
  const versionStats = useMemo(() => {
    const map = new Map<string, { label: string; count: number }>()
    for (const ci of data ?? []) {
      const type = pickAttr(ci.attributes, ["component_type"])
      const version = pickAttr(ci.attributes, ["version"])
      const label = `${type === "—" ? "未知组件" : type} ${version === "—" ? "未知版本" : version}`
      const entry = map.get(label) ?? { label, count: 0 }
      entry.count += 1
      map.set(label, entry)
    }
    return [...map.values()].sort(
      (a, b) => b.count - a.count || a.label.localeCompare(b.label)
    )
  }, [data])

  /** EOL 导出组件下拉候选：来自当前实例的去重组件类型 */
  const componentOptions = useMemo(() => {
    const types = new Set<string>()
    for (const ci of data ?? []) {
      const type = pickAttr(ci.attributes, ["component_type"])
      if (type !== "—") types.add(type)
    }
    return [...types].sort()
  }, [data])

  /** 版本分布条形基准：最大实例数（0 时兜底 1 避免除零） */
  const versionMax = Math.max(1, versionStats[0]?.count ?? 0)

  /** 触发浏览器下载 EOL 清单 CSV（带组件/版本前缀过滤） */
  const onExportEol = async () => {
    setDownloading(true)
    setDownloadError(null)
    try {
      const blob = await fetchEolReportCsv({
        component: eolComponent === ALL_COMPONENTS ? undefined : eolComponent,
        version_prefix: eolVersionPrefix.trim() || undefined,
      })
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement("a")
      anchor.href = url
      anchor.download = `dbms-eol-report-${new Date().toISOString().slice(0, 10)}.csv`
      anchor.click()
      URL.revokeObjectURL(url)
    } catch {
      setDownloadError("下载 EOL 清单失败，请稍后重试")
    } finally {
      setDownloading(false)
    }
  }

  const columns = useMemo<ColumnDef<CI>[]>(
    () => [
      {
        id: "addr",
        // accessorFn 同时服务全局关键字过滤（无 accessor 的列不参与过滤）
        accessorFn: (ci) => pickAttr(ci.attributes, ["instance_addr"]),
        header: "实例地址",
        cell: ({ row }) => (
          <span className="font-medium">{row.getValue("addr")}</span>
        ),
      },
      {
        id: "type",
        accessorFn: (ci) => pickAttr(ci.attributes, ["component_type"]),
        header: "组件类型",
        cell: ({ row }) => {
          const type = row.getValue<string>("type")
          return type === "—" ? (
            <span className="text-muted-foreground">—</span>
          ) : (
            <Badge variant="secondary">{type}</Badge>
          )
        },
      },
      {
        id: "version",
        accessorFn: (ci) => pickAttr(ci.attributes, ["version"]),
        header: "版本",
      },
      {
        id: "role",
        accessorFn: (ci) => pickAttr(ci.attributes, ["role"]),
        header: "角色",
        cell: ({ row }) => {
          const role = row.getValue<string>("role")
          return role === "—" ? "—" : (ROLE_LABELS[role] ?? role)
        },
      },
      {
        id: "cluster",
        accessorFn: (ci) => clusterOf(ci),
        header: "所属集群",
        cell: ({ row }) => {
          const cluster = row.getValue<string>("cluster")
          return cluster === UNGROUPED ? (
            <span className="text-muted-foreground">{UNGROUPED}</span>
          ) : (
            cluster
          )
        },
      },
      {
        id: "host",
        header: "关联主机",
        enableGlobalFilter: false,
        cell: ({ row }) => (
          <RelationPeerCell
            ciId={row.original.id}
            relationCode="runs_on"
            hrefFor={(peer) => `/hosts/${peer.id}`}
          />
        ),
      },
    ],
    [],
  )

  const table = useReactTable({
    data: data ?? [],
    columns,
    getCoreRowModel: getCoreRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    globalFilterFn: "includesString",
    state: { globalFilter: keyword },
    initialState: { pagination: { pageSize: PAGE_SIZE } },
  })

  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <header>
        <h1 className="text-xl font-semibold">数据库实例</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          MySQL、Redis 等数据库与中间件实例清单，由标签枚举发现通道建档并挂接宿主机，点击行查看详情
        </p>
      </header>

      {loading && !data ? (
        <div className="flex flex-col gap-2">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            {Array.from({ length: 4 }).map((_, index) => (
              <Skeleton key={index} className="h-20 w-full" />
            ))}
          </div>
          {Array.from({ length: 6 }).map((_, index) => (
            <Skeleton key={index} className="h-11 w-full" />
          ))}
        </div>
      ) : error ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
          <p className="text-xs text-destructive">{error}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      ) : (
        <>
          {/* 集群分组统计卡 */}
          {clusterStats.length > 0 ? (
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
              {clusterStats.map((stat) => (
                <Card key={stat.name}>
                  <CardHeader className="pb-2">
                    <CardTitle className="flex items-center gap-2 text-xs">
                      <DatabaseIcon className="size-4 text-muted-foreground" />
                      <span className="truncate">{stat.name}</span>
                    </CardTitle>
                  </CardHeader>
                  <CardContent className="flex flex-wrap items-center gap-1.5">
                    <Badge variant="default">{stat.count} 实例</Badge>
                    {stat.types.map((type) => (
                      <Badge key={type} variant="outline">
                        {type}
                      </Badge>
                    ))}
                  </CardContent>
                </Card>
              ))}
            </div>
          ) : null}

          {/* 版本分布 + EOL 清单导出（阶段四 4A：EOL 治理） */}
          {versionStats.length > 0 ? (
            <div className="grid gap-3 lg:grid-cols-2">
              <Card>
                <CardHeader className="pb-2">
                  <CardTitle className="text-xs">版本分布</CardTitle>
                </CardHeader>
                <CardContent className="flex flex-col gap-1.5">
                  {versionStats.map((stat) => (
                    <div
                      key={stat.label}
                      className="flex items-center gap-2 text-xs"
                    >
                      <span className="w-44 shrink-0 truncate font-medium">
                        {stat.label}
                      </span>
                      <div className="h-2 flex-1 overflow-hidden rounded bg-muted">
                        <div
                          className="h-full rounded bg-primary/70"
                          style={{
                            width: `${Math.max(
                              (stat.count / versionMax) * 100,
                              3,
                            )}%`,
                          }}
                        />
                      </div>
                      <span className="w-10 shrink-0 text-right text-muted-foreground">
                        {stat.count}
                      </span>
                    </div>
                  ))}
                </CardContent>
              </Card>

              <Card>
                <CardHeader className="pb-2">
                  <CardTitle className="text-xs">EOL 清单导出</CardTitle>
                </CardHeader>
                <CardContent className="flex flex-col gap-3">
                  <p className="text-xs text-muted-foreground">
                    按组件与版本前缀导出实例清单 CSV（含所属业务与负责人），用于版本升级与 EOL 治理
                  </p>
                  <div className="flex flex-wrap items-center gap-2">
                    <Select
                      value={eolComponent}
                      onValueChange={(v) => v && setEolComponent(v)}
                    >
                      <SelectTrigger className="w-40">
                        <SelectValue>
                          {(v: string) =>
                            v === ALL_COMPONENTS ? "全部组件" : v
                          }
                        </SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={ALL_COMPONENTS}>
                          全部组件
                        </SelectItem>
                        {componentOptions.map((type) => (
                          <SelectItem key={type} value={type}>
                            {type}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <Input
                      className="w-44"
                      placeholder="版本前缀，如 5.7（可留空）"
                      value={eolVersionPrefix}
                      onChange={(event) =>
                        setEolVersionPrefix(event.target.value)
                      }
                    />
                    <Button
                      size="sm"
                      disabled={downloading}
                      onClick={() => void onExportEol()}
                    >
                      <DownloadIcon className="mr-1 size-3.5" />
                      {downloading ? "导出中…" : "下载 CSV"}
                    </Button>
                  </div>
                  {downloadError && (
                    <p className="text-xs text-destructive">{downloadError}</p>
                  )}
                </CardContent>
              </Card>
            </div>
          ) : null}

          <div className="relative max-w-sm">
            <SearchIcon className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              className="pl-9"
              placeholder="按地址 / 组件类型 / 版本 / 集群搜索"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
            />
          </div>

          <div className="rounded-xl border">
            <Table>
              <TableHeader>
                {table.getHeaderGroups().map((headerGroup) => (
                  <TableRow key={headerGroup.id}>
                    {headerGroup.headers.map((header) => (
                      <TableHead key={header.id}>
                        {header.isPlaceholder
                          ? null
                          : flexRender(
                              header.column.columnDef.header,
                              header.getContext(),
                            )}
                      </TableHead>
                    ))}
                  </TableRow>
                ))}
              </TableHeader>
              <TableBody>
                {table.getRowModel().rows.length === 0 ? (
                  <TableRow>
                    <TableCell
                      colSpan={columns.length}
                      className="py-12 text-center text-muted-foreground"
                    >
                      {keyword
                        ? "没有匹配的实例"
                        : "暂无数据库实例数据，等待发现器上报并调和建档"}
                    </TableCell>
                  </TableRow>
                ) : (
                  table.getRowModel().rows.map((row) => (
                    <TableRow
                      key={row.id}
                      className="cursor-pointer"
                      onClick={() => setSelected(row.original)}
                    >
                      {row.getVisibleCells().map((cell) => (
                        <TableCell key={cell.id}>
                          {flexRender(
                            cell.column.columnDef.cell,
                            cell.getContext(),
                          )}
                        </TableCell>
                      ))}
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>

          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>共 {table.getFilteredRowModel().rows.length} 个实例</span>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={!table.getCanPreviousPage()}
                onClick={() => table.previousPage()}
              >
                上一页
              </Button>
              <span>
                第 {table.getState().pagination.pageIndex + 1} /{" "}
                {Math.max(1, table.getPageCount())} 页
              </span>
              <Button
                variant="outline"
                size="sm"
                disabled={!table.getCanNextPage()}
                onClick={() => table.nextPage()}
              >
                下一页
              </Button>
            </div>
          </div>
        </>
      )}

      <CIDetailDrawer
        ci={selected}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        extra={selected ? <ImpactCard ciId={selected.id} /> : null}
      />
    </div>
  )
}
