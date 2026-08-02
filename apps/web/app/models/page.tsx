"use client"

// 模型管理页：模型列表（TanStack Table，服务端分页）+ 新建/编辑对话框

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from "@tanstack/react-table"
import { Plus as PlusIcon, Search as SearchIcon } from "lucide-react"

import { Button } from "@workspace/ui/components/button"
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
import { ModelFormDialog } from "@/components/model-form-dialog"
import { ApiError, listCIs, listModels, type Model, type Paged } from "@/lib/api"
import { formatDateTime } from "@/lib/format"

const PAGE_SIZE = 20

export default function ModelsPage() {
  const [data, setData] = useState<Paged<Model> | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [page, setPage] = useState(1)
  const [keywordInput, setKeywordInput] = useState("")
  const [keyword, setKeyword] = useState("")
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingModel, setEditingModel] = useState<Model | null>(null)
  // 各模型的 CI 数（列表接口不含该字段，按模型逐个统计；-1 表示获取失败）
  const [ciCounts, setCiCounts] = useState<Record<string, number>>({})

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await listModels({ page, page_size: PAGE_SIZE, keyword: keyword || undefined })
      setData(res)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载模型列表失败")
    } finally {
      setLoading(false)
    }
  }, [page, keyword])

  useEffect(() => {
    void load()
  }, [load])

  // 关键字防抖
  useEffect(() => {
    const timer = setTimeout(() => {
      setPage(1)
      setKeyword(keywordInput.trim())
    }, 300)
    return () => clearTimeout(timer)
  }, [keywordInput])

  // 模型加载后统计各模型 CI 数
  useEffect(() => {
    if (!data) return
    let cancelled = false
    void Promise.all(
      data.items.map(async (model): Promise<readonly [string, number]> => {
        try {
          const res = await listCIs({ model_id: model.id, page: 1, page_size: 1 })
          return [model.id, res.total] as const
        } catch {
          return [model.id, -1] as const
        }
      }),
    ).then((entries) => {
      if (!cancelled) setCiCounts(Object.fromEntries(entries))
    })
    return () => {
      cancelled = true
    }
  }, [data])

  const openCreate = useCallback(() => {
    setEditingModel(null)
    setDialogOpen(true)
  }, [])

  const columns = useMemo<ColumnDef<Model>[]>(
    () => [
      {
        accessorKey: "name",
        header: "名称",
        cell: ({ row }) => <span className="font-medium">{row.original.name}</span>,
      },
      {
        accessorKey: "code",
        header: "编码",
        cell: ({ row }) => (
          <code className="rounded-md bg-muted px-1.5 py-0.5 text-xs">{row.original.code}</code>
        ),
      },
      {
        id: "attrCount",
        header: "属性数",
        cell: ({ row }) => row.original.attributes.length,
      },
      {
        id: "ciCount",
        header: "CI 数",
        cell: ({ row }) => {
          const count = ciCounts[row.original.id]
          return count === undefined || count < 0 ? "—" : count
        },
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
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setEditingModel(row.original)
              setDialogOpen(true)
            }}
          >
            编辑
          </Button>
        ),
      },
    ],
    [ciCounts],
  )

  const table = useReactTable({
    data: data?.items ?? [],
    columns,
    getCoreRowModel: getCoreRowModel(),
  })

  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1

  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">模型管理</h1>
          <p className="mt-1 text-xs text-muted-foreground">
            定义 CI 模型的属性、校验规则与模型间关系
          </p>
        </div>
        <Button onClick={openCreate}>
          <PlusIcon /> 新建模型
        </Button>
      </header>

      <div className="relative max-w-sm">
        <SearchIcon className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          className="pl-9"
          placeholder="按名称或编码搜索"
          value={keywordInput}
          onChange={(event) => setKeywordInput(event.target.value)}
        />
      </div>

      {loading && !data ? (
        <div className="flex flex-col gap-2">
          {Array.from({ length: 5 }).map((_, index) => (
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
                      {keyword ? "没有匹配的模型" : "暂无模型，点击右上角「新建模型」开始建模"}
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
            <span>共 {data?.total ?? 0} 个模型</span>
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

      <ModelFormDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        model={editingModel}
        modelCodes={(data?.items ?? []).map((model) => model.code)}
        onSaved={() => void load()}
      />
    </div>
  )
}
