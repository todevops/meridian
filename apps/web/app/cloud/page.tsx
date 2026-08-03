"use client"

// 云资源视图：ECS（云主机）+ VPC/RDS/SLB 台账 Tab。
// ECS 数据源为 host 模型中 host_type=cloud 的 CI；VPC/RDS/SLB 为 cloud_vpc/cloud_rds/cloud_slb 模型，
// 均由阿里云/火山采集器经发现管道调和建档

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
  { key: "ecs", label: "ECS 云主机", icon: CloudIcon, modelCode: "host", noun: "云主机" },
  { key: "vpc", label: "VPC", icon: GlobeIcon, modelCode: "cloud_vpc", noun: "VPC" },
  { key: "rds", label: "RDS", icon: DatabaseIcon, modelCode: "cloud_rds", noun: "云数据库" },
  { key: "slb", label: "SLB", icon: GlobeIcon, modelCode: "cloud_slb", noun: "负载均衡" },
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

/** 云厂商列（各 Tab 共用） */
function providerColumn(): ColumnDef<CI> {
  return {
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
  }
}

/** 状态列（各 Tab 共用） */
function statusColumn(): ColumnDef<CI> {
  return {
    accessorKey: "status",
    header: "状态",
    cell: ({ row }) => (
      <Badge variant={STATUS_VARIANTS[row.original.status]}>
        {CI_STATUS_LABELS[row.original.status]}
      </Badge>
    ),
  }
}

/** 各 Tab 的列定义 */
function columnsFor(tab: TabKey): ColumnDef<CI>[] {
  switch (tab) {
    case "vpc":
      return [
        {
          id: "name",
          accessorFn: (ci) => pickAttr(ci.attributes, ["name", "vpc_id"]),
          header: "名称",
          cell: ({ row }) => (
            <span className="font-medium">{row.getValue("name")}</span>
          ),
        },
        {
          id: "vpc_id",
          accessorFn: (ci) => pickAttr(ci.attributes, ["vpc_id"]),
          header: "VPC ID",
        },
        {
          id: "cidr",
          accessorFn: (ci) => pickAttr(ci.attributes, ["cidr"]),
          header: "网段",
        },
        {
          id: "region",
          accessorFn: (ci) => pickAttr(ci.attributes, ["region"]),
          header: "地域",
        },
        providerColumn(),
        statusColumn(),
        {
          id: "tags",
          accessorFn: (ci) => pickAttr(ci.attributes, ["tags"]),
          header: "标签",
        },
      ]
    case "rds":
      return [
        {
          id: "name",
          accessorFn: (ci) =>
            pickAttr(ci.attributes, ["name", "db_instance_id"]),
          header: "实例名称",
          cell: ({ row }) => (
            <span className="font-medium">{row.getValue("name")}</span>
          ),
        },
        {
          id: "engine",
          accessorFn: (ci) =>
            [pickAttr(ci.attributes, ["engine"]), pickAttr(ci.attributes, ["engine_version"])]
              .filter((part) => part !== "—")
              .join(" "),
          header: "引擎",
        },
        {
          id: "spec",
          accessorFn: (ci) => pickAttr(ci.attributes, ["spec"]),
          header: "规格",
        },
        {
          id: "zone",
          accessorFn: (ci) => pickAttr(ci.attributes, ["zone", "region"]),
          header: "可用区",
        },
        providerColumn(),
        statusColumn(),
        {
          id: "tags",
          accessorFn: (ci) => pickAttr(ci.attributes, ["tags"]),
          header: "标签",
        },
      ]
    case "slb":
      return [
        {
          id: "name",
          accessorFn: (ci) => pickAttr(ci.attributes, ["name", "slb_id"]),
          header: "实例名称",
          cell: ({ row }) => (
            <span className="font-medium">{row.getValue("name")}</span>
          ),
        },
        {
          id: "vip",
          accessorFn: (ci) => pickAttr(ci.attributes, ["vip"]),
          header: "服务地址",
        },
        {
          id: "slb_type",
          accessorFn: (ci) => pickAttr(ci.attributes, ["slb_type"]),
          header: "类型",
        },
        {
          id: "region",
          accessorFn: (ci) => pickAttr(ci.attributes, ["region"]),
          header: "地域",
        },
        providerColumn(),
        statusColumn(),
        {
          id: "tags",
          accessorFn: (ci) => pickAttr(ci.attributes, ["tags"]),
          header: "标签",
        },
      ]
    default: // ecs
      return [
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
        providerColumn(),
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
        statusColumn(),
        {
          id: "tags",
          accessorFn: (ci) => pickAttr(ci.attributes, ["tags", "labels"]),
          header: "标签",
        },
      ]
  }
}

export default function CloudPage() {
  const [tab, setTab] = useState<TabKey>("ecs")
  const [data, setData] = useState<CI[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [keyword, setKeyword] = useState("")

  const tabDef = TABS.find((item) => item.key === tab) ?? TABS[0]

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const modelId = await resolveModelId(tabDef.modelCode)
      const items = await listAllCIs({ model_id: modelId })
      // 云主机 = host 模型中 host_type=cloud 的子集；其余 Tab 直接取对应模型
      setData(
        tabDef.key === "ecs"
          ? items.filter((ci) => ci.attributes.host_type === "cloud")
          : items,
      )
    } catch {
      setError(`加载${tabDef.noun}列表失败`)
    } finally {
      setLoading(false)
    }
  }, [tabDef])

  useEffect(() => {
    setData(null)
    setKeyword("")
    void load()
  }, [load])

  const columns = useMemo<ColumnDef<CI>[]>(() => columnsFor(tab), [tab])

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
        <h1 className="text-xl font-semibold">云资源</h1>
        <p className="mt-1 text-xs text-muted-foreground">
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
            className={`flex items-center gap-1.5 border-b-2 px-3 py-2 text-xs transition-colors ${
              tab === key
                ? "border-primary font-medium text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground"
            }`}
          >
            <Icon className="size-4" />
            {label}
          </button>
        ))}
      </div>

      {loading && !data ? (
        <div className="flex flex-col gap-2">
          {Array.from({ length: 8 }).map((_, index) => (
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
          <div className="relative max-w-sm">
            <SearchIcon className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              className="pl-9"
              placeholder={`搜索${tabDef.noun}`}
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
                        ? `没有匹配的${tabDef.noun}`
                        : `暂无${tabDef.noun}数据，等待云采集器上报并调和建档`}
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

          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>
              共 {table.getFilteredRowModel().rows.length} 条{tabDef.noun}
            </span>
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
