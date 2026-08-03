"use client"

// F-027 两级业务树（左侧栏）：biz_line → biz_app，可折叠；
// 业务线节点显示应用数/主机数汇总 Badge，应用节点显示等级色点 + 负责人；
// 未归属业务线的应用居底。「概览」入口回到分组列表视图。

import {
  AppWindow as AppWindowIcon,
  ChevronDown as ChevronDownIcon,
  ChevronRight as ChevronRightIcon,
  LayoutGrid as LayoutGridIcon,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import type { AppTreeApp, AppTreeLine } from "@/lib/api"
import { attrText } from "@/lib/format"

/** 未归属业务线分组 key */
export const UNASSIGNED_KEY = "__unassigned__"

/** 等级色点样式：L1/critical 红、L2/high 黄、其余灰 */
const LEVEL_DOT_STYLES: Record<string, string> = {
  critical: "bg-red-500",
  l1: "bg-red-500",
  high: "bg-amber-500",
  l2: "bg-amber-500",
}

function LevelDot({ level }: { level: string }) {
  const className =
    LEVEL_DOT_STYLES[level.toLowerCase()] ?? "bg-muted-foreground/40"
  return (
    <span
      className={`size-2 shrink-0 rounded-full ${className}`}
      title={level === "—" ? "未评级" : `等级 ${level}`}
    />
  )
}

interface ApplicationTreeProps {
  lines: AppTreeLine[]
  unassigned: AppTreeApp[]
  /** 已折叠的分组 key 集合 */
  collapsed: Set<string>
  onToggle: (key: string) => void
  /** 当前选中应用 id；为空表示概览模式 */
  selectedAppId: string | null
  onSelectApp: (appId: string) => void
  onSelectOverview: () => void
}

export function ApplicationTree({
  lines,
  unassigned,
  collapsed,
  onToggle,
  selectedAppId,
  onSelectApp,
  onSelectOverview,
}: ApplicationTreeProps) {
  const renderApp = (app: AppTreeApp) => {
    const active = app.id === selectedAppId
    return (
      <button
        key={app.id}
        type="button"
        data-testid={`app-tree-node-${app.id}`}
        onClick={() => onSelectApp(app.id)}
        className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs transition-colors ${
          active
            ? "bg-primary/10 font-medium text-primary"
            : "hover:bg-muted/60"
        }`}
      >
        <LevelDot level={attrText(app.level)} />
        <span className="min-w-0 flex-1 truncate">
          {attrText(app.name) === "—" ? attrText(app.code) : attrText(app.name)}
        </span>
        <span className="shrink-0 text-[10px] text-muted-foreground">
          {attrText(app.owner)}
        </span>
      </button>
    )
  }

  const renderGroup = (
    key: string,
    name: string,
    apps: AppTreeApp[],
    appCount: number,
    hostCount: number | null,
    dashed = false,
  ) => {
    const isCollapsed = collapsed.has(key)
    return (
      <div
        key={key}
        className={`rounded-lg border ${dashed ? "border-dashed" : ""}`}
      >
        <button
          type="button"
          data-testid={`app-tree-line-${key}`}
          onClick={() => onToggle(key)}
          className="flex w-full items-center gap-1.5 px-2 py-2 text-left"
        >
          {isCollapsed ? (
            <ChevronRightIcon className="size-3.5 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronDownIcon className="size-3.5 shrink-0 text-muted-foreground" />
          )}
          <span className="min-w-0 flex-1 truncate text-xs font-medium">
            {name}
          </span>
          <Badge variant="secondary" className="shrink-0">
            应用 {appCount}
          </Badge>
          {hostCount !== null ? (
            <Badge variant="outline" className="shrink-0">
              主机 {hostCount}
            </Badge>
          ) : null}
        </button>
        {isCollapsed ? null : (
          <div className="flex flex-col gap-0.5 px-2 pb-2">
            {apps.length === 0 ? (
              <p className="px-2 py-1 text-[10px] text-muted-foreground">
                该业务线下暂无应用
              </p>
            ) : (
              apps.map(renderApp)
            )}
          </div>
        )}
      </div>
    )
  }

  return (
    <aside className="flex w-72 shrink-0 flex-col gap-2">
      <button
        type="button"
        data-testid="app-tree-overview"
        onClick={onSelectOverview}
        className={`flex items-center gap-2 rounded-lg border px-3 py-2 text-left text-xs font-medium transition-colors ${
          selectedAppId === null
            ? "border-primary/50 bg-primary/10 text-primary"
            : "hover:bg-muted/60"
        }`}
      >
        <LayoutGridIcon className="size-4 shrink-0" /> 应用总览
      </button>

      {lines.map((line) =>
        renderGroup(
          line.id,
          attrText(line.name) === "—" ? attrText(line.code) : attrText(line.name),
          line.apps,
          line.app_count,
          line.host_count,
        ),
      )}

      {/* 未归属业务线的应用居底 */}
      {unassigned.length > 0
        ? renderGroup(
            UNASSIGNED_KEY,
            "未归属业务线",
            unassigned,
            unassigned.length,
            null,
            true,
          )
        : null}

      {lines.length === 0 && unassigned.length === 0 ? (
        <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed px-3 py-8">
          <AppWindowIcon className="size-5 text-muted-foreground" />
          <p className="text-center text-xs text-muted-foreground">
            暂无业务线与应用数据
          </p>
        </div>
      ) : null}
    </aside>
  )
}
