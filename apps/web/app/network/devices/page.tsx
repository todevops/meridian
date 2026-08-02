"use client"

// 网络设备台账：厂商筛选 + 关键字搜索，行点击开详情抽屉（属性 + 关系 + Oxidized 备份占位卡）
// 设备 CI 由 SNMP（LibreNMS 旁路）采集经发现池确认建档，主键 serial_no > mgmt_ip

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  flexRender,
  getCoreRowModel,
  getFilteredRowModel,
  getPaginationRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table"
import { Search as SearchIcon } from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { CIDetailDrawer } from "@/components/ci-detail-drawer"
import type { CI, CIStatus } from "@/lib/api"
import { listAllCIs, resolveModelId } from "@/lib/cis"
import { CI_STATUS_LABELS } from "@/lib/labels"
import { pickAttr } from "@/lib/format"

const PAGE_SIZE = 20
const ALL_VENDORS = "__all__"

const STATUS_VARIANTS: Record<CIStatus, "success" | "secondary" | "outline"> = {
  active: "success",
  discovered: "secondary",
  retired: "outline",
}

const DEVICE_NAME_CODES = ["name", "hostname", "sysname", "sys_name"]

function deviceName(ci: CI): string {
  return pickAttr(ci.attributes, DEVICE_NAME_CODES)
}

function deviceVendor(ci: CI): string {
  return pickAttr(ci.attributes, ["vendor", "manufacturer"])
}

export default function NetworkDevicesPage() {
  const [data, setData] = useState<CI[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [keyword, setKeyword] = useState("")
  const [vendor, setVendor] = useState<string>(ALL_VENDORS)
  const [selected, setSelected] = useState<CI | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const modelId = await resolveModelId("network_device")
      setData(await listAllCIs({ model_id: modelId }))
    } catch {
      setError("加载网络设备列表失败")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  /** 厂商筛选项：从既有数据提取去重 */
  const vendors = useMemo(() => {
    const set = new Set<string>()
    for (const ci of data ?? []) {
      const v = deviceVendor(ci)
      if (v !== "—") set.add(v)
    }
    return [...set].sort()
  }, [data])

  /** 厂商筛选在客户端叠加于全局关键字过滤之上 */
  const filtered = useMemo(() => {
    if (vendor === ALL_VENDORS) return data ?? []
    return (data ?? []).filter((ci) => deviceVendor(ci) === vendor)
  }, [data, vendor])

  const columns = useMemo<ColumnDef<CI>[]>(
    () => [
      {
        id: "name",
        // accessorFn 同时服务全局关键字过滤（无 accessor 的列不参与过滤）
        accessorFn: (ci) => deviceName(ci),
        header: "名称",
        cell: ({ row }) => (
          <span className="font-medium">{row.getValue("name")}</span>
        ),
      },
      {
        id: "mgmt_ip",
        accessorFn: (ci) => pickAttr(ci.attributes, ["mgmt_ip", "ip"]),
        header: "管理 IP",
      },
      {
        id: "vendor",
        accessorFn: (ci) => deviceVendor(ci),
        header: "厂商",
      },
      {
        id: "model",
        accessorFn: (ci) => pickAttr(ci.attributes, ["model"]),
        header: "型号",
      },
      {
        id: "serial_no",
        accessorFn: (ci) => pickAttr(ci.attributes, ["serial_no"]),
        header: "序列号",
      },
      {
        id: "source",
        accessorFn: (ci) => ci.source,
        header: "来源",
        cell: ({ row }) => <Badge variant="outline">{row.original.source}</Badge>,
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
    ],
    [],
  )

  const table = useReactTable({
    data: filtered,
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
        <h1 className="text-xl font-semibold">网络设备</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          交换机、路由器等网络设备 CI 清单，由 SNMP 采集经发现池确认建档，点击行查看详情
        </p>
      </header>

      <div className="flex flex-wrap items-center gap-3">
        <Select value={vendor} onValueChange={(value) => value && setVendor(value)}>
          <SelectTrigger className="w-40">
            <SelectValue>
              {(value: string) =>
                value === ALL_VENDORS ? "全部厂商" : value
              }
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL_VENDORS}>全部厂商</SelectItem>
            {vendors.map((item) => (
              <SelectItem key={item} value={item}>
                {item}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="relative max-w-sm flex-1">
          <SearchIcon className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            className="pl-9"
            placeholder="按名称 / 管理 IP / 厂商 / 型号 / 序列号搜索"
            value={keyword}
            onChange={(event) => setKeyword(event.target.value)}
          />
        </div>
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
                      {keyword || vendor !== ALL_VENDORS
                        ? "没有匹配的网络设备"
                        : "暂无网络设备数据，等待 SNMP 采集并经发现池确认入库"}
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
            <span>共 {table.getFilteredRowModel().rows.length} 台设备</span>
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
        extra={
          // Oxidized 备份元数据占位：2C 联调后展示备份状态与变更事件（备份原文不入库）
          <Card className="border-dashed">
            <CardHeader>
              <CardTitle className="text-xs">Oxidized 配置备份</CardTitle>
              <CardDescription>
                备份状态、最近备份时间与配置变更事件
              </CardDescription>
            </CardHeader>
            <CardContent>
              <p className="text-xs text-muted-foreground">
                备份元数据将在迭代 2C 与 Oxidized 联调后展示（备份原文不入库）。
              </p>
            </CardContent>
          </Card>
        }
      />
    </div>
  )
}
