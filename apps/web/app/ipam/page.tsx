"use client"

// IPAM 页：前缀表格（CIDR/名称/描述/利用率/已用-总数），关键字搜索，新建前缀对话框，
// 行点击打开右侧抽屉展示 IP 列表与分配/登记操作

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
import { PrefixCreateDialog } from "@/components/prefix-create-dialog"
import { PrefixDrawer, utilizationPercent } from "@/components/prefix-drawer"
import { ApiError, listPrefixes, type IpamPrefix, type Paged } from "@/lib/api"

const PAGE_SIZE = 20

/** 利用率进度条：>90% 红色告警，>70% 黄色提醒，其余主色 */
function UtilizationBar({ prefix }: { prefix: IpamPrefix }) {
  const percent = utilizationPercent(prefix)
  if (percent === null) return <span className="text-muted-foreground">—</span>
  const color =
    percent > 90 ? "bg-destructive" : percent > 70 ? "bg-amber-500" : "bg-emerald-500"
  return (
    <div className="flex min-w-36 items-center gap-2">
      <div className="h-2 w-24 overflow-hidden rounded-full bg-muted">
        <div className={`h-full rounded-full ${color}`} style={{ width: `${Math.min(100, percent)}%` }} />
      </div>
      <span className="text-xs text-muted-foreground">{percent}%</span>
    </div>
  )
}

export default function IpamPage() {
  const [data, setData] = useState<Paged<IpamPrefix> | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [page, setPage] = useState(1)
  const [keywordInput, setKeywordInput] = useState("")
  const [keyword, setKeyword] = useState("")
  const [createOpen, setCreateOpen] = useState(false)
  const [selected, setSelected] = useState<IpamPrefix | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await listPrefixes({ keyword: keyword || undefined, page, page_size: PAGE_SIZE })
      setData(res)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载前缀列表失败")
    } finally {
      setLoading(false)
    }
  }, [keyword, page])

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

  const columns = useMemo<ColumnDef<IpamPrefix>[]>(
    () => [
      {
        accessorKey: "cidr",
        header: "CIDR",
        cell: ({ row }) => (
          <code className="rounded-md bg-muted px-1.5 py-0.5 text-xs font-medium">
            {row.original.cidr}
          </code>
        ),
      },
      {
        accessorKey: "name",
        header: "名称",
        cell: ({ row }) => <span className="font-medium">{row.original.name}</span>,
      },
      {
        accessorKey: "description",
        header: "描述",
        cell: ({ row }) => (
          <span className="block max-w-56 truncate" title={row.original.description ?? ""}>
            {row.original.description || "—"}
          </span>
        ),
      },
      {
        accessorKey: "vlan_id",
        header: "VLAN",
        cell: ({ row }) => row.original.vlan_id ?? "—",
      },
      {
        id: "utilization",
        header: "利用率",
        cell: ({ row }) => <UtilizationBar prefix={row.original} />,
      },
      {
        id: "used",
        header: "已用 / 总数",
        cell: ({ row }) => {
          const util = row.original.utilization
          return util ? `${util.used_ips} / ${util.total_ips}` : "—"
        },
      },
      {
        id: "actions",
        header: "",
        cell: ({ row }) => (
          <Button
            variant="ghost"
            size="sm"
            onClick={(event) => {
              event.stopPropagation()
              setSelected(row.original)
            }}
          >
            详情
          </Button>
        ),
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
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">IPAM 地址管理</h1>
          <p className="mt-1 text-xs text-muted-foreground">
            子网前缀与 IP 地址的登记、分配与利用率统计，点击行查看前缀下的 IP 明细
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <PlusIcon /> 新建前缀
        </Button>
      </header>

      <div className="relative max-w-sm">
        <SearchIcon className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          className="pl-9"
          placeholder="按 CIDR 或名称搜索"
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
                      {keyword ? "没有匹配的前缀" : "暂无前缀，点击右上角「新建前缀」登记子网"}
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
            <span>共 {data?.total ?? 0} 个前缀</span>
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

      <PrefixCreateDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        prefixes={data?.items ?? []}
        onSaved={() => void load()}
      />
      <PrefixDrawer
        prefix={selected}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        onChanged={() => void load()}
      />
    </div>
  )
}
