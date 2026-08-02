"use client"

// 规划中模块的统一占位页：图标 + 说明 + 预计上线迭代标注

import type { LucideIcon } from "lucide-react"
import { Construction as ConstructionIcon } from "lucide-react"

import { Badge } from "@/components/ui/badge"

interface PlaceholderPageProps {
  title: string
  description: string
  /** 预计上线的迭代标注，如「迭代 2B 上线」 */
  eta: string
  icon?: LucideIcon
}

export function PlaceholderPage({
  title,
  description,
  eta,
  icon: Icon = ConstructionIcon,
}: PlaceholderPageProps) {
  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <header>
        <h1 className="flex items-center gap-2 text-xl font-semibold">
          {title}
          <Badge variant="secondary">{eta}</Badge>
        </h1>
        <p className="mt-1 text-xs text-muted-foreground">{description}</p>
      </header>
      <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-16 text-muted-foreground">
        <Icon className="size-8" />
        <p className="text-xs">该模块正在建设中，{eta}</p>
      </div>
    </div>
  )
}
