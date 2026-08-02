"use client"

// n9e 嵌入面板：上区监控仪表盘 iframe，下区当前告警列表。
// 监控时序与告警不入门户主库，实时经服务端代理接口拉取；
// n9e 侧不可达或接口报错时仅本面板降级为占位，不影响所在页面其余内容。

import { useCallback, useEffect, useState } from "react"
import {
  ExternalLink as ExternalLinkIcon,
  MonitorX as MonitorXIcon,
} from "lucide-react"

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
import {
  getN9EDashboardURL,
  listN9EAlerts,
  type N9EAlertItem,
} from "@/lib/api"
import { formatDateTime } from "@/lib/format"

/** 告警级别徽标样式（n9e severity 常见为 1/2/3 数字或 critical/warning/info 文本，做兼容映射） */
const SEVERITY_STYLES: Record<string, { label: string; className: string }> = {
  "1": { label: "严重", className: "bg-red-500/15 text-red-700 dark:text-red-400" },
  critical: { label: "严重", className: "bg-red-500/15 text-red-700 dark:text-red-400" },
  "2": { label: "警告", className: "bg-amber-500/15 text-amber-700 dark:text-amber-400" },
  warning: { label: "警告", className: "bg-amber-500/15 text-amber-700 dark:text-amber-400" },
  "3": { label: "提示", className: "bg-blue-500/15 text-blue-700 dark:text-blue-400" },
  info: { label: "提示", className: "bg-blue-500/15 text-blue-700 dark:text-blue-400" },
  notice: { label: "提示", className: "bg-blue-500/15 text-blue-700 dark:text-blue-400" },
}

function SeverityBadge({ severity }: { severity?: string | number }) {
  // n9e severity 实际数据可能是数字（1/2/3），必须先转字符串再取小写
  const key = String(severity ?? "").toLowerCase()
  const style = SEVERITY_STYLES[key] ?? {
    label: severity || "未知",
    className: "bg-muted text-muted-foreground",
  }
  return <Badge className={style.className}>{style.label}</Badge>
}

/** first_trigger_time 兼容 unix 秒/毫秒/数字字符串/ISO，统一交给格式化器 */
function triggerTimeText(value: number | string | undefined): string {
  if (value === undefined || value === "") return "—"
  const asNumber = typeof value === "number" ? value : Number(value)
  if (!Number.isNaN(asNumber)) {
    const ms = asNumber > 1e12 ? asNumber : asNumber * 1000
    return formatDateTime(new Date(ms).toISOString())
  }
  return formatDateTime(String(value))
}

interface N9EPanelProps {
  /** n9e target 标识（主机取 attributes.ident，网络设备取 name）；为空时展示未配置占位 */
  ident: string
}

export function N9EPanel({ ident }: N9EPanelProps) {
  const [dashboardURL, setDashboardURL] = useState<string | null>(null)
  const [dashboardError, setDashboardError] = useState<string | null>(null)
  const [iframeFailed, setIframeFailed] = useState(false)
  const [alerts, setAlerts] = useState<N9EAlertItem[] | null>(null)
  const [alertsError, setAlertsError] = useState<string | null>(null)

  const load = useCallback(async (target: string) => {
    setDashboardURL(null)
    setDashboardError(null)
    setIframeFailed(false)
    setAlerts(null)
    setAlertsError(null)
    // 两个接口独立降级：任一失败不影响另一区展示
    const [dashResult, alertsResult] = await Promise.allSettled([
      getN9EDashboardURL(target),
      listN9EAlerts(target),
    ])
    if (dashResult.status === "fulfilled") {
      setDashboardURL(dashResult.value.url || null)
    } else {
      setDashboardError("监控视图加载失败")
    }
    if (alertsResult.status === "fulfilled") {
      setAlerts(alertsResult.value ?? [])
    } else {
      setAlertsError("告警列表加载失败，n9e 可能不可达")
    }
  }, [])

  useEffect(() => {
    if (ident) void load(ident)
  }, [ident, load])

  const showDashboardPlaceholder =
    !ident || dashboardError !== null || !dashboardURL || iframeFailed

  return (
    <Card>
      <CardHeader>
        <CardTitle>n9e 监控</CardTitle>
        <CardDescription>
          监控视图与当前告警实时取自 n9e，时序数据不入库
          {ident ? `（ident：${ident}）` : ""}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        {/* 上区：监控仪表盘 */}
        <section className="flex flex-col gap-2">
          {showDashboardPlaceholder ? (
            <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed py-10">
              <MonitorXIcon className="size-6 text-muted-foreground" />
              <p className="text-xs text-muted-foreground">
                {!ident
                  ? "未配置监控视图（缺少 ident）"
                  : (dashboardError ?? "未配置监控视图")}
              </p>
              <div className="flex items-center gap-3">
                {dashboardURL ? (
                  <a
                    href={dashboardURL}
                    target="_blank"
                    rel="noreferrer"
                    className="flex items-center gap-1 text-xs text-primary hover:underline"
                  >
                    <ExternalLinkIcon className="size-3.5" /> 在 n9e 中打开
                  </a>
                ) : null}
                {ident && dashboardError ? (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => void load(ident)}
                  >
                    重试
                  </Button>
                ) : null}
              </div>
            </div>
          ) : (
            <div className="flex flex-col gap-2">
              <iframe
                src={dashboardURL}
                title={`n9e 监控视图 ${ident}`}
                className="h-80 w-full rounded-lg border bg-background"
                sandbox="allow-scripts allow-same-origin allow-forms"
                onError={() => setIframeFailed(true)}
              />
              <div>
                <a
                  href={dashboardURL}
                  target="_blank"
                  rel="noreferrer"
                  className="flex w-fit items-center gap-1 text-xs text-primary hover:underline"
                >
                  <ExternalLinkIcon className="size-3.5" /> 在 n9e 中打开
                </a>
              </div>
            </div>
          )}
        </section>

        {/* 下区：当前告警 */}
        <section className="flex flex-col gap-2">
          <h3 className="text-xs font-semibold">
            当前告警{alerts && alerts.length > 0 ? `（${alerts.length}）` : ""}
          </h3>
          {!ident ? (
            <p className="text-xs text-muted-foreground">无告警</p>
          ) : alertsError ? (
            <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed py-8">
              <p className="text-xs text-destructive">{alertsError}</p>
              <Button
                variant="outline"
                size="sm"
                onClick={() => void load(ident)}
              >
                重试
              </Button>
            </div>
          ) : alerts === null ? (
            <div className="flex flex-col gap-2">
              {Array.from({ length: 3 }).map((_, index) => (
                <Skeleton key={index} className="h-9 w-full" />
              ))}
            </div>
          ) : alerts.length === 0 ? (
            <p className="text-xs text-muted-foreground">无告警</p>
          ) : (
            <ul className="flex flex-col gap-2">
              {alerts.map((alert, index) => (
                <li
                  key={index}
                  className="flex items-center justify-between gap-3 rounded-lg border px-3 py-2 text-xs"
                >
                  <span className="flex min-w-0 items-center gap-2">
                    <SeverityBadge severity={alert.severity} />
                    <span className="min-w-0 truncate font-medium">
                      {alert.trigger || "未命名规则"}
                    </span>
                  </span>
                  <span className="shrink-0 text-muted-foreground">
                    {triggerTimeText(alert.first_trigger_time)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>
      </CardContent>
    </Card>
  )
}
