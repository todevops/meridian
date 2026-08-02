"use client"

// 凭据审计抽屉：右侧滑出，分页展示该凭据的操作审计记录（操作/操作者/来源/时间）

import { useCallback, useEffect, useState } from "react"

import { Button } from "@workspace/ui/components/button"
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
  listCredentialAudits,
  type Credential,
  type CredentialAudit,
  type Paged,
} from "@/lib/api"
import { auditActionLabel } from "@/lib/labels"
import { formatDateTime } from "@/lib/format"

const PAGE_SIZE = 20

interface CredentialAuditDrawerProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  credential: Credential | null
}

export function CredentialAuditDrawer({
  open,
  onOpenChange,
  credential,
}: CredentialAuditDrawerProps) {
  const [page, setPage] = useState(1)
  const [data, setData] = useState<Paged<CredentialAudit> | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    if (!credential) return
    setLoading(true)
    setError(null)
    try {
      setData(
        await listCredentialAudits(credential.id, { page, page_size: PAGE_SIZE })
      )
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载审计记录失败")
    } finally {
      setLoading(false)
    }
  }, [credential, page])

  useEffect(() => {
    if (!open) return
    setPage(1)
  }, [open, credential])

  useEffect(() => {
    if (open) void load()
  }, [open, load])

  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1

  return (
    <Drawer open={open} onOpenChange={onOpenChange}>
      <DrawerContent>
        <DrawerHeader>
          <DrawerTitle>操作审计</DrawerTitle>
          <DrawerDescription>
            凭据「{credential?.name}」的新建、轮换与取用记录
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
                    <TableHead>操作</TableHead>
                    <TableHead>操作者</TableHead>
                    <TableHead>来源</TableHead>
                    <TableHead>时间</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {(data?.items ?? []).length === 0 ? (
                    <TableRow>
                      <TableCell
                        colSpan={4}
                        className="py-12 text-center text-muted-foreground"
                      >
                        暂无审计记录
                      </TableCell>
                    </TableRow>
                  ) : (
                    (data?.items ?? []).map((audit) => (
                      <TableRow key={audit.id}>
                        <TableCell className="font-medium">
                          {auditActionLabel(audit.action)}
                        </TableCell>
                        <TableCell>{audit.operator || "—"}</TableCell>
                        <TableCell className="text-muted-foreground">
                          {audit.source || "—"}
                        </TableCell>
                        <TableCell className="text-muted-foreground">
                          {formatDateTime(audit.created_at)}
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
