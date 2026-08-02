"use client"

// 告警事件页：黑设备等系统告警的列表与确认闭环（AC-F043-01）。
// Tab：未确认 / 已确认 / 全部；未确认行可一键确认（ack）。

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { BellRing as BellRingIcon, Check as CheckIcon } from "lucide-react"

import {
  listAlerts,
  ackAlert,
  ApiError,
  type AlertEvent,
} from "@/lib/api"
import { formatDateTime } from "@/lib/format"
import { Badge } from "@/components/ui/badge"
import { Button } from "@workspace/ui/components/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"

type TabKey = "unacked" | "acked" | "all"

const TABS: { key: TabKey; label: string }[] = [
  { key: "unacked", label: "未确认" },
  { key: "acked", label: "已确认" },
  { key: "all", label: "全部" },
]

const LEVEL_STYLES: Record<string, { label: string; className: string }> = {
  warning: { label: "警告", className: "bg-amber-500/15 text-amber-700 dark:text-amber-400" },
  critical: { label: "严重", className: "bg-red-500/15 text-red-700 dark:text-red-400" },
  info: { label: "提示", className: "bg-blue-500/15 text-blue-700 dark:text-blue-400" },
}

function LevelBadge({ level }: { level: string }) {
  const s = LEVEL_STYLES[level] ?? { label: level, className: "bg-muted text-muted-foreground" }
  return <Badge className={s.className}>{s.label}</Badge>
}

export default function AlertsPage() {
  const [tab, setTab] = useState<TabKey>("unacked")
  const [items, setItems] = useState<AlertEvent[] | null>(null)
  const [total, setTotal] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [acking, setAcking] = useState<string | null>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      const params =
        tab === "all"
          ? { page: 1, page_size: 50 }
          : { acknowledged: tab === "acked", page: 1, page_size: 50 }
      const res = await listAlerts(params)
      setItems(res.items)
      setTotal(res.total)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "加载告警事件失败")
      setItems([])
    }
  }, [tab])

  useEffect(() => {
    setItems(null)
    void load()
  }, [load])

  const onAck = useCallback(
    async (id: string) => {
      setAcking(id)
      try {
        await ackAlert(id)
        await load()
      } catch (e) {
        setError(e instanceof ApiError ? e.message : "确认告警失败")
      } finally {
        setAcking(null)
      }
    },
    [load],
  )

  const emptyText = useMemo(() => {
    if (tab === "unacked") return "没有未确认的告警，一切正常"
    if (tab === "acked") return "暂无已确认的告警"
    return "暂无告警事件"
  }, [tab])

  return (
    <div className="flex flex-col gap-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">告警事件</h1>
          <p className="text-xs text-muted-foreground">
            黑设备与系统侧告警事件；确认后从「未确认」列表移除（事件保留可审计）
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={() => void load()}>
          刷新
        </Button>
      </div>

      <div className="flex items-center gap-2">
        {TABS.map((t) => (
          <Button
            key={t.key}
            variant={tab === t.key ? "default" : "outline"}
            size="sm"
            onClick={() => setTab(t.key)}
          >
            {t.label}
          </Button>
        ))}
        <span className="ml-2 text-xs text-muted-foreground">共 {total} 条</span>
      </div>

      {error && (
        <Card className="border-destructive/50">
          <CardContent className="flex items-center justify-between gap-4 py-3 text-xs">
            <span className="text-destructive">{error}</span>
            <Button variant="outline" size="sm" onClick={() => void load()}>
              重试
            </Button>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">事件列表</CardTitle>
          <CardDescription>按时间倒序，最多展示最近 50 条</CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {items === null ? (
            <div className="flex flex-col gap-2 p-6">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : items.length === 0 ? (
            <div className="flex flex-col items-center gap-2 py-16">
              <BellRingIcon className="size-8 text-muted-foreground" />
              <p className="text-xs text-muted-foreground">{emptyText}</p>
            </div>
          ) : (
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b text-left text-muted-foreground">
                  <th className="px-4 py-2.5 font-medium">级别</th>
                  <th className="px-4 py-2.5 font-medium">标题</th>
                  <th className="px-4 py-2.5 font-medium">来源</th>
                  <th className="px-4 py-2.5 font-medium">详情</th>
                  <th className="px-4 py-2.5 font-medium">时间</th>
                  <th className="px-4 py-2.5 font-medium">状态</th>
                  <th className="px-4 py-2.5 font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {items.map((a) => (
                  <tr key={a.id} className="border-b last:border-0 hover:bg-muted/40">
                    <td className="px-4 py-2.5">
                      <LevelBadge level={a.level} />
                    </td>
                    <td className="px-4 py-2.5 font-medium">{a.title}</td>
                    <td className="px-4 py-2.5 text-muted-foreground">{a.source}</td>
                    <td
                      className="max-w-[320px] truncate px-4 py-2.5 text-muted-foreground"
                      title={a.detail ?? ""}
                    >
                      {a.detail ?? "—"}
                    </td>
                    <td className="px-4 py-2.5 text-muted-foreground">
                      {formatDateTime(a.created_at)}
                    </td>
                    <td className="px-4 py-2.5">
                      {a.acknowledged ? (
                        <Badge className="bg-muted text-muted-foreground">已确认</Badge>
                      ) : (
                        <Badge className="bg-amber-500/15 text-amber-700 dark:text-amber-400">
                          未确认
                        </Badge>
                      )}
                    </td>
                    <td className="px-4 py-2.5">
                      {!a.acknowledged && (
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={acking === a.id}
                          onClick={() => void onAck(a.id)}
                        >
                          <CheckIcon className="mr-1 size-3.5" />
                          {acking === a.id ? "确认中…" : "确认"}
                        </Button>
                      )}
                      {a.ci_id && (
                        <Link
                          href={`/hosts/${a.ci_id}`}
                          className="ml-2 text-primary underline-offset-2 hover:underline"
                        >
                          查看 CI
                        </Link>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
