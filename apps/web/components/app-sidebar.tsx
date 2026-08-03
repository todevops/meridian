"use client"

// 全局左侧导航（F-090 六组结构）：总览组顶部独立入口 + 五个折叠组，当前路由高亮；
// 组折叠状态记忆于 localStorage；发现池待处理数、未确认告警数、整改待办数三路徽标轮询（60s）；
// 菜单项按 GET /auth/me 权限点过滤；底部为当前用户与退出登录。
// 支持拖拽右缘调整宽度（176–384px，localStorage 记忆），并可整体收纳为图标栏：
// 收纳后仅显示图标（悬停 title 提示、徽标退化为圆点），顶部标题替换为方形 Logo；
// 展开/收纳由右缘中点的悬浮圆形按钮统一切换（位置不随状态变化），点击 Logo 也可恢复。
// 登录页不渲染侧边栏。

import { useCallback, useEffect, useState, type PointerEvent as ReactPointerEvent } from "react"
import Link from "next/link"
import { usePathname, useRouter } from "next/navigation"
import {
  ChevronRight as ChevronRightIcon,
  ChevronsLeft as ChevronsLeftIcon,
  ChevronsRight as ChevronsRightIcon,
  LogOut as LogOutIcon,
  UserRound as UserRoundIcon,
} from "lucide-react"

import { cn } from "@workspace/ui/lib/utils"
import {
  getCurrentUser,
  listAlerts,
  listDiscoveryPool,
  listGovernanceTodos,
  logout,
  type CurrentUser,
} from "@/lib/api"
import {
  isNavItemActive,
  visibleNavGroups,
  TOP_ITEMS,
  type NavItemDef,
} from "@/lib/nav"

/** 组折叠状态的 localStorage 键，值为折叠组 key 数组 */
const COLLAPSED_STORAGE_KEY = "cmdb.nav.collapsed"

/** 侧边栏宽度与收纳状态的 localStorage 键 */
const WIDTH_STORAGE_KEY = "cmdb.nav.width"
const RAIL_STORAGE_KEY = "cmdb.nav.rail"

/** 侧边栏宽度（px）：默认 / 最小 / 最大 / 收纳态固定宽度 */
const DEFAULT_WIDTH = 224
const MIN_WIDTH = 176
const MAX_WIDTH = 384
const RAIL_WIDTH = 56

/** 发现池待处理数轮询间隔（毫秒） */
const POOL_BADGE_INTERVAL = 60_000

function clampWidth(w: number): number {
  return Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, Math.round(w)))
}

/** 方形品牌矢量标：圆角方块内 2×2 网格，收纳态替代文字标题 */
function LogoMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={className} aria-hidden="true">
      <rect x="1.5" y="1.5" width="21" height="21" rx="5" className="fill-primary" />
      <rect x="6" y="6" width="5" height="5" rx="1.2" className="fill-primary-foreground" />
      <rect x="13" y="6" width="5" height="5" rx="1.2" className="fill-primary-foreground/60" />
      <rect x="6" y="13" width="5" height="5" rx="1.2" className="fill-primary-foreground/60" />
      <rect x="13" y="13" width="5" height="5" rx="1.2" className="fill-primary-foreground" />
    </svg>
  )
}

function readCollapsedKeys(): string[] {
  if (typeof window === "undefined") return []
  try {
    const raw = window.localStorage.getItem(COLLAPSED_STORAGE_KEY)
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    return Array.isArray(parsed)
      ? parsed.filter((k): k is string => typeof k === "string")
      : []
  } catch {
    return []
  }
}

function navLinkClass(active: boolean) {
  return cn(
    "flex items-center gap-2.5 rounded-lg px-3 py-2 text-[13px] transition-colors",
    active
      ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
      : "text-muted-foreground hover:bg-sidebar-accent/60 hover:text-foreground"
  )
}

export function AppSidebar() {
  const pathname = usePathname()
  const router = useRouter()
  const [user, setUser] = useState<CurrentUser | null>(null)
  const [collapsed, setCollapsed] = useState<string[]>([])
  const [poolPending, setPoolPending] = useState<number | null>(null)
  const [alertsUnacked, setAlertsUnacked] = useState<number | null>(null)
  const [governanceOpen, setGovernanceOpen] = useState<number | null>(null)
  const [width, setWidth] = useState(DEFAULT_WIDTH)
  const [rail, setRail] = useState(false)
  const [dragging, setDragging] = useState(false)

  useEffect(() => {
    if (pathname === "/login") return
    // 会话失效时 api 客户端会自动跳登录页，这里静默失败即可
    getCurrentUser()
      .then(setUser)
      .catch(() => {})
  }, [pathname])

  // 挂载后读取折叠状态、宽度与收纳状态（避免 SSR 水合不一致）
  useEffect(() => {
    setCollapsed(readCollapsedKeys())
    try {
      const w = Number(window.localStorage.getItem(WIDTH_STORAGE_KEY))
      if (Number.isFinite(w) && w > 0) setWidth(clampWidth(w))
      setRail(window.localStorage.getItem(RAIL_STORAGE_KEY) === "1")
    } catch {
      // 存储不可用时使用默认值
    }
  }, [])

  const setRailMode = useCallback((v: boolean) => {
    setRail(v)
    try {
      window.localStorage.setItem(RAIL_STORAGE_KEY, v ? "1" : "0")
    } catch {
      // 存储不可用时仅影响记忆，不阻断交互
    }
  }, [])

  // 拖拽结束后持久化宽度
  useEffect(() => {
    if (dragging) return
    try {
      window.localStorage.setItem(WIDTH_STORAGE_KEY, String(width))
    } catch {
      // 存储不可用时仅影响记忆
    }
  }, [width, dragging])

  // 拖拽右缘手柄调宽：window 级监听，抬起时结束并解除文本选中禁用
  const onResizeStart = useCallback(
    (e: ReactPointerEvent<HTMLDivElement>) => {
      e.preventDefault()
      const startX = e.clientX
      const startWidth = width
      setDragging(true)
      document.body.style.userSelect = "none"
      const onMove = (ev: PointerEvent) => {
        setWidth(clampWidth(startWidth + ev.clientX - startX))
      }
      const onUp = () => {
        setDragging(false)
        document.body.style.userSelect = ""
        window.removeEventListener("pointermove", onMove)
      }
      window.addEventListener("pointermove", onMove)
      window.addEventListener("pointerup", onUp, { once: true })
    },
    [width]
  )

  const toggleGroup = useCallback((key: string) => {
    setCollapsed((prev) => {
      const next = prev.includes(key)
        ? prev.filter((k) => k !== key)
        : [...prev, key]
      try {
        window.localStorage.setItem(COLLAPSED_STORAGE_KEY, JSON.stringify(next))
      } catch {
        // 存储不可用时仅影响记忆，不阻断交互
      }
      return next
    })
  }, [])

  // 发现池待处理数、未确认告警数与整改待办数徽标：登录后 60s 轮询
  useEffect(() => {
    if (pathname === "/login" || !user) return
    let cancelled = false
    const poll = () => {
      listDiscoveryPool({ status: "pending", page: 1, page_size: 1 })
        .then((res) => {
          if (!cancelled) setPoolPending(res.total)
        })
        .catch(() => {
          if (!cancelled) setPoolPending(null)
        })
      listAlerts({ acknowledged: false, page: 1, page_size: 1 })
        .then((res) => {
          if (!cancelled) setAlertsUnacked(res.total)
        })
        .catch(() => {
          if (!cancelled) setAlertsUnacked(null)
        })
      listGovernanceTodos({ status: "open", page: 1, page_size: 1 })
        .then((res) => {
          if (!cancelled) setGovernanceOpen(res.total)
        })
        .catch(() => {
          if (!cancelled) setGovernanceOpen(null)
        })
    }
    poll()
    const timer = window.setInterval(poll, POOL_BADGE_INTERVAL)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [pathname, user])

  // 登录页不渲染侧边栏
  if (pathname === "/login") return null

  const groups = visibleNavGroups(user?.permissions)

  const badgeCount = (item: NavItemDef): number | null =>
    item.badge === "pool-pending"
      ? poolPending
      : item.badge === "alerts-unacked"
        ? alertsUnacked
        : item.badge === "governance-todos"
          ? governanceOpen
          : null

  const renderBadge = (item: NavItemDef) => {
    const count = badgeCount(item)
    if (count === null || count <= 0) return null
    return (
      <span className="ml-auto rounded-full bg-amber-500/15 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:text-amber-400">
        {count > 99 ? "99+" : count}
      </span>
    )
  }

  // 收纳态徽标退化为右上角圆点
  const renderBadgeDot = (item: NavItemDef) => {
    const count = badgeCount(item)
    if (count === null || count <= 0) return null
    return (
      <span className="absolute top-1 right-1 size-1.5 rounded-full bg-amber-500" />
    )
  }

  async function onLogout() {
    try {
      await logout()
    } catch {
      // 登出失败（如会话已失效）也照常回登录页
    }
    router.replace("/login")
  }

  return (
    <aside
      style={{ width: rail ? RAIL_WIDTH : width }}
      className={cn(
        "sticky top-0 flex h-svh shrink-0 flex-col border-r bg-sidebar",
        !dragging && "transition-[width] duration-200"
      )}
    >
      {/* 展开/收纳切换：悬浮在右缘中点，位置不随状态变化 */}
      <button
        type="button"
        onClick={() => setRailMode(!rail)}
        title={rail ? "展开导航" : "收起导航"}
        className="absolute top-1/2 -right-4 z-20 flex size-6 -translate-y-1/2 items-center justify-center rounded-full border bg-background text-muted-foreground shadow-sm transition-colors hover:text-foreground"
      >
        {rail ? (
          <ChevronsRightIcon className="size-3.5" />
        ) : (
          <ChevronsLeftIcon className="size-3.5" />
        )}
      </button>

      {/* 宽度拖拽手柄：仅展开态可用 */}
      {!rail && (
        <div
          role="separator"
          aria-orientation="vertical"
          aria-label="拖拽调整侧边栏宽度"
          onPointerDown={onResizeStart}
          className="absolute inset-y-0 right-0 z-10 w-1 cursor-col-resize transition-colors hover:bg-primary/30"
        />
      )}

      <div
        className={cn(
          "flex h-14 shrink-0 items-center border-b",
          rail ? "justify-center px-2" : "gap-2 px-4"
        )}
      >
        {rail ? (
          <button
            type="button"
            onClick={() => setRailMode(false)}
            title="展开导航"
            className="rounded-md transition-transform hover:scale-105"
          >
            <LogoMark className="size-7" />
          </button>
        ) : (
          <Link
            href="/"
            className="flex min-w-0 items-center gap-2 text-sm font-semibold tracking-tight"
          >
            <LogoMark className="size-5 shrink-0" />
            <span className="truncate">Meridian 配置管理中心</span>
          </Link>
        )}
      </div>

      <nav
        className={cn(
          "flex flex-1 flex-col gap-1 overflow-y-auto",
          rail ? "p-2" : "p-3"
        )}
      >
        {TOP_ITEMS.map((item) => (
          <Link
            key={item.href}
            href={item.href}
            title={rail ? item.label : undefined}
            aria-current={isNavItemActive(pathname, item) ? "page" : undefined}
            className={cn(
              navLinkClass(isNavItemActive(pathname, item)),
              rail && "justify-center px-0"
            )}
          >
            <item.icon className="size-4 shrink-0" />
            {!rail && item.label}
          </Link>
        ))}
        {groups.map((group) => {
          const isCollapsed = collapsed.includes(group.key)
          const groupActive = group.items.some((item) =>
            isNavItemActive(pathname, item)
          )
          return (
            <div key={group.key} className="flex flex-col gap-1">
              {rail ? (
                <div className="mx-2 my-1 border-t border-sidebar-border" />
              ) : (
                <button
                  type="button"
                  onClick={() => toggleGroup(group.key)}
                  aria-expanded={!isCollapsed}
                  className={cn(
                    "mt-2 flex items-center gap-1.5 rounded-md px-3 py-1 text-[13px] font-medium transition-colors hover:text-foreground",
                    groupActive ? "text-foreground" : "text-muted-foreground"
                  )}
                >
                  <ChevronRightIcon
                    className={cn(
                      "size-4 transition-transform",
                      !isCollapsed && "rotate-90"
                    )}
                  />
                  {group.label}
                </button>
              )}
              {(rail || !isCollapsed) &&
                group.items.map((item) => (
                  <Link
                    key={item.href}
                    href={item.href}
                    title={rail ? item.label : undefined}
                    aria-current={
                      isNavItemActive(pathname, item) ? "page" : undefined
                    }
                    className={cn(
                      navLinkClass(isNavItemActive(pathname, item)),
                      rail ? "relative justify-center px-0" : "pl-4"
                    )}
                  >
                    <item.icon className="size-4 shrink-0" />
                    {!rail && <span className="truncate">{item.label}</span>}
                    {rail ? renderBadgeDot(item) : renderBadge(item)}
                  </Link>
                ))}
            </div>
          )
        })}
      </nav>
      {user && (
        <div
          className={cn(
            "flex border-t",
            rail ? "flex-col items-center gap-2 p-2" : "items-center gap-2 p-3"
          )}
        >
          <div
            className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted"
            title={rail ? user.display_name : undefined}
          >
            <UserRoundIcon className="size-4 text-muted-foreground" />
          </div>
          {!rail && (
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-medium">
                {user.display_name}
              </div>
              <div className="truncate text-xs text-muted-foreground">
                {user.roles.join(" / ")}
              </div>
            </div>
          )}
          <button
            type="button"
            onClick={onLogout}
            title="退出登录"
            className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-sidebar-accent hover:text-foreground"
          >
            <LogOutIcon className="size-4" />
          </button>
        </div>
      )}
    </aside>
  )
}
