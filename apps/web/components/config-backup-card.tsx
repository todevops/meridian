"use client"

// 配置备份元数据卡：展示 Oxidized webhook 回写到 CI 属性上的备份元数据。
// 仅展示元数据（时间/次数/来源），配置原文不入库，统一去 Oxidized 查看。

import { Archive as ArchiveIcon } from "lucide-react"

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import type { CI } from "@/lib/api"
import { attrText, formatDateTime } from "@/lib/format"

/** 时间类属性：优先按 ISO 格式化，非法值回退原文 */
function timeAttr(value: unknown): string {
  const text = attrText(value)
  if (text === "—") return text
  const formatted = formatDateTime(String(value))
  return formatted === "—" ? text : formatted
}

export function ConfigBackupCard({ ci }: { ci: CI }) {
  const attrs = ci.attributes
  // 四个元数据属性任一存在即视为已接入配置备份
  const hasData = [
    attrs.last_backup_at,
    attrs.backup_count,
    attrs.last_change_at,
    attrs.config_source,
  ].some((value) => attrText(value) !== "—")

  if (!hasData) {
    return (
      <Card className="border-dashed">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-xs">
            <ArchiveIcon className="size-4" /> 配置备份
          </CardTitle>
          <CardDescription>备份状态、最近备份时间与配置变更</CardDescription>
        </CardHeader>
        <CardContent>
          <p className="text-xs text-muted-foreground">
            未接入配置备份（备份原文不入库，接入后经 Oxidized webhook 回写元数据）。
          </p>
        </CardContent>
      </Card>
    )
  }

  const rows = [
    { label: "最近备份", value: timeAttr(attrs.last_backup_at) },
    { label: "备份次数", value: attrText(attrs.backup_count) },
    { label: "最近变更", value: timeAttr(attrs.last_change_at) },
    { label: "备份来源", value: attrText(attrs.config_source) },
  ]

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-xs">
          <ArchiveIcon className="size-4" /> 配置备份
        </CardTitle>
        <CardDescription>备份状态、最近备份时间与配置变更</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <dl className="flex flex-col gap-2 rounded-lg border p-3">
          {rows.map((row) => (
            <div
              key={row.label}
              className="flex items-baseline justify-between gap-4 text-xs"
            >
              <dt className="shrink-0 text-muted-foreground">{row.label}</dt>
              <dd className="min-w-0 text-right break-all">{row.value}</dd>
            </div>
          ))}
        </dl>
        <p className="text-xs text-muted-foreground">
          配置原文不入库，见 Oxidized。
        </p>
      </CardContent>
    </Card>
  )
}
