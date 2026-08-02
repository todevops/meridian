"use client"

// 任务运行历史抽屉：右侧滑出，分页展示运行记录（时间/成败/产出条数/错误摘要）

import { useCallback, useEffect, useState } from "react"

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
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerHeader,
  DrawerTitle,
} from "@/components/ui/drawer"
import {
  ApiError,
  listDiscoveryTaskRuns,
  type DiscoveryTask,
  type DiscoveryTaskRun,
  type Paged,
} from "@/lib/api"
import { formatDateTime } from "@/lib/format"

const PAGE_SIZE = 20

interface TaskRunsDrawerProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  task: DiscoveryTask | null
}

export function TaskRunsDrawer({ open, onOpenChange, task }: TaskRunsDrawerProps) {
  const [page, setPage] = useState(1)
  const [data, setData] = useState<Paged<DiscoveryTaskRun> | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    if (!task) return
    setLoading(true)
    setError(null)
    try {
      setData(await listDiscoveryTaskRuns(task.id, { page, page_size: PAGE_SIZE }))
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载运行历史失败")
    } finally {
      setLoading(false)
    }
  }, [task, page])

  useEffect(() => {
    if (!open) return
    setPage(1)
  }, [open, task])

  useEffect(() => {
    if (open) void load()
  }, [open, load])

  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1

  return (
    <Drawer open={open} onOpenChange={onOpenChange}>
      <DrawerContent>
        <DrawerHeader>
          <DrawerTitle>运行历史</DrawerTitle>
          <DrawerDescription>
            任务「{task?.name}」的历次调度与手动运行记录
          </DrawerDescription>
        </DrawerHeader>

        {loading && !data ? (
          <div className="flex flex-col gap-2">
            {Array.from({ length: 6 }).map((_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
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
                  <TableRow>
                    <TableHead>开始时间</TableHead>
                    <TableHead>结束时间</TableHead>
                    <TableHead>结果</TableHead>
                    <TableHead>产出</TableHead>
                    <TableHead>错误摘要</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {(data?.items ?? []).length === 0 ? (
                    <TableRow>
                      <TableCell
                        colSpan={5}
                        className="py-12 text-center text-muted-foreground"
                      >
                        暂无运行记录
                      </TableCell>
                    </TableRow>
                  ) : (
                    (data?.items ?? []).map((run) => (
                      <TableRow key={run.id}>
                        <TableCell className="text-muted-foreground">
                          {formatDateTime(run.started_at)}
                        </TableCell>
                        <TableCell className="text-muted-foreground">
                          {formatDateTime(run.finished_at)}
                        </TableCell>
                        <TableCell>
                          {run.success ? (
                            <Badge variant="success">成功</Badge>
                          ) : (
                            <Badge variant="destructive">失败</Badge>
                          )}
                        </TableCell>
                        <TableCell>{run.produced}</TableCell>
                        <TableCell>
                          <span
                            className="block max-w-48 truncate text-muted-foreground"
                            title={run.error_summary ?? ""}
                          >
                            {run.error_summary || "—"}
                          </span>
                        </TableCell>
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
      </DrawerContent>
    </Drawer>
  )
}
