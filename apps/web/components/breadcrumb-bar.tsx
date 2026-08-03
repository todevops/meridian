"use client"

// 页面顶部面包屑：由当前路径反查导航分组，展示「组 / 页」；登录页不渲染。

import Link from "next/link"
import { usePathname } from "next/navigation"
import { ChevronRight as ChevronRightIcon } from "lucide-react"

import { breadcrumbFor } from "@/lib/nav"

export function BreadcrumbBar() {
  const pathname = usePathname()
  if (pathname === "/login" || pathname === "/") return null
  const crumb = breadcrumbFor(pathname)
  if (!crumb) return null

  return (
    <div className="flex h-11 items-center gap-1.5 border-b px-6 text-[13px] text-muted-foreground">
      <Link
        href="/"
        className="transition-colors hover:text-foreground"
      >
        总览
      </Link>
      {crumb.group && (
        <>
          <ChevronRightIcon className="size-3.5" />
          <span>{crumb.group}</span>
        </>
      )}
      <ChevronRightIcon className="size-3.5" />
      <span className="font-medium text-foreground">{crumb.page}</span>
    </div>
  )
}
