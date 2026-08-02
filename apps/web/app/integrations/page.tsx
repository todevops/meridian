"use client"

// 凭据管理页（/integrations）：类型筛选 + 凭据表格（类型/名称/描述/最近轮换/使用次数/更新时间），
// 行操作：编辑、轮换（重新录入密文）、审计（右侧抽屉）；列表永不展示密文。

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table"
import { Plus as PlusIcon } from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
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
import { CredentialFormDialog } from "@/components/credential-form-dialog"
import { CredentialRotateDialog } from "@/components/credential-rotate-dialog"
import { CredentialAuditDrawer } from "@/components/credential-audit-drawer"
import {
  ApiError,
  listCredentials,
  type Credential,
  type CredentialType,
  type Paged,
} from "@/lib/api"
import { CREDENTIAL_TYPES, CREDENTIAL_TYPE_LABELS } from "@/lib/labels"
import { formatDateTime } from "@/lib/format"

const PAGE_SIZE = 20

/** 类型筛选的「全部」选项值（Select 不允许空串 value） */
const TYPE_ALL = "__all__"

export default function IntegrationsPage() {
  const [typeFilter, setTypeFilter] = useState<string>(TYPE_ALL)
  const [page, setPage] = useState(1)
  const [data, setData] = useState<Paged<Credential> | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Credential | null>(null)
  const [rotating, setRotating] = useState<Credential | null>(null)
  const [auditing, setAuditing] = useState<Credential | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setData(
        await listCredentials({
          type:
            typeFilter === TYPE_ALL
              ? undefined
              : (typeFilter as CredentialType),
          page,
          page_size: PAGE_SIZE,
        })
      )
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载凭据列表失败")
    } finally {
      setLoading(false)
    }
  }, [typeFilter, page])

  useEffect(() => {
    void load()
  }, [load])

  const columns = useMemo<ColumnDef<Credential>[]>(
    () => [
      {
        accessorKey: "type",
        header: "类型",
        cell: ({ row }) => (
          <Badge variant="secondary">
            {CREDENTIAL_TYPE_LABELS[row.original.type] ?? row.original.type}
          </Badge>
        ),
      },
      {
        accessorKey: "name",
        header: "名称",
        cell: ({ row }) => (
          <span className="font-medium">{row.original.name}</span>
        ),
      },
      {
        accessorKey: "description",
        header: "描述",
        cell: ({ row }) => (
          <span className="block max-w-56 truncate text-muted-foreground">
            {row.original.description || "—"}
          </span>
        ),
      },
      {
        accessorKey: "last_rotated_at",
        header: "最近轮换",
        cell: ({ row }) => formatDateTime(row.original.last_rotated_at),
      },
      {
        accessorKey: "use_count",
        header: "使用次数",
        cell: ({ row }) => row.original.use_count,
      },
      {
        accessorKey: "updated_at",
        header: "更新时间",
        cell: ({ row }) => formatDateTime(row.original.updated_at),
      },
      {
        id: "actions",
        header: "",
        cell: ({ row }) => (
          <div className="flex items-center gap-1">
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
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setRotating(row.original)}
            >
              轮换
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setAuditing(row.original)}
            >
              审计
            </Button>
          </div>
        ),
      },
    ],
    []
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
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">凭据管理</h1>
          <p className="mt-1 text-xs text-muted-foreground">
            采集器接入外部系统的凭据统一托管：密文加密存储、永不回读，仅可通过轮换更新
          </p>
        </div>
        <Button
          onClick={() => {
            setEditing(null)
            setFormOpen(true)
          }}
        >
          <PlusIcon /> 新建凭据
        </Button>
      </header>

      {/* 类型筛选 */}
      <div className="w-56">
        <Select
          value={typeFilter}
          onValueChange={(value) => {
            if (!value) return
            setPage(1)
            setTypeFilter(value)
          }}
        >
          <SelectTrigger>
            <SelectValue>
              {(v: string) =>
                v === TYPE_ALL
                  ? "全部类型"
                  : (CREDENTIAL_TYPE_LABELS[v as CredentialType] ?? v)
              }
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={TYPE_ALL}>全部类型</SelectItem>
            {CREDENTIAL_TYPES.map((t) => (
              <SelectItem key={t} value={t}>
                {CREDENTIAL_TYPE_LABELS[t]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {loading && !data ? (
        <div className="flex flex-col gap-2">
          {Array.from({ length: 6 }).map((_, i) => (
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
                      暂无凭据，点击右上角「新建凭据」接入外部系统
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

      <CredentialFormDialog
        open={formOpen}
        onOpenChange={setFormOpen}
        credential={editing}
        onSaved={() => void load()}
      />
      <CredentialRotateDialog
        open={rotating !== null}
        onOpenChange={(open) => {
          if (!open) setRotating(null)
        }}
        credential={rotating}
        onRotated={() => void load()}
      />
      <CredentialAuditDrawer
        open={auditing !== null}
        onOpenChange={(open) => {
          if (!open) setAuditing(null)
        }}
        credential={auditing}
      />
    </div>
  )
}
