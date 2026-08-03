"use client"

// 审计日志页（F-004）：按 CI / 操作者 / 来源 / 时间范围过滤，分页回放全部写操作留痕。
// 变更摘要以截断 JSON 展示，悬浮可见完整内容。

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table"
import { ScrollText as ScrollTextIcon, Search as SearchIcon } from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
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
  listAuditLogs,
  type AuditLogItem,
  type Paged,
} from "@/lib/api"
import { auditActionLabel } from "@/lib/labels"
import { formatDateTime } from "@/lib/format"

const PAGE_SIZE = 20

/** datetime-local 输入值转 ISO 时间；空值返回 undefined */
function toISO(local: string): string | undefined {
  if (!local) return undefined
  const time = new Date(local).getTime()
  return Number.isNaN(time) ? undefined : new Date(time).toISOString()
}

/** 变更摘要：截断展示，完整 JSON 走悬浮 */
function changesSummary(changes: AuditLogItem["changes"]): string {
  if (!changes || Object.keys(changes).length === 0) return "—"
  const text = JSON.stringify(changes)
  return text.length > 80 ? `${text.slice(0, 80)}…` : text
}

export default function AuditPage() {
  const [ciIdInput, setCiIdInput] = useState("")
  const [operatorInput, setOperatorInput] = useState("")
  const [sourceInput, setSourceInput] = useState("")
  const [fromInput, setFromInput] = useState("")
  const [toInput, setToInput] = useState("")

  // 生效中的过滤条件（点击查询后生效）
  const [filters, setFilters] = useState({
    ci_id: "",
    operator: "",
    source: "",
    from: "",
    to: "",
  })
  const [page, setPage] = useState(1)
  const [data, setData] = useState<Paged<AuditLogItem> | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await listAuditLogs({
        ci_id: filters.ci_id || undefined,
        operator: filters.operator || undefined,
        source: filters.source || undefined,
        from: toISO(filters.from),
        to: toISO(filters.to),
        page,
        page_size: PAGE_SIZE,
      })
      setData(res)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载审计日志失败")
    } finally {
      setLoading(false)
    }
  }, [filters, page])

  useEffect(() => {
    void load()
  }, [load])

  const applyFilters = () => {
    setPage(1)
    setFilters({
      ci_id: ciIdInput.trim(),
      operator: operatorInput.trim(),
      source: sourceInput.trim(),
      from: fromInput,
      to: toInput,
    })
  }

  const resetFilters = () => {
    setCiIdInput("")
    setOperatorInput("")
    setSourceInput("")
    setFromInput("")
    setToInput("")
    setPage(1)
    setFilters({ ci_id: "", operator: "", source: "", from: "", to: "" })
  }

  const columns = useMemo<ColumnDef<AuditLogItem>[]>(
    () => [
      {
        accessorKey: "created_at",
        header: "时间",
        cell: ({ row }) => (
          <span className="text-muted-foreground">
            {formatDateTime(row.original.created_at)}
          </span>
        ),
      },
      {
        accessorKey: "ci_id",
        header: "CI",
        cell: ({ row }) =>
          row.original.ci_id ? (
            <Link
              href={`/hosts/${row.original.ci_id}`}
              className="text-primary underline-offset-2 hover:underline"
            >
              查看 CI
            </Link>
          ) : (
            "—"
          ),
      },
      {
        accessorKey: "action",
        header: "动作",
        cell: ({ row }) => (
          <Badge variant="secondary">{auditActionLabel(row.original.action)}</Badge>
        ),
      },
      {
        accessorKey: "source",
        header: "来源",
        cell: ({ row }) => (
          <span className="text-muted-foreground">{row.original.source}</span>
        ),
      },
      {
        accessorKey: "operator",
        header: "操作者",
        cell: ({ row }) => row.original.operator ?? "—",
      },
      {
        accessorKey: "changes",
        header: "变更摘要",
        cell: ({ row }) => {
          const changes = row.original.changes
          const full =
            changes && Object.keys(changes).length > 0
              ? JSON.stringify(changes, null, 2)
              : ""
          return (
            <span
              className="block max-w-[420px] truncate font-mono text-xs text-muted-foreground"
              title={full}
            >
              {changesSummary(changes)}
            </span>
          )
        },
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
        <h1 className="text-xl font-semibold tracking-tight">审计日志</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          全部写操作与调和历史留痕，按 CI / 操作者 / 来源 / 时间范围回放
        </p>
      </header>

      {/* 过滤器 */}
      <div className="flex flex-wrap items-end gap-3">
        <div className="flex flex-col gap-1.5">
          <Label className="text-xs text-muted-foreground">CI ID</Label>
          <Input
            className="w-48"
            placeholder="精确匹配 CI ID"
            value={ciIdInput}
            onChange={(e) => setCiIdInput(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label className="text-xs text-muted-foreground">操作者</Label>
          <Input
            className="w-36"
            placeholder="用户名 / 采集器"
            value={operatorInput}
            onChange={(e) => setOperatorInput(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label className="text-xs text-muted-foreground">来源</Label>
          <Input
            className="w-32"
            placeholder="如：web"
            value={sourceInput}
            onChange={(e) => setSourceInput(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label className="text-xs text-muted-foreground">开始时间</Label>
          <Input
            type="datetime-local"
            className="w-52"
            value={fromInput}
            onChange={(e) => setFromInput(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label className="text-xs text-muted-foreground">结束时间</Label>
          <Input
            type="datetime-local"
            className="w-52"
            value={toInput}
            onChange={(e) => setToInput(e.target.value)}
          />
        </div>
        <Button size="sm" onClick={applyFilters}>
          <SearchIcon className="mr-1 size-3.5" />
          查询
        </Button>
        <Button variant="outline" size="sm" onClick={resetFilters}>
          重置
        </Button>
      </div>

      {loading && !data ? (
        <div className="flex flex-col gap-2">
          {Array.from({ length: 8 }).map((_, i) => (
            <Skeleton key={i} className="h-11 w-full" />
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
                          : flexRender(header.column.columnDef.header, header.getContext())}
                      </TableHead>
                    ))}
                  </TableRow>
                ))}
              </TableHeader>
              <TableBody>
                {table.getRowModel().rows.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={columns.length} className="py-12 text-center">
                      <div className="flex flex-col items-center gap-2">
                        <ScrollTextIcon className="size-8 text-muted-foreground" />
                        <span className="text-xs text-muted-foreground">
                          没有匹配的审计记录
                        </span>
                      </div>
                    </TableCell>
                  </TableRow>
                ) : (
                  table.getRowModel().rows.map((row) => (
                    <TableRow key={row.id}>
                      {row.getVisibleCells().map((cell) => (
                        <TableCell key={cell.id}>
                          {flexRender(cell.column.columnDef.cell, cell.getContext())}
                        </TableCell>
                      ))}
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>

          <div className="flex items-center justify-between text-xs text-muted-foreground">
            <span>共 {data?.total ?? 0} 条记录</span>
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
