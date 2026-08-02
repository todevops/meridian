"use client"

// 全局左侧导航：各功能域入口，当前路由高亮；底部为当前用户与退出登录。
// 登录页不渲染侧边栏；系统管理入口按权限点控制可见性。

import { useEffect, useState } from "react"
import Link from "next/link"
import { usePathname, useRouter } from "next/navigation"
import {
  Boxes as BoxesIcon,
  Building2 as Building2Icon,
  LayoutDashboard as LayoutDashboardIcon,
  LogOut as LogOutIcon,
  Network as NetworkIcon,
  Radar as RadarIcon,
  Server as ServerIcon,
  Settings as SettingsIcon,
  UserRound as UserRoundIcon,
  UsersRound as UsersRoundIcon,
} from "lucide-react"

import { cn } from "@workspace/ui/lib/utils"
import { getCurrentUser, logout, type CurrentUser } from "@/lib/api"

const NAV_ITEMS = [
  { href: "/", label: "总览", icon: LayoutDashboardIcon, exact: true },
  { href: "/models", label: "模型管理", icon: BoxesIcon, exact: false },
  { href: "/hosts", label: "主机", icon: ServerIcon, exact: false },
  { href: "/pool", label: "发现池", icon: RadarIcon, exact: false },
  { href: "/ipam", label: "IPAM", icon: NetworkIcon, exact: false },
  { href: "/dcim", label: "机柜", icon: Building2Icon, exact: false },
] as const

// 系统管理菜单：permission 为所需权限点，无权限不展示
const ADMIN_ITEMS = [
  {
    href: "/settings/users",
    label: "用户管理",
    icon: UsersRoundIcon,
    permission: "user:manage",
  },
  {
    href: "/settings/roles",
    label: "角色管理",
    icon: SettingsIcon,
    permission: "role:manage",
  },
] as const

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

  useEffect(() => {
    if (pathname === "/login") return
    // 会话失效时 api 客户端会自动跳登录页，这里静默失败即可
    getCurrentUser()
      .then(setUser)
      .catch(() => {})
  }, [pathname])

  // 登录页不渲染侧边栏
  if (pathname === "/login") return null

  const isActive = (href: string, exact?: boolean) =>
    exact
      ? pathname === href
      : pathname === href || pathname.startsWith(`${href}/`)

  const adminItems = ADMIN_ITEMS.filter((item) =>
    user?.permissions.includes(item.permission)
  )

  async function onLogout() {
    try {
      await logout()
    } catch {
      // 登出失败（如会话已失效）也照常回登录页
    }
    router.replace("/login")
  }

  return (
    <aside className="sticky top-0 flex h-svh w-52 shrink-0 flex-col border-r bg-sidebar">
      <div className="flex h-14 items-center border-b px-4">
        <Link href="/" className="text-sm font-semibold tracking-tight">
          CMDB 配置管理中心
        </Link>
      </div>
      <nav className="flex flex-1 flex-col gap-1 overflow-y-auto p-3">
        {NAV_ITEMS.map((item) => (
          <Link
            key={item.href}
            href={item.href}
            aria-current={isActive(item.href, item.exact) ? "page" : undefined}
            className={navLinkClass(isActive(item.href, item.exact))}
          >
            <item.icon className="size-4 shrink-0" />
            {item.label}
          </Link>
        ))}
        {adminItems.length > 0 && (
          <>
            <div className="mt-3 mb-1 px-3 text-xs font-medium text-muted-foreground/70">
              系统管理
            </div>
            {adminItems.map((item) => (
              <Link
                key={item.href}
                href={item.href}
                aria-current={isActive(item.href) ? "page" : undefined}
                className={navLinkClass(isActive(item.href))}
              >
                <item.icon className="size-4 shrink-0" />
                {item.label}
              </Link>
            ))}
          </>
        )}
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
