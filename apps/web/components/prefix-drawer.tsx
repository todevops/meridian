"use client"

// 前缀详情抽屉：子前缀、IP 列表（状态/关联 CI/描述）、自动分配 IP、手动登记 IP（409 冲突提示）

import { useCallback, useEffect, useState } from "react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerHeader,
  DrawerTitle,
} from "@/components/ui/drawer"
import {
  ApiError,
  allocateIPs,
  createIP,
  getCI,
  getPrefix,
  listIPs,
  type IpamIP,
  type IpamPrefix,
  type Paged,
} from "@/lib/api"
import { ipStatusLabel } from "@/lib/labels"
import { pickAttr } from "@/lib/format"

const IP_PAGE_SIZE = 20

/** 关联 CI 的展示名候选属性编码 */
const CI_NAME_CODES = ["hostname", "name", "ident", "ip"]

const IP_STATUS_OPTIONS = ["available", "used", "reserved"] as const
const DEFAULT_STATUS = "__default__"

/** 利用率字段兼容 0-1 小数与 0-100 百分数两种约定 */
export function utilizationPercent(prefix: IpamPrefix): number | null {
  const util = prefix.utilization
  if (!util) return null
  const raw = util.utilization
  if (typeof raw !== "number" || Number.isNaN(raw)) return null
  return Math.round(raw <= 1 ? raw * 100 : raw)
}

interface PrefixDrawerProps {
  /** 为 null 表示抽屉关闭 */
  prefix: IpamPrefix | null
  onOpenChange: (open: boolean) => void
  /** IP 数据变化后回调父组件刷新前缀列表（利用率会变化） */
  onChanged: () => void
}

export function PrefixDrawer({ prefix, onOpenChange, onChanged }: PrefixDrawerProps) {
  const [detail, setDetail] = useState<IpamPrefix | null>(null)
  const [ips, setIps] = useState<Paged<IpamIP> | null>(null)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [ciNames, setCiNames] = useState<Record<string, string>>({})

  // 分配 IP 表单状态
  const [allocCount, setAllocCount] = useState("1")
  const [allocDesc, setAllocDesc] = useState("")
  const [allocLoading, setAllocLoading] = useState(false)
  const [allocError, setAllocError] = useState<string | null>(null)
  const [allocResult, setAllocResult] = useState<string[] | null>(null)

  // 手动登记表单状态
  const [regIP, setRegIP] = useState("")
  const [regDesc, setRegDesc] = useState("")
  const [regStatus, setRegStatus] = useState(DEFAULT_STATUS)
  const [regCiId, setRegCiId] = useState("")
  const [regLoading, setRegLoading] = useState(false)
  const [regError, setRegError] = useState<string | null>(null)
  const [regOk, setRegOk] = useState<string | null>(null)

  const prefixId = prefix?.id ?? null

  const load = useCallback(async () => {
    if (!prefixId) return
    setLoading(true)
    setError(null)
    try {
      // 详情（含子前缀）与 IP 列表并行加载；详情失败不阻塞 IP 列表
      const [detailResult, ipsResult] = await Promise.allSettled([
        getPrefix(prefixId),
        listIPs({ prefix_id: prefixId, page, page_size: IP_PAGE_SIZE }),
      ])
      if (detailResult.status === "fulfilled") setDetail(detailResult.value)
      if (ipsResult.status === "fulfilled") {
        setIps(ipsResult.value)
      } else {
        const err = ipsResult.reason
        setError(err instanceof ApiError ? err.message : "加载 IP 列表失败")
      }
    } finally {
      setLoading(false)
    }
  }, [prefixId, page])

  // 打开抽屉时重置状态并加载
  useEffect(() => {
    if (!prefixId) return
    setDetail(null)
    setIps(null)
    setPage(1)
    setAllocCount("1")
    setAllocDesc("")
    setAllocError(null)
    setAllocResult(null)
    setRegIP("")
    setRegDesc("")
    setRegStatus(DEFAULT_STATUS)
    setRegCiId("")
    setRegError(null)
    setRegOk(null)
  }, [prefixId])

  useEffect(() => {
    void load()
  }, [load])

  // IP 列表加载后解析关联 CI 的展示名（失败回退为短 id）
  useEffect(() => {
    if (!ips) return
    const ids = Array.from(new Set(ips.items.map((item) => item.ci_id).filter(Boolean))) as string[]
    if (ids.length === 0) {
      setCiNames({})
      return
    }
    let cancelled = false
    void Promise.all(
      ids.map(async (id): Promise<readonly [string, string]> => {
        try {
          const ci = await getCI(id)
          const name = pickAttr(ci.attributes, [...CI_NAME_CODES])
          return [id, name === "—" ? "" : name] as const
        } catch {
          return [id, ""] as const
        }
      }),
    ).then((entries) => {
      if (!cancelled) setCiNames(Object.fromEntries(entries))
    })
    return () => {
      cancelled = true
    }
  }, [ips])

  const onAllocate = useCallback(async () => {
    if (!prefixId) return
    const count = Number(allocCount)
    if (!Number.isInteger(count) || count < 1) {
      setAllocError("数量须为不小于 1 的整数")
      return
    }
    setAllocLoading(true)
    setAllocError(null)
    setAllocResult(null)
    try {
      const allocated = await allocateIPs(prefixId, {
        count,
        ...(allocDesc.trim() ? { description: allocDesc.trim() } : {}),
      })
      setAllocResult(allocated)
      onChanged()
      void load()
    } catch (err) {
      setAllocError(err instanceof ApiError ? err.message : "分配失败，请稍后重试")
    } finally {
      setAllocLoading(false)
    }
  }, [prefixId, allocCount, allocDesc, load, onChanged])

  const onRegister = useCallback(async () => {
    if (!prefixId) return
    const ip = regIP.trim()
    if (!ip) {
      setRegError("请输入 IP 地址")
      return
    }
    setRegLoading(true)
    setRegError(null)
    setRegOk(null)
    try {
      const created = await createIP({
        prefix_id: prefixId,
        ip,
        ...(regStatus !== DEFAULT_STATUS ? { status: regStatus } : {}),
        ...(regCiId.trim() ? { ci_id: regCiId.trim() } : {}),
        ...(regDesc.trim() ? { description: regDesc.trim() } : {}),
      })
      setRegOk(`已登记 ${created.ip}`)
      setRegIP("")
      setRegDesc("")
      setRegCiId("")
      onChanged()
      void load()
    } catch (err) {
      // 409 重复登记、400 不在前缀内等错误直接展示契约错误文案
      setRegError(
        err instanceof ApiError
          ? err.status === 409
            ? `登记冲突：${err.message}`
            : err.message
          : "登记失败，请稍后重试",
      )
    } finally {
      setRegLoading(false)
    }
  }, [prefixId, regIP, regDesc, regStatus, regCiId, load, onChanged])

  const totalPages = ips ? Math.max(1, Math.ceil(ips.total / IP_PAGE_SIZE)) : 1
  const percent = prefix ? utilizationPercent(prefix) : null
  const children = detail?.children ?? []

  return (
    <Drawer open={prefix !== null} onOpenChange={onOpenChange}>
      <DrawerContent>
        <DrawerHeader>
          <DrawerTitle>
            <code className="rounded-md bg-muted px-1.5 py-0.5">{prefix?.cidr}</code>{" "}
            {prefix?.name}
          </DrawerTitle>
          <DrawerDescription>
            {prefix?.description || "暂无描述"}
            {percent !== null && prefix?.utilization
              ? ` · 已用 ${prefix.utilization.used_ips} / ${prefix.utilization.total_ips}（${percent}%）`
              : ""}
          </DrawerDescription>
        </DrawerHeader>

        {/* 子前缀 */}
        {children.length > 0 ? (
          <section className="flex flex-col gap-2">
            <h3 className="text-xs font-medium">子前缀（{children.length}）</h3>
            <div className="flex flex-wrap gap-2">
              {children.map((child) => (
                <Badge key={child.id} variant="secondary">
                  {child.cidr}（{child.name}）
                </Badge>
              ))}
            </div>
          </section>
        ) : null}

        {/* 自动分配 IP */}
        <section className="flex flex-col gap-3 rounded-xl border p-4">
          <h3 className="text-xs font-medium">分配 IP</h3>
          <div className="grid grid-cols-[7rem_1fr_auto] items-end gap-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="alloc-count">数量</Label>
              <Input
                id="alloc-count"
                inputMode="numeric"
                value={allocCount}
                onChange={(event) => setAllocCount(event.target.value)}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="alloc-desc">描述（可选）</Label>
              <Input
                id="alloc-desc"
                placeholder="如：分配给 web 集群"
                value={allocDesc}
                onChange={(event) => setAllocDesc(event.target.value)}
              />
            </div>
            <Button onClick={() => void onAllocate()} disabled={allocLoading}>
              {allocLoading ? "分配中…" : "分配"}
            </Button>
          </div>
          {allocError ? <p className="text-xs text-destructive">{allocError}</p> : null}
          {allocResult !== null ? (
            <div className="flex flex-col gap-1.5">
              <p className="text-xs text-emerald-600 dark:text-emerald-400">
                分配成功{allocResult.length > 0 ? `（${allocResult.length} 个）` : ""}：
              </p>
              <div className="flex flex-wrap gap-1.5">
                {allocResult.map((ip) => (
                  <Badge key={ip} variant="success">
                    {ip}
                  </Badge>
                ))}
              </div>
            </div>
          ) : null}
        </section>

        {/* 手动登记 IP */}
        <section className="flex flex-col gap-3 rounded-xl border p-4">
          <h3 className="text-xs font-medium">手动登记 IP</h3>
          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="reg-ip">
                IP 地址<span className="text-destructive">*</span>
              </Label>
              <Input
                id="reg-ip"
                placeholder="如：10.0.0.10"
                value={regIP}
                onChange={(event) => setRegIP(event.target.value)}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label>状态</Label>
              <Select
                value={regStatus}
                onValueChange={(value) => {
                  if (value) setRegStatus(value)
                }}
              >
                <SelectTrigger>
                  <SelectValue>
                    {(v: string) => (v === DEFAULT_STATUS ? "默认（服务端决定）" : ipStatusLabel(v))}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={DEFAULT_STATUS}>默认（服务端决定）</SelectItem>
                  {IP_STATUS_OPTIONS.map((s) => (
                    <SelectItem key={s} value={s}>
                      {ipStatusLabel(s)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="reg-ci">关联 CI ID（可选）</Label>
              <Input
                id="reg-ci"
                placeholder="CI uuid"
                value={regCiId}
                onChange={(event) => setRegCiId(event.target.value)}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="reg-desc">描述（可选）</Label>
              <Input
                id="reg-desc"
                value={regDesc}
                onChange={(event) => setRegDesc(event.target.value)}
              />
            </div>
          </div>
          {regError ? <p className="text-xs text-destructive">{regError}</p> : null}
          {regOk ? (
            <p className="text-xs text-emerald-600 dark:text-emerald-400">{regOk}</p>
          ) : null}
          <div>
            <Button variant="secondary" onClick={() => void onRegister()} disabled={regLoading}>
              {regLoading ? "登记中…" : "登记"}
            </Button>
          </div>
        </section>

        {/* IP 列表 */}
        <section className="flex flex-col gap-3">
          <h3 className="text-xs font-medium">IP 列表{ips ? `（共 ${ips.total} 条）` : ""}</h3>
          {loading && !ips ? (
            <div className="flex flex-col gap-2">
              {Array.from({ length: 5 }).map((_, index) => (
                <Skeleton key={index} className="h-9 w-full" />
              ))}
            </div>
          ) : error ? (
            <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-10">
              <p className="text-xs text-destructive">{error}</p>
              <Button variant="outline" size="sm" onClick={() => void load()}>
                重试
              </Button>
            </div>
          ) : (
            <>
              <div className="rounded-xl border">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>IP</TableHead>
                      <TableHead>状态</TableHead>
                      <TableHead>关联 CI</TableHead>
                      <TableHead>描述</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {(ips?.items ?? []).length === 0 ? (
                      <TableRow>
                        <TableCell colSpan={4} className="py-10 text-center text-muted-foreground">
                          暂无 IP 记录，可通过上方「分配」或「手动登记」创建
                        </TableCell>
                      </TableRow>
                    ) : (
                      (ips?.items ?? []).map((item) => (
                        <TableRow key={item.id}>
                          <TableCell>
                            <code className="rounded-md bg-muted px-1.5 py-0.5 text-xs">
                              {item.ip}
                            </code>
                          </TableCell>
                          <TableCell>
                            <Badge variant={item.status === "used" || item.status === "allocated" ? "success" : "secondary"}>
                              {ipStatusLabel(item.status)}
                            </Badge>
                          </TableCell>
                          <TableCell>
                            {item.ci_id ? (
                              <span className="block max-w-40 truncate" title={item.ci_id}>
                                {ciNames[item.ci_id] || `${item.ci_id.slice(0, 8)}…`}
                              </span>
                            ) : (
                              "—"
                            )}
                          </TableCell>
                          <TableCell>
                            <span className="block max-w-44 truncate" title={item.description ?? ""}>
                              {item.description || "—"}
                            </span>
                          </TableCell>
                        </TableRow>
                      ))
                    )}
                  </TableBody>
                </Table>
              </div>
              <div className="flex items-center justify-between text-xs text-muted-foreground">
                <span>共 {ips?.total ?? 0} 条</span>
                <div className="flex items-center gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={page <= 1 || loading}
                    onClick={() => setPage((p) => p - 1)}
                  >
                    上一页
                  </Button>
                  <span>
                    第 {page} / {totalPages} 页
                  </span>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={page >= totalPages || loading}
                    onClick={() => setPage((p) => p + 1)}
                  >
                    下一页
                  </Button>
                </div>
              </div>
            </>
          )}
        </section>
      </DrawerContent>
    </Drawer>
  )
}
