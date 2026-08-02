"use client"

// 采集任务页（/discovery）：任务表格（名称/采集器类型/凭据/频率/启用/状态/最近成功/失败计数），
// 行操作：手动运行（toast 反馈）、运行历史（右侧抽屉）、编辑；启用 Switch 直接切换。

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table"
import {
  History as HistoryIcon,
  Loader2 as Loader2Icon,
  Play as PlayIcon,
  Plus as PlusIcon,
} from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { TaskFormDialog } from "@/components/task-form-dialog"
import { TaskRunsDrawer } from "@/components/task-runs-drawer"
import { Toast, type ToastData } from "@/components/toast"
import {
  ApiError,
  listCredentials,
  listDiscoveryTasks,
  patchDiscoveryTask,
  runDiscoveryTask,
  type Credential,
  type DiscoveryTask,
  type DiscoveryTaskStatus,
  type Paged,
} from "@/lib/api"
import { collectorTypeLabel, TASK_STATUS_LABELS } from "@/lib/labels"
import { formatDateTime } from "@/lib/format"

const PAGE_SIZE = 20

const STATUS_VARIANTS: Record<
  DiscoveryTaskStatus,
  "secondary" | "default" | "destructive"
> = {
  idle: "secondary",
  running: "default",
  error: "destructive",
}

/** 手动运行响应中提取提示文案（契约未固定形状，做宽松兜底） */
function runResultText(res: Record<string, unknown>): string {
  for (const key of ["message", "status", "run_id"]) {
    const value = res[key]
    if (typeof value === "string" && value) return value
  }
  return "已触发运行"
}

export default function DiscoveryPage() {
  const [page, setPage] = useState(1)
  const [data, setData] = useState<Paged<DiscoveryTask> | null>(null)
  const [credentials, setCredentials] = useState<Credential[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [runningIds, setRunningIds] = useState<Set<string>>(new Set())

  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<DiscoveryTask | null>(null)
  const [historyOf, setHistoryOf] = useState<DiscoveryTask | null>(null)

  const [toast, setToast] = useState<ToastData | null>(null)
  const toastTimer = useRef<number | null>(null)

  const showToast = useCallback((data: ToastData) => {
    setToast(data)
    if (toastTimer.current) window.clearTimeout(toastTimer.current)
    toastTimer.current = window.setTimeout(() => setToast(null), 4000)
  }, [])

  useEffect(() => {
    return () => {
      if (toastTimer.current) window.clearTimeout(toastTimer.current)
    }
  }, [])

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [tasks, creds] = await Promise.all([
        listDiscoveryTasks({ page, page_size: PAGE_SIZE }),
        listCredentials({ page: 1, page_size: 200 }),
      ])
      setData(tasks)
      setCredentials(creds.items)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载任务列表失败")
    } finally {
      setLoading(false)
    }
  }, [page])

  useEffect(() => {
    void load()
  }, [load])

  const credentialName = useCallback(
    (id: string | null | undefined): string => {
      if (!id) return "—"
      return credentials.find((c) => c.id === id)?.name ?? "—"
    },
    [credentials]
  )

  const onToggleEnabled = useCallback(
    async (task: DiscoveryTask, enabled: boolean) => {
      // 乐观更新
      setData((prev) =>
        prev
          ? {
              ...prev,
              items: prev.items.map((t) =>
                t.id === task.id ? { ...t, enabled } : t
              ),
            }
          : prev
      )
      try {
        await patchDiscoveryTask(task.id, { enabled })
      } catch (err) {
        showToast({
          kind: "error",
          text:
            err instanceof ApiError ? err.message : "切换启用状态失败，请重试",
        })
        void load()
      }
    },
    [load, showToast]
  )

  const onRun = useCallback(
    async (task: DiscoveryTask) => {
      setRunningIds((prev) => new Set(prev).add(task.id))
      try {
        const res = await runDiscoveryTask(task.id)
        showToast({ kind: "success", text: `「${task.name}」${runResultText(res)}` })
        void load()
      } catch (err) {
        showToast({
          kind: "error",
          text:
            err instanceof ApiError
              ? `「${task.name}」触发失败：${err.message}`
              : `「${task.name}」触发失败，请稍后重试`,
        })
      } finally {
        setRunningIds((prev) => {
          const next = new Set(prev)
          next.delete(task.id)
          return next
        })
      }
    },
    [load, showToast]
  )

  const columns = useMemo<ColumnDef<DiscoveryTask>[]>(
    () => [
      {
        accessorKey: "name",
        header: "名称",
        cell: ({ row }) => (
          <span className="font-medium">{row.original.name}</span>
        ),
      },
      {
        accessorKey: "collector_type",
        header: "采集器类型",
        cell: ({ row }) => (
          <code className="rounded-md bg-muted px-1.5 py-0.5 text-xs">
            {collectorTypeLabel(row.original.collector_type)}
          </code>
        ),
      },
      {
        id: "credential",
        header: "凭据",
        cell: ({ row }) => credentialName(row.original.credential_id),
      },
      {
        accessorKey: "interval_seconds",
        header: "频率（秒）",
        cell: ({ row }) => row.original.interval_seconds,
      },
      {
        accessorKey: "enabled",
        header: "启用",
        cell: ({ row }) => (
          <Switch
            checked={row.original.enabled}
            onCheckedChange={(checked) =>
              void onToggleEnabled(row.original, checked)
            }
            aria-label={`切换「${row.original.name}」启用状态`}
          />
        ),
      },
      {
        accessorKey: "status",
        header: "状态",
        cell: ({ row }) => {
          const status = row.original.status
          return (
            <div className="flex flex-col gap-0.5">
              <Badge variant={STATUS_VARIANTS[status] ?? "secondary"}>
                {TASK_STATUS_LABELS[status] ?? status}
              </Badge>
              {status === "error" && row.original.last_error && (
                <span
                  className="block max-w-40 truncate text-xs text-destructive"
                  title={row.original.last_error}
                >
                  {row.original.last_error}
                </span>
              )}
            </div>
          )
        },
      },
      {
        accessorKey: "last_success_at",
        header: "最近成功",
        cell: ({ row }) => formatDateTime(row.original.last_success_at),
      },
      {
        id: "counts",
        header: "运行/失败",
        cell: ({ row }) => (
          <span className="text-muted-foreground">
            {row.original.run_count} /{" "}
            <span
              className={
                row.original.fail_count > 0 ? "text-destructive" : undefined
              }
            >
              {row.original.fail_count}
            </span>
          </span>
        ),
      },
      {
        id: "actions",
        header: "",
        cell: ({ row }) => {
          const running = runningIds.has(row.original.id)
          return (
            <div className="flex items-center gap-1">
              <Button
                variant="ghost"
                size="sm"
                disabled={running}
                onClick={() => void onRun(row.original)}
              >
                {running ? (
                  <Loader2Icon className="animate-spin" />
                ) : (
                  <PlayIcon />
                )}
                运行
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setHistoryOf(row.original)}
              >
                <HistoryIcon /> 历史
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setEditing(row.original)
                  setFormOpen(true)
                }}
              >
                编辑
              </Button>
            </div>
          )
        },
      },
    ],
    [credentialName, onRun, onToggleEnabled, runningIds]
  )

  const table = useReactTable({
    data: data?.items ?? [],
    columns,
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
  })

  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1

  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-5 p-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">采集任务</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            内置与外部采集器的调度管理：周期运行、手动触发与运行历史追踪
          </p>
        </div>
        <Button
          onClick={() => {
            setEditing(null)
            setFormOpen(true)
          }}
        >
          <PlusIcon /> 新建任务
        </Button>
      </header>

      {loading && !data ? (
        <div className="flex flex-col gap-2">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-11 w-full" />
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
                      暂无采集任务，点击右上角「新建任务」创建
                    </TableCell>
                  </TableRow>
                ) : (
                  table.getRowModel().rows.map((row) => (
                    <TableRow key={row.id}>
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

          <div className="flex items-center justify-between text-sm text-muted-foreground">
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

      <TaskFormDialog
        open={formOpen}
        onOpenChange={setFormOpen}
        task={editing}
        onSaved={() => void load()}
      />
      <TaskRunsDrawer
        open={historyOf !== null}
        onOpenChange={(open) => {
          if (!open) setHistoryOf(null)
        }}
        task={historyOf}
      />
      <Toast data={toast} />
    </div>
  )
}
