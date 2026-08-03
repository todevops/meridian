"use client"

// 主机列表页：TanStack Table + 服务端分页，顶部状态筛选与关键字搜索，行点击跳详情

import { useCallback, useEffect, useMemo, useState } from "react"
import { useRouter } from "next/navigation"
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table"
import { Search as SearchIcon } from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
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
import {
  ApiError,
  listCIs,
  listModels,
  type CI,
  type CIStatus,
  type Paged,
} from "@/lib/api"
import { CI_STATUS_LABELS, CI_STATUSES } from "@/lib/labels"
import { formatDateTime, pickAttr } from "@/lib/format"

const PAGE_SIZE = 20

// 主机属性在不同来源（n9e/vSphere/云 API/人工）下编码可能不同，按候选顺序兜底取值
const HOST_ATTR_CODES = {
  hostname: ["hostname", "ident", "name"],
  ip: ["ip", "host_ip", "hostip", "inner_ip"],
  os: ["os"],
  cpu: ["cpu", "cpu_num", "cpu_count", "cpus"],
  bizGroup: ["biz_group", "group_name", "business_group"],
  heartbeat: ["last_heartbeat", "heartbeat_at", "last_seen_at", "update_at"],
} as const

const STATUS_VARIANTS: Record<CIStatus, "success" | "secondary" | "outline"> = {
  active: "success",
  discovered: "secondary",
  retired: "outline",
}

function hostAttr(ci: CI, key: keyof typeof HOST_ATTR_CODES): string {
  return pickAttr(ci.attributes, [...HOST_ATTR_CODES[key]])
}

export default function HostsPage() {
  const router = useRouter()
  const [modelId, setModelId] = useState<string | null>(null)
  const [data, setData] = useState<Paged<CI> | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState<CIStatus | "all">("all")
  const [keywordInput, setKeywordInput] = useState("")
  const [keyword, setKeyword] = useState("")

  // 契约中 model_id 为模型 uuid：先按编码 host 解析出模型 id；
  // 解析失败则回退为字面量 "host"，兼容后端同时支持按编码过滤的实现。
  useEffect(() => {
    let cancelled = false
    listModels({ keyword: "host", page_size: 50 })
      .then((res) => {
        if (cancelled) return
        const hostModel = res.items.find((model) => model.code === "host")
        setModelId(hostModel ? hostModel.id : "host")
      })
      .catch(() => {
        if (!cancelled) setModelId("host")
      })
    return () => {
      cancelled = true
    }
  }, [])

  const load = useCallback(async () => {
    if (modelId === null) return
    setLoading(true)
    setError(null)
    try {
      const res = await listCIs({
        model_id: modelId,
        status: status === "all" ? undefined : status,
        keyword: keyword || undefined,
        page,
        page_size: PAGE_SIZE,
      })
      setData(res)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载主机列表失败")
    } finally {
      setLoading(false)
    }
  }, [modelId, status, keyword, page])

  useEffect(() => {
    void load()
  }, [load])

  // 关键字防抖（服务端全文过滤，匹配全部属性值）
  useEffect(() => {
    const timer = setTimeout(() => {
      setPage(1)
      setKeyword(keywordInput.trim())
    }, 300)
    return () => clearTimeout(timer)
  }, [keywordInput])

  const columns = useMemo<ColumnDef<CI>[]>(
    () => [
      {
        id: "hostname",
        header: "主机名",
        cell: ({ row }) => (
          <span className="font-medium">
            {hostAttr(row.original, "hostname")}
          </span>
        ),
      },
      {
        id: "ip",
        header: "IP",
        cell: ({ row }) => hostAttr(row.original, "ip"),
      },
      {
        id: "os",
        header: "OS",
        cell: ({ row }) => hostAttr(row.original, "os"),
      },
      {
        id: "cpu",
        header: "CPU",
        cell: ({ row }) => hostAttr(row.original, "cpu"),
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
        id: "bizGroup",
        header: "业务组",
        cell: ({ row }) => hostAttr(row.original, "bizGroup"),
      },
      {
        id: "heartbeat",
        header: "最近心跳",
        cell: ({ row }) => formatDateTime(hostAttr(row.original, "heartbeat")),
      },
    ],
    []
  )

  const table = useReactTable({
    data: data?.items ?? [],
    columns,
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
    pageCount: data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 0,
  })

  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1

  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <header>
        <h1 className="text-xl font-semibold">主机列表</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          主机 CI 由 n9e 心跳、vSphere、云 API
          等来源自动调和建档，点击行查看详情
        </p>
      </header>

      <div className="flex flex-wrap items-center gap-3">
        <Select
          value={status}
          onValueChange={(value) => {
            if (!value) return
            setPage(1)
            setStatus(value)
          }}
        >
          <SelectTrigger className="w-36">
            <SelectValue>
              {(v: CIStatus | "all") =>
                v === "all" ? "全部状态" : CI_STATUS_LABELS[v]
              }
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部状态</SelectItem>
            {CI_STATUSES.map((s) => (
              <SelectItem key={s} value={s}>
                {CI_STATUS_LABELS[s]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="relative max-w-sm flex-1">
          <SearchIcon className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            className="pl-9"
            placeholder="按主机名 / IP / OS / 业务组搜索"
            value={keywordInput}
            onChange={(event) => setKeywordInput(event.target.value)}
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
                              header.getContext()
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
                        ? "当前页没有匹配的主机"
                        : "暂无主机数据，等待采集器上报并调和建档"}
                    </TableCell>
                  </TableRow>
                ) : (
                  table.getRowModel().rows.map((row) => (
                    <TableRow
                      key={row.id}
                      className="cursor-pointer"
                      onClick={() => router.push(`/hosts/${row.original.id}`)}
                    >
                      {row.getVisibleCells().map((cell) => (
                        <TableCell key={cell.id}>
                          {flexRender(
                            cell.column.columnDef.cell,
                            cell.getContext()
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
            <span>共 {data?.total ?? 0} 台主机</span>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={page <= 1 || loading}
                onClick={() => setPage((p) => p - 1)}
              >
                上一页
              </Button>
              <span>
                第 {page} / {totalPages} 页
              </span>
              <Button
                variant="outline"
                size="sm"
                disabled={page >= totalPages || loading}
                onClick={() => setPage((p) => p + 1)}
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
