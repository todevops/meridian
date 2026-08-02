"use client"

// 全局左侧导航（F-090 六组结构）：总览 + 五个折叠组，当前路由高亮；
// 组折叠状态记忆于 localStorage；发现池入口轮询待处理数徽标（60s）；
// 菜单项按 GET /auth/me 权限点过滤；底部为当前用户与退出登录。
// 登录页不渲染侧边栏。

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"
import { usePathname, useRouter } from "next/navigation"
import {
  ChevronRight as ChevronRightIcon,
  LogOut as LogOutIcon,
  UserRound as UserRoundIcon,
} from "lucide-react"

import { cn } from "@workspace/ui/lib/utils"
import {
  getCurrentUser,
  listAlerts,
  listDiscoveryPool,
  logout,
  type CurrentUser,
} from "@/lib/api"
import {
  isNavItemActive,
  visibleNavGroups,
  OVERVIEW_ITEM,
  type NavItemDef,
} from "@/lib/nav"

/** 组折叠状态的 localStorage 键，值为折叠组 key 数组 */
const COLLAPSED_STORAGE_KEY = "cmdb.nav.collapsed"

/** 发现池待处理数轮询间隔（毫秒） */
const POOL_BADGE_INTERVAL = 60_000

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
    "flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm transition-colors",
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

  useEffect(() => {
    if (pathname === "/login") return
    // 会话失效时 api 客户端会自动跳登录页，这里静默失败即可
    getCurrentUser()
      .then(setUser)
      .catch(() => {})
  }, [pathname])

  // 挂载后读取折叠状态（避免 SSR 水合不一致）
  useEffect(() => {
    setCollapsed(readCollapsedKeys())
  }, [])

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

  // 发现池待处理数与未确认告警数徽标：登录后 60s 轮询
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

  const renderBadge = (item: NavItemDef) => {
    const count =
      item.badge === "pool-pending"
        ? poolPending
        : item.badge === "alerts-unacked"
          ? alertsUnacked
          : null
    if (count === null || count <= 0) return null
    return (
      <span className="ml-auto rounded-full bg-amber-500/15 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:text-amber-400">
        {count > 99 ? "99+" : count}
      </span>
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
    <aside className="sticky top-0 flex h-svh w-56 shrink-0 flex-col border-r bg-sidebar">
      <div className="flex h-14 items-center border-b px-4">
        <Link href="/" className="text-sm font-semibold tracking-tight">
          CMDB 配置管理中心
        </Link>
      </div>
      <nav className="flex flex-1 flex-col gap-1 overflow-y-auto p-3">
        <Link
          href={OVERVIEW_ITEM.href}
          aria-current={
            isNavItemActive(pathname, OVERVIEW_ITEM) ? "page" : undefined
          }
          className={navLinkClass(isNavItemActive(pathname, OVERVIEW_ITEM))}
        >
          <OVERVIEW_ITEM.icon className="size-4 shrink-0" />
          {OVERVIEW_ITEM.label}
        </Link>
        {groups.map((group) => {
          const isCollapsed = collapsed.includes(group.key)
          const groupActive = group.items.some((item) =>
            isNavItemActive(pathname, item)
          )
          return (
            <div key={group.key} className="flex flex-col gap-1">
              <button
                type="button"
                onClick={() => toggleGroup(group.key)}
                aria-expanded={!isCollapsed}
                className={cn(
                  "mt-2 flex items-center gap-1.5 rounded-md px-3 py-1 text-sm font-medium transition-colors hover:text-foreground",
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
              {!isCollapsed &&
                group.items.map((item) => (
                  <Link
                    key={item.href}
                    href={item.href}
                    aria-current={
                      isNavItemActive(pathname, item) ? "page" : undefined
                    }
                    className={cn(
                      navLinkClass(isNavItemActive(pathname, item)),
                      "pl-4"
                    )}
                  >
                    <item.icon className="size-4 shrink-0" />
                    <span className="truncate">{item.label}</span>
                    {renderBadge(item)}
                  </Link>
                ))}
            </div>
          )
        })}
      </nav>
      {user && (
        <div className="flex items-center gap-2 border-t p-3">
          <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted">
            <UserRoundIcon className="size-4 text-muted-foreground" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium">
              {user.display_name}
            </div>
            <div className="truncate text-xs text-muted-foreground">
              {user.roles.join(" / ")}
            </div>
          </div>
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
