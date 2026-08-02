"use client"

// 机柜 U 位矩阵：自上而下 U 高→低竖排，同设备连续 U 位合并为一个色块；
// 空闲段点击打开挂载对话框，占用段提供卸载按钮（二次确认）

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { ArrowLeft as ArrowLeftIcon, Unplug as UnplugIcon } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@workspace/ui/components/button"
import { Skeleton } from "@/components/ui/skeleton"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { RackMountDialog } from "@/components/rack-mount-dialog"
import {
  ApiError,
  getCI,
  getRackUnits,
  unmountRackUnit,
  type CI,
  type RackUnit,
} from "@/lib/api"
import { pickAttr } from "@/lib/format"

/** 单个 U 位的渲染高度（px） */
const ROW_HEIGHT = 26

const RACK_NAME_CODES = ["name", "hostname", "code"]

/** 渲染段：连续同占用者的相邻 U 位合并；occupantId 为 null 表示空闲段 */
interface Segment {
  startU: number
  count: number
  occupantId: string | null
  occupantName: string | null
}

/** 由 units 明细合并出渲染段，返回顺序自上而下（U 高 → U 低） */
function buildSegments(units: RackUnit[], uTotal: number): Segment[] {
  const byU = new Map(units.map((unit) => [unit.u, unit]))
  const segments: Segment[] = []
  let u = 1
  while (u <= uTotal) {
    const unit = byU.get(u)
    const occupantId = unit?.occupant_ci_id ?? null
    const occupantName = unit?.occupant_name ?? null
    let end = u + 1
    while (end <= uTotal && (byU.get(end)?.occupant_ci_id ?? null) === occupantId) {
      end++
    }
    segments.push({ startU: u, count: end - u, occupantId, occupantName })
    u = end
  }
  return segments.reverse()
}

function segmentULabel(segment: Segment): string {
  const topU = segment.startU + segment.count - 1
  return segment.count === 1 ? `U${segment.startU}` : `U${topU}–U${segment.startU}`
}

export function RackDetail({ id }: { id: string }) {
  const [rack, setRack] = useState<CI | null>(null)
  const [units, setUnits] = useState<RackUnit[]>([])
  const [uTotal, setUTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [mountOpen, setMountOpen] = useState(false)
  const [mountTarget, setMountTarget] = useState<{ u: number; maxHeight: number }>({ u: 1, maxHeight: 1 })
  const [unmounting, setUnmounting] = useState<Segment | null>(null)
  const [unmountLoading, setUnmountLoading] = useState(false)
  const [unmountError, setUnmountError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      // 机柜 CI 与 U 位占用并行加载；CI 失败时仅缺失标题信息，不阻塞矩阵
      const [ciResult, unitsResult] = await Promise.allSettled([getCI(id), getRackUnits(id)])
      if (unitsResult.status === "rejected") {
        const err = unitsResult.reason
        setError(
          err instanceof ApiError
            ? err.status === 404
              ? "机柜不存在或已被删除"
              : err.message
            : "加载机柜 U 位失败",
        )
        return
      }
      const ci = ciResult.status === "fulfilled" ? ciResult.value : null
      setRack(ci)
      const data = unitsResult.value
      setUnits(data.units ?? [])
      // 总 U 数：接口为准，缺省回退机柜 u_capacity 属性，最后兜底 42U
      const capacity = Number(ci?.attributes?.u_capacity)
      setUTotal(data.u_total || (Number.isInteger(capacity) && capacity > 0 ? capacity : 42))
    } finally {
      setLoading(false)
    }
  }, [id])

  useEffect(() => {
    void load()
  }, [load])

  const segments = useMemo(() => buildSegments(units, uTotal), [units, uTotal])
  const usedU = useMemo(
    () => segments.filter((s) => s.occupantId !== null).reduce((sum, s) => sum + s.count, 0),
    [segments],
  )

  const onUnmountConfirm = useCallback(async () => {
    if (!unmounting?.occupantId) return
    setUnmountLoading(true)
    setUnmountError(null)
    try {
      await unmountRackUnit(id, unmounting.occupantId)
      setUnmounting(null)
      void load()
    } catch (err) {
      setUnmountError(err instanceof ApiError ? err.message : "卸载失败，请稍后重试")
    } finally {
      setUnmountLoading(false)
    }
  }, [unmounting, id, load])

  if (loading) {
    return (
      <div className="mx-auto flex w-full max-w-6xl flex-col gap-5 p-6">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-[560px] w-full max-w-md" />
      </div>
    )
  }

  if (error) {
    return (
      <div className="mx-auto flex w-full max-w-6xl flex-col gap-5 p-6">
        <Link
          href="/dcim"
          className="flex w-fit items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeftIcon className="size-4" /> 返回机柜列表
        </Link>
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-16">
          <p className="text-sm text-destructive">{error}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      </div>
    )
  }

  const rackName = rack ? pickAttr(rack.attributes, [...RACK_NAME_CODES]) : "—"

  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-5 p-6">
      <Link
        href="/dcim"
        className="flex w-fit items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeftIcon className="size-4" /> 返回机柜列表
      </Link>

      <header className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="text-xl font-semibold">
            机柜 {rackName === "—" ? id : rackName}
          </h1>
          <Badge variant="secondary">
            已用 {usedU} / {uTotal} U
          </Badge>
        </div>
        <p className="text-sm text-muted-foreground">点击空闲 U 位挂载设备，点击占用块的按钮卸载</p>
      </header>

      {/* U 位矩阵：自上而下 U 高 → U 低，两侧导轨刻度 */}
      <div className="flex justify-start">
        <div className="flex gap-2">
          {/* 左侧 U 位刻度（每格顶部对齐标注） */}
          <div className="flex w-12 flex-col">
            {segments.map((segment) => (
              <div
                key={`label-${segment.startU}`}
                className="flex items-start justify-end pr-2 pt-0.5 text-xs text-muted-foreground"
                style={{ height: segment.count * ROW_HEIGHT }}
              >
                {segmentULabel(segment)}
              </div>
            ))}
          </div>
          {/* 机柜本体 */}
          <div className="flex w-80 flex-col overflow-hidden rounded-lg border-2 border-foreground/20 bg-muted/30">
            {segments.map((segment) => {
              const height = segment.count * ROW_HEIGHT
              if (segment.occupantId === null) {
                return (
                  <button
                    key={`free-${segment.startU}`}
                    type="button"
                    title={`${segmentULabel(segment)} 空闲，点击挂载`}
                    onClick={() => {
                      setMountTarget({ u: segment.startU, maxHeight: segment.count })
                      setMountOpen(true)
                    }}
                    className="flex items-center justify-center border-b border-dashed border-muted-foreground/25 text-xs text-muted-foreground/70 transition-colors last:border-b-0 hover:bg-accent hover:text-foreground"
                    style={{ height }}
                  >
                    {segment.count >= 2 ? `${segment.count}U 空闲` : ""}
                  </button>
                )
              }
              return (
                <div
                  key={`used-${segment.startU}-${segment.occupantId}`}
                  className="flex items-center justify-between gap-2 border-b border-primary/20 bg-primary/15 px-2 last:border-b-0"
                  style={{ height }}
                >
                  <span
                    className="min-w-0 truncate text-xs font-medium"
                    title={segment.occupantName ?? segment.occupantId}
                  >
                    {segment.occupantName ?? segment.occupantId}
                  </span>
                  <Button
                    variant="ghost"
                    size="icon-xs"
                    aria-label={`卸载 ${segment.occupantName ?? segment.occupantId}`}
                    title="卸载"
                    onClick={() => {
                      setUnmountError(null)
                      setUnmounting(segment)
                    }}
                  >
                    <UnplugIcon className="text-destructive" />
                  </Button>
                </div>
              )
            })}
          </div>
        </div>
      </div>

      <RackMountDialog
        open={mountOpen}
        onOpenChange={setMountOpen}
        rackCiId={id}
        uTotal={uTotal}
        initialU={mountTarget.u}
        maxHeight={mountTarget.maxHeight}
        onMounted={() => void load()}
      />
      <ConfirmDialog
        open={unmounting !== null}
        onOpenChange={(open) => {
          if (!open) setUnmounting(null)
        }}
        title="卸载该设备？"
        description={`将把「${unmounting?.occupantName ?? unmounting?.occupantId ?? ""}」从 ${unmounting ? segmentULabel(unmounting) : ""} 卸载，U 位随即释放。`}
        confirmText="卸载"
        error={unmountError}
        loading={unmountLoading}
        onConfirm={() => void onUnmountConfirm()}
      />
    </div>
  )
}
