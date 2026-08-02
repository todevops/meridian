"use client"

// 虚拟化三级视图：左侧 集群 → ESXi 主机树，右侧选中 ESXi 的虚拟机清单；
// VM↔主机 CI 经 instantiated_by 入向关系互链（vCenter 采集器按 instanceUUID 调和）

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  ChevronDown as ChevronDownIcon,
  ChevronRight as ChevronRightIcon,
  Layers as LayersIcon,
  Server as ServerIcon,
} from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { RelationPeerCell } from "@/components/relation-peer-cell"
import { listCIRelations, type CI } from "@/lib/api"
import { listAllCIs, resolveModelId } from "@/lib/cis"
import { pickAttr } from "@/lib/format"

const CLUSTER_NAME_CODES = ["name", "cluster_name", "ident"]
const HOST_NAME_CODES = ["name", "hostname", "ident", "ip"]
const VM_IP_CODES = ["ip", "guest_ip", "primary_ip"]

const POWER_STATE_VARIANTS: Record<string, "success" | "secondary" | "outline"> = {
  poweredOn: "success",
  poweredOff: "secondary",
  suspended: "outline",
}

const POWER_STATE_LABELS: Record<string, string> = {
  poweredOn: "运行中",
  poweredOff: "已关机",
  suspended: "已挂起",
}

function displayName(ci: CI, codes: string[]): string {
  const name = pickAttr(ci.attributes, codes)
  return name === "—" ? ci.id : name
}

export default function VirtualizationPage() {
  const [vmModelId, setVmModelId] = useState<string | null>(null)
  const [clusters, setClusters] = useState<CI[] | null>(null)
  const [hosts, setHosts] = useState<CI[] | null>(null)
  /** ESXi 主机 id → 所属集群 id（由集群关系推导） */
  const [hostCluster, setHostCluster] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [selectedHostId, setSelectedHostId] = useState<string | null>(null)
  const [vms, setVMs] = useState<CI[] | null>(null)
  const [vmsLoading, setVMsLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [clusterModelId, hostModelId, vmModel] = await Promise.all([
        resolveModelId("esxi_cluster"),
        resolveModelId("esxi_host"),
        resolveModelId("virtual_machine"),
      ])
      setVmModelId(vmModel)
      const [clusterItems, hostItems] = await Promise.all([
        listAllCIs({ model_id: clusterModelId }),
        listAllCIs({ model_id: hostModelId }),
      ])
      setClusters(clusterItems)
      setHosts(hostItems)

      // 逐集群拉关系，凡对端为 ESXi 模型者即归属该集群（不依赖具体关系编码）
      const mapping: Record<string, string> = {}
      await Promise.all(
        clusterItems.map(async (cluster) => {
          try {
            const res = await listCIRelations(cluster.id)
            for (const rel of res.items) {
              if (rel.peer_ci.model_id === hostModelId) {
                mapping[rel.peer_ci.id] = cluster.id
              }
            }
          } catch {
            // 单集群关系拉取失败不阻塞整体视图
          }
        }),
      )
      setHostCluster(mapping)
      // 默认展开全部集群，便于一屏览全
      setExpanded(new Set(clusterItems.map((cluster) => cluster.id)))
    } catch {
      setError("加载虚拟化视图失败")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  // 选中 ESXi 后，经其关系找出对端为 VM 模型的全部虚拟机
  useEffect(() => {
    if (!selectedHostId || !vmModelId) {
      setVMs(null)
      return
    }
    let cancelled = false
    setVMsLoading(true)
    listCIRelations(selectedHostId)
      .then((res) => {
        if (cancelled) return
        setVMs(
          res.items
            .filter((rel) => rel.peer_ci.model_id === vmModelId)
            .map((rel) => rel.peer_ci),
        )
      })
      .catch(() => {
        if (!cancelled) setVMs([])
      })
      .finally(() => {
        if (!cancelled) setVMsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [selectedHostId, vmModelId])

  /** 树结构：集群 → 其下 ESXi；未归属任何集群的主机单独成组 */
  const tree = useMemo(() => {
    const byCluster = new Map<string, CI[]>()
    const unassigned: CI[] = []
    for (const host of hosts ?? []) {
      const clusterId = hostCluster[host.id]
      if (clusterId) {
        const list = byCluster.get(clusterId) ?? []
        list.push(host)
        byCluster.set(clusterId, list)
      } else {
        unassigned.push(host)
      }
    }
    return { byCluster, unassigned }
  }, [hosts, hostCluster])

  const selectedHost = (hosts ?? []).find((host) => host.id === selectedHostId)

  const toggleCluster = (clusterId: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(clusterId)) {
        next.delete(clusterId)
      } else {
        next.add(clusterId)
      }
      return next
    })
  }

  const renderHostButton = (host: CI) => (
    <button
      key={host.id}
      type="button"
      onClick={() => setSelectedHostId(host.id)}
      className={`flex w-full items-center gap-2 rounded-md py-1.5 pr-2 pl-8 text-left text-sm transition-colors hover:bg-muted ${
        selectedHostId === host.id ? "bg-muted font-medium" : ""
      }`}
    >
      <ServerIcon className="size-3.5 shrink-0 text-muted-foreground" />
      <span className="truncate">{displayName(host, HOST_NAME_CODES)}</span>
    </button>
  )

  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-5 p-6">
      <header>
        <h1 className="text-xl font-semibold">虚拟化视图</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          集群 → ESXi 主机 → 虚拟机三级清单，由 vSphere 采集器自动建档并动态维护从属关系
        </p>
      </header>

      {loading ? (
        <div className="flex gap-5">
          <Skeleton className="h-72 w-72 shrink-0" />
          <Skeleton className="h-72 flex-1" />
        </div>
      ) : error ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
          <p className="text-sm text-destructive">{error}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      ) : (
        <div className="flex flex-col gap-5 lg:flex-row">
          {/* 左侧：集群树 */}
          <aside className="w-full shrink-0 rounded-xl border p-3 lg:w-72">
            <h2 className="mb-2 flex items-center gap-2 px-1 text-sm font-semibold">
              <LayersIcon className="size-4" /> 集群（{clusters?.length ?? 0}）
            </h2>
            {(clusters?.length ?? 0) === 0 && tree.unassigned.length === 0 ? (
              <p className="px-1 py-8 text-center text-sm text-muted-foreground">
                暂无虚拟化数据，等待 vSphere 采集器上报
              </p>
            ) : (
              <div className="flex flex-col gap-1">
                {(clusters ?? []).map((cluster) => {
                  const members = tree.byCluster.get(cluster.id) ?? []
                  const isOpen = expanded.has(cluster.id)
                  return (
                    <div key={cluster.id}>
                      <button
                        type="button"
                        onClick={() => toggleCluster(cluster.id)}
                        className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-sm font-medium transition-colors hover:bg-muted"
                      >
                        {isOpen ? (
                          <ChevronDownIcon className="size-4 shrink-0 text-muted-foreground" />
                        ) : (
                          <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground" />
                        )}
                        <span className="truncate">
                          {displayName(cluster, CLUSTER_NAME_CODES)}
                        </span>
                        <Badge variant="secondary" className="ml-auto">
                          {members.length}
                        </Badge>
                      </button>
                      {isOpen ? (
                        <div className="flex flex-col gap-0.5 py-0.5">
                          {members.length === 0 ? (
                            <p className="py-1 pl-8 text-xs text-muted-foreground">
                              集群下暂无 ESXi 主机
                            </p>
                          ) : (
                            members.map(renderHostButton)
                          )}
                        </div>
                      ) : null}
                    </div>
                  )
                })}
                {tree.unassigned.length > 0 ? (
                  <div>
                    <p className="px-2 py-1.5 text-sm font-medium text-muted-foreground">
                      未分配集群
                    </p>
                    <div className="flex flex-col gap-0.5">
                      {tree.unassigned.map(renderHostButton)}
                    </div>
                  </div>
                ) : null}
              </div>
            )}
          </aside>

          {/* 右侧：选中 ESXi 的 VM 清单 */}
          <section className="min-w-0 flex-1 rounded-xl border">
            {!selectedHost ? (
              <div className="flex h-full flex-col items-center justify-center gap-2 py-20 text-muted-foreground">
                <ServerIcon className="size-8" />
                <p className="text-sm">从左侧选择一台 ESXi 主机，查看其虚拟机清单</p>
              </div>
            ) : (
              <>
                <div className="flex items-center justify-between gap-3 border-b px-4 py-3">
                  <h2 className="truncate text-sm font-semibold">
                    {displayName(selectedHost, HOST_NAME_CODES)} 的虚拟机
                    {vms ? `（${vms.length}）` : ""}
                  </h2>
                  <Badge variant="outline">{selectedHost.status}</Badge>
                </div>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>名称</TableHead>
                      <TableHead>电源状态</TableHead>
                      <TableHead>vCPU</TableHead>
                      <TableHead>内存(GB)</TableHead>
                      <TableHead>IP</TableHead>
                      <TableHead>关联主机</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {vmsLoading || vms === null ? (
                      Array.from({ length: 4 }).map((_, index) => (
                        <TableRow key={index}>
                          <TableCell colSpan={6}>
                            <Skeleton className="h-5 w-full" />
                          </TableCell>
                        </TableRow>
                      ))
                    ) : vms.length === 0 ? (
                      <TableRow>
                        <TableCell
                          colSpan={6}
                          className="py-10 text-center text-muted-foreground"
                        >
                          该主机下暂无虚拟机
                        </TableCell>
                      </TableRow>
                    ) : (
                      vms.map((vm) => {
                        const powerState = String(
                          vm.attributes.power_state ?? "",
                        )
                        return (
                          <TableRow key={vm.id}>
                            <TableCell className="font-medium">
                              {pickAttr(vm.attributes, ["name"])}
                            </TableCell>
                            <TableCell>
                              <Badge
                                variant={
                                  POWER_STATE_VARIANTS[powerState] ?? "outline"
                                }
                              >
                                {POWER_STATE_LABELS[powerState] ??
                                  (powerState || "—")}
                              </Badge>
                            </TableCell>
                            <TableCell>
                              {pickAttr(vm.attributes, ["vcpu"])}
                            </TableCell>
                            <TableCell>
                              {pickAttr(vm.attributes, ["memory_gb", "mem_gb"])}
                            </TableCell>
                            <TableCell>
                              {pickAttr(vm.attributes, VM_IP_CODES)}
                            </TableCell>
                            <TableCell>
                              {/* VM↔主机 CI 互链：instantiated_by 入向即宿主机 */}
                              <RelationPeerCell
                                ciId={vm.id}
                                relationCode="instantiated_by"
                                direction="incoming"
                                hrefFor={(peer) => `/hosts/${peer.id}`}
                              />
                            </TableCell>
                          </TableRow>
                        )
                      })
                    )}
                  </TableBody>
                </Table>
              </>
            )}
          </section>
        </div>
      )}
    </div>
  )
}
