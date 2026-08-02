"use client"

// DCIM 页：全局容量总览（机房/机柜/U 位/电力）+ 按机房分组的机柜卡片，点卡片进 U 位矩阵。
// 数据一次取自 /dcim/overview（避免逐机柜 N+1 请求）；支持新建机房。

import { useCallback, useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import {
  Building2 as Building2Icon,
  Pencil as PencilIcon,
  Plus as PlusIcon,
  Server as ServerIcon,
  Warehouse as WarehouseIcon,
  Zap as ZapIcon,
} from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ApiError,
  createCI,
  createCIRelation,
  deleteCIRelation,
  getDCIMOverview,
  listModels,
  type DCIMOverview,
  type DCIMRackStat,
  type DCIMRoomStat,
} from "@/lib/api"

/** U 位利用率百分比；容量为 0 时返回 null */
function uPercent(used: number, total: number): number | null {
  return total > 0 ? Math.round((used / total) * 100) : null
}

function UsageBar({ percent }: { percent: number }) {
  return (
    <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
      <div
        className={`h-full rounded-full ${
          percent > 90 ? "bg-destructive" : percent > 70 ? "bg-amber-500" : "bg-emerald-500"
        }`}
        style={{ width: `${Math.min(100, percent)}%` }}
      />
    </div>
  )
}

export default function DcimPage() {
  const router = useRouter()
  const [overview, setOverview] = useState<DCIMOverview | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [roomDialogOpen, setRoomDialogOpen] = useState(false)
  const [assigningRack, setAssigningRack] = useState<DCIMRackStat | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setOverview(await getDCIMOverview())
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载 DCIM 总览失败")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const racksOf = (roomId: string | null): DCIMRackStat[] =>
    (overview?.racks ?? []).filter((r) => r.room_id === roomId)

  const globalPercent = overview ? uPercent(overview.u_used, overview.u_total) : null

  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-6 p-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">DCIM 数据中心设施</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            机房 / 机柜 / U 位与电力容量概览，点击机柜卡片进入 U 位矩阵进行挂载/卸载
          </p>
        </div>
        <Button variant="outline" onClick={() => setRoomDialogOpen(true)}>
          <PlusIcon /> 新建机房
        </Button>
      </header>

      {loading && !overview ? (
        <div className="flex flex-col gap-4">
          <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-24 w-full" />
            ))}
          </div>
          <Skeleton className="h-48 w-full" />
        </div>
      ) : error ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
          <p className="text-sm text-destructive">{error}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      ) : !overview ? null : (
        <>
          {/* 全局容量统计 */}
          <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
            <StatCard icon={<WarehouseIcon className="size-4" />} label="机房" value={overview.room_count} />
            <StatCard icon={<ServerIcon className="size-4" />} label="机柜" value={overview.rack_count} />
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="flex items-center gap-1.5 text-sm font-normal text-muted-foreground">
                  <Building2Icon className="size-4" /> U 位占用
                </CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col gap-2">
                <div className="text-2xl font-semibold">
                  {overview.u_used}
                  <span className="text-sm font-normal text-muted-foreground"> / {overview.u_total} U</span>
                  {globalPercent !== null && (
                    <span className="ml-2 text-sm font-normal text-muted-foreground">{globalPercent}%</span>
                  )}
                </div>
                {globalPercent !== null && <UsageBar percent={globalPercent} />}
              </CardContent>
            </Card>
            <StatCard
              icon={<ZapIcon className="size-4" />}
              label="电力容量合计"
              value={`${overview.power_capacity_kw} kW`}
            />
          </div>

          {/* 按机房分组 */}
          {overview.rooms.map((room) => (
            <RoomSection key={room.room_id} room={room} racks={racksOf(room.room_id)} onOpenRack={(id) => router.push(`/dcim/${id}`)} />
          ))}

          {/* 未分配机房的机柜 */}
          {overview.unassigned.rack_count > 0 && (
            <section className="flex flex-col gap-3">
              <div className="flex items-baseline justify-between">
                <h2 className="text-base font-medium text-muted-foreground">未分配机房</h2>
                <p className="text-sm text-muted-foreground">
                  {overview.unassigned.rack_count} 柜 · U 位 {overview.unassigned.u_used}/{overview.unassigned.u_total} · {overview.unassigned.power_capacity_kw} kW
                </p>
              </div>
              <RackGrid racks={racksOf(null)} onOpenRack={(id) => router.push(`/dcim/${id}`)} />
            </section>
          )}

          {overview.room_count === 0 && overview.rack_count === 0 && (
            <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed py-16">
              <WarehouseIcon className="size-8 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">
                暂无机房与机柜，可点击「新建机房」登记，或运行 scripts/seed-models.sh 导入种子模型
              </p>
            </div>
          )}
        </>
      )}

      <RoomCreateDialog open={roomDialogOpen} onOpenChange={setRoomDialogOpen} onSaved={load} />
    </div>
  )
}

function StatCard({ icon, label, value }: { icon: React.ReactNode; label: string; value: React.ReactNode }) {
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="flex items-center gap-1.5 text-sm font-normal text-muted-foreground">
          {icon} {label}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="text-2xl font-semibold">{value}</div>
      </CardContent>
    </Card>
  )
}

function RoomSection({
  room,
  racks,
  onOpenRack,
}: {
  room: DCIMRoomStat
  racks: DCIMRackStat[]
  onOpenRack: (rackId: string) => void
}) {
  const percent = uPercent(room.u_used, room.u_total)
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <div className="flex items-baseline gap-2">
          <h2 className="text-base font-medium">{room.name || room.room_id}</h2>
          {room.code && <span className="text-sm text-muted-foreground">({room.code})</span>}
          {room.address && <span className="text-sm text-muted-foreground">· {room.address}</span>}
        </div>
        <p className="text-sm text-muted-foreground">
          {room.rack_count} 柜 · U 位 {room.u_used}/{room.u_total}
          {percent !== null && ` (${percent}%)`} · {room.power_capacity_kw} kW
        </p>
      </div>
      {racks.length > 0 ? (
        <RackGrid racks={racks} onOpenRack={onOpenRack} />
      ) : (
        <p className="rounded-lg border border-dashed px-3 py-4 text-center text-sm text-muted-foreground">
          该机房暂无机柜
        </p>
      )}
    </section>
  )
}

function RackGrid({ racks, onOpenRack }: { racks: DCIMRackStat[]; onOpenRack: (rackId: string) => void }) {
  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {racks.map((rack) => {
        const percent = uPercent(rack.u_used, rack.u_total)
        return (
          <button key={rack.rack_id} type="button" onClick={() => onOpenRack(rack.rack_id)} className="text-left">
            <Card className="h-full transition-colors hover:border-primary/50">
              <CardHeader>
                <CardTitle className="flex items-center justify-between gap-2">
                  <span className="truncate">{rack.name || rack.rack_id}</span>
                  {percent !== null && (
                    <span className="shrink-0 text-sm font-normal text-muted-foreground">{percent}%</span>
                  )}
                </CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <dl className="flex flex-col gap-1.5 text-sm">
                  <div className="flex items-center justify-between gap-4">
                    <dt className="text-muted-foreground">电力容量</dt>
                    <dd>{rack.power_capacity_kw} kW</dd>
                  </div>
                  <div className="flex items-center justify-between gap-4">
                    <dt className="text-muted-foreground">U 位占用</dt>
                    <dd>
                      {rack.u_used} / {rack.u_total} U
                    </dd>
                  </div>
                </dl>
                {percent !== null && <UsageBar percent={percent} />}
              </CardContent>
            </Card>
          </button>
        )
      })}
    </div>
  )
}

function RoomCreateDialog({
  open,
  onOpenChange,
  onSaved,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}) {
  const [name, setName] = useState("")
  const [code, setCode] = useState("")
  const [address, setAddress] = useState("")
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (open) {
      setName("")
      setCode("")
      setAddress("")
      setSubmitError(null)
    }
  }, [open])

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSubmitError(null)
    if (!name.trim() || !code.trim()) {
      setSubmitError("机房名称与编码均为必填")
      return
    }
    setSubmitting(true)
    try {
      // 机房是模型 code=room 的 CI：先解析模型 ID 再建档
      const models = await listModels({ keyword: "room", page_size: 50 })
      const roomModel = models.items.find((m) => m.code === "room")
      if (!roomModel) {
        setSubmitError("机房模型（code=room）不存在，请先运行 scripts/seed-models.sh 导入种子模型")
        return
      }
      await createCI({
        model_id: roomModel.id,
        attributes: { name: name.trim(), code: code.trim(), address: address.trim() },
        source: "manual",
      })
      onOpenChange(false)
      onSaved()
    } catch (err) {
      setSubmitError(err instanceof ApiError ? err.message : "创建机房失败，请稍后重试")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建机房</DialogTitle>
          <DialogDescription>机房是 DCIM 的顶层设施对象，机柜经「所在机房」关系挂载到机房。</DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          <div className="grid grid-cols-2 gap-4">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="room-name">
                机房名称<span className="text-destructive">*</span>
              </Label>
              <Input id="room-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="如：亦庄机房" />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="room-code">
                机房编码<span className="text-destructive">*</span>
              </Label>
              <Input id="room-code" value={code} onChange={(e) => setCode(e.target.value)} placeholder="如：bj-yz（全局唯一）" />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="room-address">地址</Label>
            <Input id="room-address" value={address} onChange={(e) => setAddress(e.target.value)} placeholder="可选" />
          </div>
          {submitError && (
            <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {submitError}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={submitting}>
              {submitting ? "保存中…" : "保存"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
