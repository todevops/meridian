"use client"

// 云资源视图：ECS（云主机）台账 + VPC/RDS/SLB 占位 Tab（迭代 2C 接入对应云采集清单）
// ECS 数据源为 host 模型中 host_type=cloud 的 CI，由阿里云/火山采集器调和建档

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
  Cloud as CloudIcon,
  Database as DatabaseIcon,
  Globe as GlobeIcon,
  Search as SearchIcon,
} from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { CI, CIStatus } from "@/lib/api"
import { listAllCIs, resolveModelId } from "@/lib/cis"
import { CI_STATUS_LABELS } from "@/lib/labels"
import { pickAttr } from "@/lib/format"

const PAGE_SIZE = 20

const STATUS_VARIANTS: Record<CIStatus, "success" | "secondary" | "outline"> = {
  active: "success",
  discovered: "secondary",
  retired: "outline",
}

/** 云厂商中文文案；采集器来源标识与属性值共用同一套编码 */
const CLOUD_PROVIDER_LABELS: Record<string, string> = {
  aliyun: "阿里云",
  volc: "火山引擎",
}

const TABS = [
  { key: "ecs", label: "ECS 云主机", icon: CloudIcon },
  { key: "vpc", label: "VPC", icon: GlobeIcon },
  { key: "rds", label: "RDS", icon: DatabaseIcon },
  { key: "slb", label: "SLB", icon: GlobeIcon },
] as const

type TabKey = (typeof TABS)[number]["key"]

/** 云厂商取值：优先属性，缺省回退建档来源（aliyun/volc 采集器） */
function cloudProviderOf(ci: CI): string {
  const raw =
    ci.attributes.cloud_provider ?? ci.attributes.provider ?? undefined
  const code =
    typeof raw === "string" && raw
      ? raw
      : CLOUD_PROVIDER_LABELS[ci.source]
        ? ci.source
        : ""
  if (!code) return "—"
  return CLOUD_PROVIDER_LABELS[code] ?? code
}

export default function CloudPage() {
  const [tab, setTab] = useState<TabKey>("ecs")
  const [data, setData] = useState<CI[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [keyword, setKeyword] = useState("")

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const hostModelId = await resolveModelId("host")
      const items = await listAllCIs({ model_id: hostModelId })
      // 云主机 = host 模型中 host_type=cloud 的子集
      setData(
        items.filter((ci) => ci.attributes.host_type === "cloud"),
      )
    } catch {
      setError("加载云主机列表失败")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const columns = useMemo<ColumnDef<CI>[]>(
    () => [
      {
        id: "ident",
        // accessorFn 同时服务全局关键字过滤（无 accessor 的列不参与过滤）
        accessorFn: (ci) =>
          pickAttr(ci.attributes, ["ident", "hostname", "name"]),
        header: "标识",
        cell: ({ row }) => (
          <span className="font-medium">{row.getValue("ident")}</span>
        ),
      },
      {
        id: "ip",
        accessorFn: (ci) => pickAttr(ci.attributes, ["ip"]),
        header: "IP",
      },
      {
        id: "provider",
        accessorFn: (ci) => cloudProviderOf(ci),
        header: "云厂商",
        cell: ({ row }) => {
          const label = row.getValue<string>("provider")
          return label === "—" ? (
            <span className="text-muted-foreground">—</span>
          ) : (
            <Badge variant="secondary">{label}</Badge>
          )
        },
      },
      {
        id: "spec",
        accessorFn: (ci) =>
          pickAttr(ci.attributes, [
            "instance_type",
            "spec",
            "flavor",
            "instance_spec",
          ]),
        header: "规格",
      },
      {
        id: "zone",
        accessorFn: (ci) =>
          pickAttr(ci.attributes, ["zone", "availability_zone", "region"]),
        header: "可用区",
      },
      {
        accessorKey: "status",
        header: "状态",
        cell: ({ row }) => (
          <Badge variant={STATUS_VARIANTS[row.original.status]}>
            {CI_STATUS_LABELS[row.original.status]}
          </Badge>
        ),
      },
      {
        id: "tags",
        accessorFn: (ci) => pickAttr(ci.attributes, ["tags", "labels"]),
        header: "标签",
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
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-5 p-6">
      <header>
        <h1 className="text-xl font-semibold">云资源</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          多云资源统一视图，由阿里云 / 火山引擎采集器经发现管道调和建档
        </p>
      </header>

      {/* Tab 栏 */}
      <div className="flex flex-wrap gap-2 border-b">
        {TABS.map(({ key, label, icon: Icon }) => (
          <button
            key={key}
            type="button"
            onClick={() => setTab(key)}
            className={`flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm transition-colors ${
              tab === key
                ? "border-primary font-medium text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            <Icon className="size-4" />
            {label}
            {key !== "ecs" ? (
              <Badge variant="outline" className="ml-1">
                2C
              </Badge>
            ) : null}
          </button>
        ))}
      </div>

      {tab !== "ecs" ? (
        // VPC / RDS / SLB：统一占位，迭代 2C 接入对应云资源清单
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-16 text-muted-foreground">
          <CloudIcon className="size-8" />
          <p className="text-sm">
            {TABS.find((item) => item.key === tab)?.label}{" "}
            台账将在迭代 2C 接入云采集器清单后展示
          </p>
          <Badge variant="secondary">迭代 2C 接入</Badge>
        </div>
      ) : loading && !data ? (
        <div className="flex flex-col gap-2">
          {Array.from({ length: 8 }).map((_, index) => (
            <Skeleton key={index} className="h-11 w-full" />
          ))}
        </div>
      ) : error ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
          <p className="text-sm text-destructive">{error}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      ) : (
        <>
          <div className="relative max-w-sm">
            <SearchIcon className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              className="pl-9"
              placeholder="按标识 / IP / 规格 / 可用区 / 标签搜索"
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
                        ? "没有匹配的云主机"
                        : "暂无云主机数据，等待云采集器上报并调和建档"}
                    </TableCell>
                  </TableRow>
                ) : (
                  table.getRowModel().rows.map((row) => (
                    <TableRow key={row.id}>
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

          <div className="flex items-center justify-between text-sm text-muted-foreground">
            <span>共 {table.getFilteredRowModel().rows.length} 台云主机</span>
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
    </div>
  )
}
