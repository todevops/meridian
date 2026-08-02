"use client"

// 发现池工作台：状态 Tab + 条目表格（来源/采集器/候选模型/属性摘要/调和动作/reasons/时间），
// 待处理条目支持确认入库（模型选择+属性编辑对话框）与忽略（二次确认）

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { PoolConfirmDialog } from "@/components/pool-confirm-dialog"
import {
  ApiError,
  ignorePoolItem,
  listDiscoveryPool,
  type Paged,
  type PoolItem,
  type PoolStatus,
  type ReconcileAction,
} from "@/lib/api"
import { POOL_STATUS_LABELS, POOL_STATUSES, RECONCILE_ACTION_LABELS } from "@/lib/labels"
import { attrText, formatDateTime } from "@/lib/format"
import { cn } from "@workspace/ui/lib/utils"

const PAGE_SIZE = 20

/** 属性摘要最多展示的条目数，完整内容悬浮可见 */
const SUMMARY_ATTR_COUNT = 3

const ACTION_VARIANTS: Record<ReconcileAction, "success" | "secondary" | "destructive"> = {
  create: "success",
  update: "secondary",
  conflict: "destructive",
}

const ACTION_DOT_COLORS: Record<PoolStatus, string> = {
  pending: "bg-amber-500",
  confirmed: "bg-emerald-500",
  ignored: "bg-zinc-400",
}

/** 关键属性摘要：前 N 个 key: value，悬浮展示完整 JSON */
function attrSummary(attributes: Record<string, unknown>): { text: string; full: string } {
  const entries = Object.entries(attributes)
  const parts = entries
    .slice(0, SUMMARY_ATTR_COUNT)
    .map(([key, value]) => `${key}: ${attrText(value)}`)
  const more = entries.length > SUMMARY_ATTR_COUNT ? ` 等 ${entries.length} 项` : ""
  return { text: parts.join("，") + more, full: JSON.stringify(attributes, null, 2) }
}

export default function PoolPage() {
  const [status, setStatus] = useState<PoolStatus>("pending")
  const [data, setData] = useState<Paged<PoolItem> | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [page, setPage] = useState(1)
  const [confirming, setConfirming] = useState<PoolItem | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [ignoring, setIgnoring] = useState<PoolItem | null>(null)
  const [ignoreLoading, setIgnoreLoading] = useState(false)
  const [ignoreError, setIgnoreError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await listDiscoveryPool({ status, page, page_size: PAGE_SIZE })
      setData(res)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载发现池失败")
    } finally {
      setLoading(false)
    }
  }, [status, page])

  useEffect(() => {
    void load()
  }, [load])

  const onIgnoreConfirm = useCallback(async () => {
    if (!ignoring) return
    setIgnoreLoading(true)
    setIgnoreError(null)
    try {
      await ignorePoolItem(ignoring.id)
      setIgnoring(null)
      void load()
    } catch (err) {
      setIgnoreError(err instanceof ApiError ? err.message : "忽略失败，请稍后重试")
    } finally {
      setIgnoreLoading(false)
    }
  }, [ignoring, load])

  const columns = useMemo<ColumnDef<PoolItem>[]>(
    () => [
      {
        accessorKey: "source",
        header: "来源",
        cell: ({ row }) => <span className="font-medium">{row.original.source}</span>,
      },
      { accessorKey: "collector", header: "采集器", cell: ({ row }) => row.original.collector },
      {
        accessorKey: "model_candidate",
        header: "候选模型",
        cell: ({ row }) => (
          <code className="rounded-md bg-muted px-1.5 py-0.5 text-xs">
            {row.original.model_candidate}
          </code>
        ),
      },
      {
        id: "attributes",
        header: "关键属性",
        cell: ({ row }) => {
          const summary = attrSummary(row.original.attributes)
          return (
            <span className="block max-w-72 truncate" title={summary.full}>
              {summary.text || "—"}
            </span>
          )
        },
      },
      {
        accessorKey: "reconcile_action",
        header: "调和动作",
        cell: ({ row }) => (
          <Badge variant={ACTION_VARIANTS[row.original.reconcile_action]}>
            {RECONCILE_ACTION_LABELS[row.original.reconcile_action]}
          </Badge>
        ),
      },
      {
        accessorKey: "reasons",
        header: "判定理由",
        cell: ({ row }) => {
          const reasons = row.original.reasons ?? []
          if (reasons.length === 0) return "—"
          return (
            <span
              className="block max-w-56 truncate text-muted-foreground underline decoration-dotted underline-offset-4"
              title={reasons.join("\n")}
            >
              {reasons[0]}
              {reasons.length > 1 ? `（共 ${reasons.length} 条）` : ""}
            </span>
          )
        },
      },
      {
        accessorKey: "created_at",
        header: "发现时间",
        cell: ({ row }) => formatDateTime(row.original.created_at),
      },
      {
        id: "actions",
        header: "",
        cell: ({ row }) => {
          if (row.original.status !== "pending") return null
          return (
            <div className="flex items-center gap-1">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setConfirming(row.original)
                  setConfirmOpen(true)
                }}
              >
                确认入库
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="text-destructive"
                onClick={() => {
                  setIgnoreError(null)
                  setIgnoring(row.original)
                }}
              >
                忽略
              </Button>
            </div>
          )
        },
      },
    ],
    [],
  )

  const table = useReactTable({
    data: data?.items ?? [],
    columns,
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
  })

  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1

  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <header>
        <h1 className="text-xl font-semibold">发现池</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          各采集源上报的发现记录经调和后在此等待人工处置：确认入库或忽略
        </p>
      </header>

      {/* 状态 Tab */}
      <div className="flex items-center gap-1 rounded-lg border bg-muted/40 p-1 w-fit">
        {POOL_STATUSES.map((s) => (
          <button
            key={s}
            type="button"
            onClick={() => {
              setPage(1)
              setStatus(s)
            }}
            className={cn(
              "flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs transition-colors",
              status === s
                ? "bg-background font-medium shadow-sm"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            <span className={cn("size-1.5 rounded-full", ACTION_DOT_COLORS[s])} />
            {POOL_STATUS_LABELS[s]}
          </button>
        ))}
      </div>

      {loading && !data ? (
        <div className="flex flex-col gap-2">
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
                    <TableCell
                      colSpan={columns.length}
                      className="py-12 text-center text-muted-foreground"
                    >
                      {status === "pending"
                        ? "暂无待处理的发现记录"
                        : `暂无「${POOL_STATUS_LABELS[status]}」的记录`}
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

      <PoolConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        record={confirming}
        onConfirmed={() => void load()}
      />
      <ConfirmDialog
        open={ignoring !== null}
        onOpenChange={(open) => {
          if (!open) setIgnoring(null)
        }}
        title="忽略该发现记录？"
        description={`来源 ${ignoring?.source ?? ""} / 采集器 ${ignoring?.collector ?? ""} 的记录将被标记为已忽略，不再出现在待处理列表。`}
        confirmText="忽略"
        error={ignoreError}
        loading={ignoreLoading}
        onConfirm={() => void onIgnoreConfirm()}
      />
    </div>
  )
}
