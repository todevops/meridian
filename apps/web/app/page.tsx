"use client"

// Landing 页：全局全文搜索（模型 / CI 实例 / IPAM 统一入口）。
// 无关键字时按 F-090 六组展示功能入口卡片，并附规划模块占位卡；输入即搜（300ms 防抖），结果按资源类型分组。

import { useEffect, useState } from "react"
import Link from "next/link"
import {
  Boxes as BoxesIcon,
  Building2 as Building2Icon,
  Cable as CableIcon,
  ChartColumn as ChartColumnIcon,
  Cloud as CloudIcon,
  Container as ContainerIcon,
  Database as DatabaseIcon,
  Gauge as GaugeIcon,
  KeyRound as KeyRoundIcon,
  Layers as LayersIcon,
  ListChecks as ListChecksIcon,
  Loader2 as Loader2Icon,
  Network as NetworkIcon,
  Radar as RadarIcon,
  ScrollText as ScrollTextIcon,
  Search as SearchIcon,
  Server as ServerIcon,
  Settings as SettingsIcon,
  Share2 as Share2Icon,
  ShieldCheck as ShieldCheckIcon,
  UsersRound as UsersRoundIcon,
  type LucideIcon,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
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

interface SectionCard {
  href: string
  icon: LucideIcon
  title: string
  description: string
}

interface SectionGroup {
  key: string
  label: string
  cards: SectionCard[]
}

const SECTION_GROUPS: SectionGroup[] = [
  {
    key: "overview",
    label: "总览",
    cards: [
      {
        href: "/dashboard",
        icon: GaugeIcon,
        title: "运营仪表盘",
        description: "数据质量五指标看板：完整率、关联完整率、孤岛、鲜度与监控覆盖率",
      },
    ],
  },
  {
    key: "assets",
    label: "资产管理",
    cards: [
      {
        href: "/hosts",
        icon: ServerIcon,
        title: "主机",
        description: "n9e 心跳、vSphere、云 API 等来源自动调和建档的主机 CI",
      },
      {
        href: "/virtualization",
        icon: LayersIcon,
        title: "虚拟化",
        description: "集群 → ESXi → 虚拟机三级视图，vSphere 采集自动建档",
      },
      {
        href: "/cloud",
        icon: CloudIcon,
        title: "云资源",
        description: "阿里云 / 火山引擎 ECS 等云资源统一台账",
      },
      {
        href: "/network/devices",
        icon: CableIcon,
        title: "网络设备",
        description: "交换机、路由器等网络设备清单，SNMP 采集建档",
      },
      {
        href: "/dcim",
        icon: Building2Icon,
        title: "机房与机柜",
        description: "机房机柜 U 位占用视图与设备挂载管理",
      },
    ],
  },
  {
    key: "network",
    label: "网络与地址",
    cards: [
      {
        href: "/ipam",
        icon: NetworkIcon,
        title: "IPAM 地址管理",
        description: "子网前缀与 IP 地址的登记、分配与利用率统计",
      },
    ],
  },
  {
    key: "discovery",
    label: "发现与治理",
    cards: [
      {
        href: "/pool",
        icon: RadarIcon,
        title: "发现池",
        description: "待人工处置的发现记录：确认入库或忽略",
      },
      {
        href: "/governance",
        icon: ShieldCheckIcon,
        title: "稽核与整改",
        description: "稽核规则管理、整改待办闭环与待退役资产会签处置",
      },
      {
        href: "/discovery",
        icon: ListChecksIcon,
        title: "采集任务",
        description: "内置与外部采集器的调度、手动运行与运行历史",
      },
      {
        href: "/integrations",
        icon: KeyRoundIcon,
        title: "凭据管理",
        description: "各外部系统接入凭据的托管、轮换与操作审计",
      },
    ],
  },
  {
    key: "dbms",
    label: "数据库与中间件",
    cards: [
      {
        href: "/dbms",
        icon: DatabaseIcon,
        title: "数据库实例",
        description: "MySQL、Redis 等实例清单与集群分组视图",
      },
    ],
  },
  {
    key: "system",
    label: "平台配置",
    cards: [
      {
        href: "/models",
        icon: BoxesIcon,
        title: "模型管理",
        description: "定义 CI 模型的属性、校验规则与模型间关系",
      },
      {
        href: "/audit",
        icon: ScrollTextIcon,
        title: "审计日志",
        description: "按 CI / 操作者 / 来源回放全部写操作与调和历史",
      },
      {
        href: "/settings/users",
        icon: UsersRoundIcon,
        title: "用户管理",
        description: "系统账号的新建、角色分配与启停",
      },
      {
        href: "/settings/roles",
        icon: SettingsIcon,
        title: "角色管理",
        description: "角色与权限点的维护",
      },
    ],
  },
]

/** 规划中的模块占位卡（不可跳转，标注预计上线迭代） */
const PLANNED_CARDS = [
  {
    icon: ContainerIcon,
    title: "K8s 元数据",
    description: "集群、命名空间与工作负载 CI 的自动建档",
    eta: "迭代 2B 上线",
  },
  {
    icon: Share2Icon,
    title: "网络拓扑",
    description: "基于 LLDP/CDP 与 IPAM 数据的链路拓扑可视化",
    eta: "迭代 2B 上线",
  },
  {
    icon: ChartColumnIcon,
    title: "运营报表",
    description: "资产分布、采集覆盖率与调和质量的多维报表",
    eta: "迭代 2C 上线",
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
    <div className="flex w-full flex-col gap-6 p-6">
      <header className="flex flex-col items-center gap-4 pt-8 text-center">
        <h1 className="text-xl font-semibold">Meridian 配置管理中心</h1>
        <p className="text-xs text-muted-foreground">
          全局搜索模型、CI 实例、IPAM 地址与机房机柜
        </p>
        <div className="relative w-full max-w-xl">
          <SearchIcon className="absolute top-1/2 left-3.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            className="h-11 rounded-full pl-10 text-sm shadow-sm"
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
        <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {error}
        </p>
      )}

      {/* 无关键字：按六组展示功能域入口 */}
      {!query && (
        <>
          {SECTION_GROUPS.map((group) => (
            <section key={group.key} className="flex flex-col gap-3">
              <h2 className="text-xs font-medium text-muted-foreground">
                {group.label}
              </h2>
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
                {group.cards.map((card) => (
                  <Link key={card.href} href={card.href} className="group">
                    <Card className="h-full transition-colors group-hover:border-primary/50">
                      <CardHeader>
                        <CardTitle className="flex items-center gap-2 text-sm">
                          <card.icon className="size-4 text-muted-foreground transition-colors group-hover:text-primary" />
                          {card.title}
                        </CardTitle>
                        <CardDescription>{card.description}</CardDescription>
                      </CardHeader>
                      <CardContent />
                    </Card>
                  </Link>
                ))}
              </div>
            </section>
          ))}

          <section className="flex flex-col gap-3">
            <h2 className="text-xs font-medium text-muted-foreground">
              规划中
            </h2>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {PLANNED_CARDS.map((card) => (
                <Card key={card.title} className="h-full border-dashed opacity-75">
                  <CardHeader>
                    <CardTitle className="flex items-center gap-2 text-sm">
                      <card.icon className="size-4 text-muted-foreground" />
                      {card.title}
                      <Badge variant="secondary" className="ml-auto">
                        {card.eta}
                      </Badge>
                    </CardTitle>
                    <CardDescription>{card.description}</CardDescription>
                  </CardHeader>
                  <CardContent />
                </Card>
              ))}
            </div>
          </section>
        </>
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
        <p className="py-12 text-center text-xs text-muted-foreground">
          没有找到与「{query}」相关的内容
        </p>
      )}
      {groups?.map((group) => (
        <section key={group.kind} className="flex flex-col gap-2">
          <h2 className="text-xs font-medium text-muted-foreground">
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
                  <span className="shrink-0 text-sm font-medium">{item.title}</span>
                  <span className="truncate text-xs text-muted-foreground">
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
