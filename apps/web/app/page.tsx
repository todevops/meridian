"use client"

// Landing 页：全局全文搜索（模型 / CI 实例 / IPAM 统一入口）。
// 无关键字时展示功能域入口卡片；输入即搜（300ms 防抖），结果按资源类型分组。

import { useEffect, useState } from "react"
import Link from "next/link"
import {
  Boxes as BoxesIcon,
  Building2 as Building2Icon,
  Loader2 as Loader2Icon,
  Network as NetworkIcon,
  Radar as RadarIcon,
  Search as SearchIcon,
  Server as ServerIcon,
} from "lucide-react"

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
  ApiError,
  globalSearch,
  type SearchGroup,
  type SearchItem,
} from "@/lib/api"

const SECTIONS = [
  {
    href: "/models",
    icon: BoxesIcon,
    title: "模型管理",
    description: "定义 CI 模型的属性、校验规则与模型间关系",
  },
  {
    href: "/hosts",
    icon: ServerIcon,
    title: "主机",
    description: "n9e 心跳、vSphere、云 API 等来源自动调和建档的主机 CI",
  },
  {
    href: "/pool",
    icon: RadarIcon,
    title: "发现池",
    description: "待人工处置的发现记录：确认入库或忽略",
  },
  {
    href: "/ipam",
    icon: NetworkIcon,
    title: "IPAM 地址管理",
    description: "子网前缀与 IP 地址的登记、分配与利用率统计",
  },
  {
    href: "/dcim",
    icon: Building2Icon,
    title: "机柜",
    description: "机房机柜 U 位占用视图与设备挂载管理",
  },
] as const

/** 搜索结果项的跳转目标 */
function itemHref(item: SearchItem): string {
  switch (item.kind) {
    case "model":
      return "/models"
    case "ci":
      if (item.model_code === "rack") return `/dcim/${item.id}`
      if (item.model_code === "room") return "/dcim"
      return `/hosts/${item.id}`
    case "ipam_prefix":
    case "ipam_ip":
      return "/ipam"
  }
}

export default function Page() {
  const [input, setInput] = useState("")
  const [query, setQuery] = useState("")
  const [groups, setGroups] = useState<SearchGroup[] | null>(null)
  const [searching, setSearching] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    const timer = setTimeout(() => setQuery(input.trim()), 300)
    return () => clearTimeout(timer)
  }, [input])

  useEffect(() => {
    if (!query) {
      setGroups(null)
      setError(null)
      return
    }
    let cancelled = false
    setSearching(true)
    setError(null)
    globalSearch(query)
      .then((res) => {
        if (!cancelled) setGroups(res.groups)
      })
      .catch((err) => {
        if (!cancelled)
          setError(
            err instanceof ApiError ? err.message : "搜索失败，请稍后重试"
          )
      })
      .finally(() => {
        if (!cancelled) setSearching(false)
      })
    return () => {
      cancelled = true
    }
  }, [query])

  const totalHits = groups?.reduce((sum, g) => sum + g.items.length, 0) ?? 0

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-6 p-6">
      <header className="flex flex-col items-center gap-4 pt-10 text-center">
        <h1 className="text-2xl font-semibold">CMDB 配置管理中心</h1>
        <p className="text-sm text-muted-foreground">
          全局搜索模型、CI 实例、IPAM 地址与机房机柜
        </p>
        <div className="relative w-full max-w-xl">
          <SearchIcon className="absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            className="h-11 rounded-full pl-10 text-base shadow-sm"
            placeholder="搜索主机名、IP、序列号、机柜、模型…"
            autoFocus
            value={input}
            onChange={(e) => setInput(e.target.value)}
          />
          {searching && (
            <Loader2Icon className="absolute top-1/2 right-3.5 size-4 -translate-y-1/2 animate-spin text-muted-foreground" />
          )}
        </div>
      </header>

      {error && (
        <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </p>
      )}

      {/* 无关键字：功能域入口 */}
      {!query && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {SECTIONS.map((section) => (
            <Link key={section.href} href={section.href} className="group">
              <Card className="h-full transition-colors group-hover:border-primary/50">
                <CardHeader>
                  <CardTitle className="flex items-center gap-2">
                    <section.icon className="size-4 text-muted-foreground transition-colors group-hover:text-primary" />
                    {section.title}
                  </CardTitle>
                  <CardDescription>{section.description}</CardDescription>
                </CardHeader>
                <CardContent />
              </Card>
            </Link>
          ))}
        </div>
      )}

      {/* 搜索结果 */}
      {query && searching && !groups && (
        <div className="flex flex-col gap-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-16 w-full" />
          ))}
        </div>
      )}
      {query && groups && totalHits === 0 && (
        <p className="py-12 text-center text-sm text-muted-foreground">
          没有找到与「{query}」相关的内容
        </p>
      )}
      {groups?.map((group) => (
        <section key={group.kind} className="flex flex-col gap-2">
          <h2 className="text-sm font-medium text-muted-foreground">
            {group.label}（{group.items.length}）
          </h2>
          <div className="flex flex-col gap-1.5">
            {group.items.map((item) => (
              <Link
                key={`${item.kind}-${item.id}`}
                href={itemHref(item)}
                className="flex items-baseline justify-between gap-4 rounded-lg border px-4 py-2.5 transition-colors hover:border-primary/50 hover:bg-muted/40"
              >
                <div className="flex min-w-0 items-baseline gap-3">
                  <span className="shrink-0 font-medium">{item.title}</span>
                  <span className="truncate text-sm text-muted-foreground">
                    {item.subtitle}
                  </span>
                </div>
                {item.matched && (
                  <span className="shrink-0 font-mono text-xs text-muted-foreground/70">
                    {item.matched}
                  </span>
                )}
              </Link>
            ))}
          </div>
        </section>
      ))}
    </div>
  )
}
