"use client"

// K8s 容器云视图（F-024）：左树 集群 → 命名空间（k8s_namespace.cluster 属性分组），
// 右侧选中命名空间的 工作负载 / Service / Ingress 三表；业务归属经命名空间 mounted_to 反显（整挂继承）。
// 工作负载行点开抽屉：属性 + Pod 实况表（直查 apiserver，不落库）。

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Boxes as BoxesIcon,
  ChevronDown as ChevronDownIcon,
  ChevronRight as ChevronRightIcon,
  Container as ContainerIcon,
} from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Drawer,
  DrawerContent,
  DrawerDescription,
  DrawerHeader,
  DrawerTitle,
} from "@/components/ui/drawer"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  ApiError,
  listCIRelations,
  listK8sPods,
  type CI,
  type K8sPod,
} from "@/lib/api"
import { listAllCIs, resolveModelId } from "@/lib/cis"
import { attrText, pickAttr } from "@/lib/format"

/** 命名空间展示名候选属性编码 */
const NS_NAME_CODES = ["name", "namespace"]
/** 对端业务应用展示名候选属性编码 */
const APP_NAME_CODES = ["name", "app_name", "ident"]

type TabKey = "workload" | "service" | "ingress"

const TABS: { key: TabKey; label: string }[] = [
  { key: "workload", label: "工作负载" },
  { key: "service", label: "Service" },
  { key: "ingress", label: "Ingress" },
]

/** Pod 阶段着色：CrashLoopBackOff 等异常一律红色 */
function phaseVariant(phase: string): "success" | "secondary" | "outline" | "destructive" {
  if (/crash|failed|error|unknown/i.test(phase)) return "destructive"
  if (phase === "Running" || phase === "Succeeded") return "success"
  if (phase === "Pending") return "secondary"
  return "outline"
}

/** Pod 存活时长：age_seconds → 紧凑可读文本 */
function formatAge(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "—"
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d${h}h`
  if (h > 0) return `${h}h${m}m`
  if (m > 0) return `${m}m`
  return `${Math.floor(seconds)}s`
}

function displayName(ci: CI, codes: string[]): string {
  const name = pickAttr(ci.attributes, codes)
  return name === "—" ? ci.id : name
}

/** 命名空间（及 Service/Ingress）按 cluster+namespace 归属过滤 */
function inNamespace(ci: CI, cluster: string, namespace: string): boolean {
  const ns = attrText(ci.attributes.namespace)
  const cl = attrText(ci.attributes.cluster)
  if (ns !== namespace) return false
  // cluster 属性缺失时（Service/Ingress 模型可选）不强行过滤
  return cl === "—" || cl === cluster
}

/** k8s_service.kind 为 Ingress 的记录归入 Ingress Tab（兼容单模型上报形态） */
function isIngressKind(ci: CI): boolean {
  return attrText(ci.attributes.kind).toLowerCase() === "ingress"
}

export default function K8sPage() {
  const [clusters, setClusters] = useState<CI[]>([])
  const [namespaces, setNamespaces] = useState<CI[] | null>(null)
  const [workloads, setWorkloads] = useState<CI[]>([])
  const [services, setServices] = useState<CI[]>([])
  const [ingresses, setIngresses] = useState<CI[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [selectedNsId, setSelectedNsId] = useState<string | null>(null)
  const [tab, setTab] = useState<TabKey>("workload")
  /** 命名空间 mounted_to 反显的业务应用名（整挂继承） */
  const [appName, setAppName] = useState<string>("—")

  const [drawerWorkload, setDrawerWorkload] = useState<CI | null>(null)
  const [pods, setPods] = useState<K8sPod[] | null>(null)
  const [podsError, setPodsError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [clModelId, nsModelId, wlModelId, svcModelId, ingModelId] =
        await Promise.all([
          resolveModelId("k8s_cluster"),
          resolveModelId("k8s_namespace"),
          resolveModelId("k8s_workload"),
          resolveModelId("k8s_service"),
          resolveModelId("k8s_ingress"),
        ])
      // 集群/Service/Ingress 模型可能尚未由采集器建档，单类失败降级为空表不阻塞整页
      const [clItems, nsItems, wlItems, svcItems, ingItems] = await Promise.all([
        listAllCIs({ model_id: clModelId }).catch(() => [] as CI[]),
        listAllCIs({ model_id: nsModelId }),
        listAllCIs({ model_id: wlModelId }).catch(() => [] as CI[]),
        listAllCIs({ model_id: svcModelId }).catch(() => [] as CI[]),
        listAllCIs({ model_id: ingModelId }).catch(() => [] as CI[]),
      ])
      setClusters(clItems)
      setNamespaces(nsItems)
      setWorkloads(wlItems)
      setServices(svcItems)
      setIngresses(ingItems)
      // 默认展开全部集群
      setExpanded(
        new Set([
          ...clItems.map((cl) => attrText(cl.attributes.name)),
          ...nsItems.map((ns) => attrText(ns.attributes.cluster)),
        ]),
      )
    } catch {
      setError("加载容器云视图失败")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  /** 树结构：k8s_cluster → k8s_namespace；无集群 CI 的命名空间按其 cluster 属性兜底成组 */
  const tree = useMemo(() => {
    const byCluster = new Map<string, CI[]>()
    for (const ns of namespaces ?? []) {
      const cluster = attrText(ns.attributes.cluster)
      const key = cluster === "—" ? "未标注集群" : cluster
      const list = byCluster.get(key) ?? []
      list.push(ns)
      byCluster.set(key, list)
    }
    // k8s_cluster CI 优先作为树节点（即使其下暂无命名空间也显示，便于对账）
    for (const cl of clusters) {
      const name = attrText(cl.attributes.name)
      if (name !== "—" && !byCluster.has(name)) byCluster.set(name, [])
    }
    return [...byCluster.entries()].sort(([a], [b]) => a.localeCompare(b))
  }, [clusters, namespaces])

  const selectedNs = (namespaces ?? []).find((ns) => ns.id === selectedNsId) ?? null
  const selectedCluster = selectedNs ? attrText(selectedNs.attributes.cluster) : ""
  const selectedNsName = selectedNs ? displayName(selectedNs, NS_NAME_CODES) : ""

  // 选中命名空间后拉取其 mounted_to 关系，反显业务应用（工作负载经命名空间链继承归属）
  useEffect(() => {
    if (!selectedNs) {
      setAppName("—")
      return
    }
    let cancelled = false
    listCIRelations(selectedNs.id)
      .then((res) => {
        if (cancelled) return
        const hit = res.items.find((rel) => rel.relation_code === "mounted_to")
        setAppName(hit ? pickAttr(hit.peer_ci.attributes, APP_NAME_CODES) : "—")
      })
      .catch(() => {
        if (!cancelled) setAppName("—")
      })
    return () => {
      cancelled = true
    }
  }, [selectedNs])

  const nsWorkloads = useMemo(
    () =>
      selectedNs
        ? workloads.filter((wl) => inNamespace(wl, selectedCluster, selectedNsName))
        : [],
    [workloads, selectedNs, selectedCluster, selectedNsName],
  )
  // Service / Ingress 拆分：kind=Ingress 的 service 记录与独立 k8s_ingress 模型的 CI 一并进 Ingress Tab
  const nsServices = useMemo(
    () =>
      selectedNs
        ? services.filter(
            (svc) => !isIngressKind(svc) && inNamespace(svc, selectedCluster, selectedNsName),
          )
        : [],
    [services, selectedNs, selectedCluster, selectedNsName],
  )
  const nsIngresses = useMemo(
    () =>
      selectedNs
        ? [
            ...services.filter(
              (svc) => isIngressKind(svc) && inNamespace(svc, selectedCluster, selectedNsName),
            ),
            ...ingresses.filter((ing) => inNamespace(ing, selectedCluster, selectedNsName)),
          ]
        : [],
    [services, ingresses, selectedNs, selectedCluster, selectedNsName],
  )

  // 打开工作负载抽屉时直查 apiserver 拉 Pod 实况（不落库）
  useEffect(() => {
    if (!drawerWorkload) {
      setPods(null)
      setPodsError(null)
      return
    }
    let cancelled = false
    setPods(null)
    setPodsError(null)
    listK8sPods({
      cluster: attrText(drawerWorkload.attributes.cluster),
      namespace: attrText(drawerWorkload.attributes.namespace),
      selector: `app=${attrText(drawerWorkload.attributes.name)}`,
    })
      .then((items) => {
        if (!cancelled) setPods(items)
      })
      .catch((err) => {
        if (cancelled) return
        setPodsError(
          err instanceof ApiError
            ? `Pod 实况查询失败：${err.message}`
            : "Pod 实况查询失败（apiserver 不可达）",
        )
      })
    return () => {
      cancelled = true
    }
  }, [drawerWorkload])

  const toggleCluster = (cluster: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(cluster)) {
        next.delete(cluster)
      } else {
        next.add(cluster)
      }
      return next
    })
  }

  /** Service / Ingress 共用的简表（名称/kind/selector/host） */
  const renderResourceTable = (items: CI[], emptyText: string) => (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>名称</TableHead>
          <TableHead>kind</TableHead>
          <TableHead>selector</TableHead>
          <TableHead>host</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {items.length === 0 ? (
          <TableRow>
            <TableCell colSpan={4} className="py-10 text-center text-muted-foreground">
              {emptyText}
            </TableCell>
          </TableRow>
        ) : (
          items.map((item) => (
            <TableRow key={item.id}>
              <TableCell className="font-medium">
                {pickAttr(item.attributes, ["name"])}
              </TableCell>
              <TableCell>
                <Badge variant="secondary">{pickAttr(item.attributes, ["kind", "type"])}</Badge>
              </TableCell>
              <TableCell className="font-mono text-xs">
                {attrText(item.attributes.selector)}
              </TableCell>
              <TableCell>{pickAttr(item.attributes, ["host", "hosts"])}</TableCell>
            </TableRow>
          ))
        )}
      </TableBody>
    </Table>
  )

  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <header>
        <h1 className="text-xl font-semibold">容器云视图</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          集群 → 命名空间 → 工作负载三级清单，由 K8s 采集器自动建档；业务归属经命名空间整挂继承
        </p>
      </header>

      {loading ? (
        <div className="flex gap-5">
          <Skeleton className="h-72 w-72 shrink-0" />
          <Skeleton className="h-72 flex-1" />
        </div>
      ) : error ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
          <p className="text-xs text-destructive">{error}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      ) : (
        <div className="flex flex-col gap-5 lg:flex-row">
          {/* 左侧：集群 → 命名空间树 */}
          <aside className="w-full shrink-0 rounded-xl border p-3 lg:w-72">
            <h2 className="mb-2 flex items-center gap-2 px-1 text-xs font-semibold">
              <ContainerIcon className="size-4" /> 集群（{tree.length}）
            </h2>
            {tree.length === 0 ? (
              <p className="px-1 py-8 text-center text-xs text-muted-foreground">
                暂无 K8s 数据，等待采集器上报
              </p>
            ) : (
              <div className="flex flex-col gap-1">
                {tree.map(([cluster, nsList]) => {
                  const isOpen = expanded.has(cluster)
                  return (
                    <div key={cluster}>
                      <button
                        type="button"
                        onClick={() => toggleCluster(cluster)}
                        className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left text-xs font-medium transition-colors hover:bg-muted"
                      >
                        {isOpen ? (
                          <ChevronDownIcon className="size-4 shrink-0 text-muted-foreground" />
                        ) : (
                          <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground" />
                        )}
                        <span className="truncate">{cluster}</span>
                        <Badge variant="secondary" className="ml-auto">
                          {nsList.length}
                        </Badge>
                      </button>
                      {isOpen ? (
                        <div className="flex flex-col gap-0.5 py-0.5">
                          {nsList.map((ns) => (
                            <button
                              key={ns.id}
                              type="button"
                              onClick={() => setSelectedNsId(ns.id)}
                              className={`flex w-full items-center gap-2 rounded-md py-1.5 pr-2 pl-8 text-left text-xs transition-colors hover:bg-muted ${
                                selectedNsId === ns.id ? "bg-muted font-medium" : ""
                              }`}
                            >
                              <BoxesIcon className="size-3.5 shrink-0 text-muted-foreground" />
                              <span className="truncate">{displayName(ns, NS_NAME_CODES)}</span>
                            </button>
                          ))}
                        </div>
                      ) : null}
                    </div>
                  )
                })}
              </div>
            )}
          </aside>

          {/* 右侧：选中命名空间的资源面板 */}
          <section className="min-w-0 flex-1 rounded-xl border">
            {!selectedNs ? (
              <div className="flex h-full flex-col items-center justify-center gap-2 py-20 text-muted-foreground">
                <ContainerIcon className="size-8" />
                <p className="text-xs">从左侧选择一个命名空间，查看其工作负载与网络资源</p>
              </div>
            ) : (
              <>
                <div className="flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3">
                  <h2 className="truncate text-xs font-semibold">
                    {selectedCluster} / {selectedNsName}
                  </h2>
                  <div className="flex items-center gap-2">
                    {appName !== "—" ? (
                      <Badge variant="success">业务：{appName}</Badge>
                    ) : (
                      <Badge variant="outline">未挂载业务</Badge>
                    )}
                    {/* Tab 切换：工作负载 / Service / Ingress */}
                    <div className="flex rounded-lg border p-0.5">
                      {TABS.map((t) => (
                        <button
                          key={t.key}
                          type="button"
                          onClick={() => setTab(t.key)}
                          className={`rounded-md px-3 py-1 text-xs transition-colors ${
                            tab === t.key
                              ? "bg-primary text-primary-foreground"
                              : "text-muted-foreground hover:text-foreground"
                          }`}
                        >
                          {t.label}
                        </button>
                      ))}
                    </div>
                  </div>
                </div>

                {tab === "workload" ? (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>类型</TableHead>
                        <TableHead>名称</TableHead>
                        <TableHead>副本(就绪)</TableHead>
                        <TableHead>镜像</TableHead>
                        <TableHead>业务归属</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {nsWorkloads.length === 0 ? (
                        <TableRow>
                          <TableCell
                            colSpan={5}
                            className="py-10 text-center text-muted-foreground"
                          >
                            该命名空间下暂无工作负载
                          </TableCell>
                        </TableRow>
                      ) : (
                        nsWorkloads.map((wl) => {
                          const total = attrText(wl.attributes.replicas)
                          const ready = pickAttr(wl.attributes, [
                            "ready_replicas",
                            "available_replicas",
                          ])
                          return (
                            <TableRow
                              key={wl.id}
                              className="cursor-pointer"
                              onClick={() => setDrawerWorkload(wl)}
                            >
                              <TableCell>
                                <Badge variant="secondary">
                                  {pickAttr(wl.attributes, ["kind"])}
                                </Badge>
                              </TableCell>
                              <TableCell className="font-medium">
                                {pickAttr(wl.attributes, ["name"])}
                              </TableCell>
                              <TableCell>
                                {ready === "—" ? total : `${ready}/${total}`}
                              </TableCell>
                              <TableCell className="max-w-64 truncate font-mono text-xs">
                                {pickAttr(wl.attributes, ["image"])}
                              </TableCell>
                              <TableCell>{appName}</TableCell>
                            </TableRow>
                          )
                        })
                      )}
                    </TableBody>
                  </Table>
                ) : tab === "service" ? (
                  renderResourceTable(nsServices, "该命名空间下暂无 Service，等待采集器上报")
                ) : (
                  renderResourceTable(nsIngresses, "该命名空间下暂无 Ingress，等待采集器上报")
                )}
              </>
            )}
          </section>
        </div>
      )}

      {/* 工作负载详情抽屉：属性 + Pod 实况（直查 apiserver，不落库） */}
      <Drawer
        open={drawerWorkload !== null}
        onOpenChange={(open) => {
          if (!open) setDrawerWorkload(null)
        }}
      >
        <DrawerContent>
          {drawerWorkload ? (
            <>
              <DrawerHeader>
                <DrawerTitle className="flex items-center gap-2">
                  {pickAttr(drawerWorkload.attributes, ["name"])}
                  <Badge variant="secondary">
                    {pickAttr(drawerWorkload.attributes, ["kind"])}
                  </Badge>
                </DrawerTitle>
                <DrawerDescription>
                  {attrText(drawerWorkload.attributes.cluster)} /{" "}
                  {attrText(drawerWorkload.attributes.namespace)}
                  {appName !== "—" ? ` · 业务：${appName}` : ""}
                </DrawerDescription>
              </DrawerHeader>

              <section className="flex flex-col gap-2">
                <h2 className="text-xs font-semibold">属性</h2>
                <dl className="flex flex-col gap-2 rounded-lg border p-3">
                  {Object.entries(drawerWorkload.attributes).map(([code, value]) => (
                    <div
                      key={code}
                      className="flex items-baseline justify-between gap-4 text-xs"
                    >
                      <dt className="shrink-0 text-muted-foreground">{code}</dt>
                      <dd className="min-w-0 text-right break-all">{attrText(value)}</dd>
                    </div>
                  ))}
                </dl>
              </section>

              <section className="flex flex-col gap-2">
                <h2 className="text-xs font-semibold">
                  Pod 实况{pods ? `（${pods.length}）` : ""}
                </h2>
                <p className="text-xs text-muted-foreground">
                  实况直查 apiserver，不落库
                </p>
                {podsError ? (
                  <p className="rounded-lg border border-dashed p-3 text-xs text-destructive">
                    {podsError}
                  </p>
                ) : pods === null ? (
                  <div className="flex flex-col gap-2">
                    {Array.from({ length: 3 }).map((_, index) => (
                      <Skeleton key={index} className="h-6 w-full" />
                    ))}
                  </div>
                ) : (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>名称</TableHead>
                        <TableHead>阶段</TableHead>
                        <TableHead>节点</TableHead>
                        <TableHead>重启次数</TableHead>
                        <TableHead>存活时长</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {pods.length === 0 ? (
                        <TableRow>
                          <TableCell
                            colSpan={5}
                            className="py-8 text-center text-muted-foreground"
                          >
                            未查询到匹配 Pod（selector: app=
                            {attrText(drawerWorkload.attributes.name)}）
                          </TableCell>
                        </TableRow>
                      ) : (
                        pods.map((pod) => (
                          <TableRow key={pod.name}>
                            <TableCell className="max-w-48 truncate font-mono text-xs">
                              {pod.name}
                            </TableCell>
                            <TableCell>
                              <Badge variant={phaseVariant(pod.phase)}>{pod.phase}</Badge>
                            </TableCell>
                            <TableCell>{pod.node ?? "—"}</TableCell>
                            <TableCell
                              className={pod.restart_count > 0 ? "text-destructive" : ""}
                            >
                              {pod.restart_count}
                            </TableCell>
                            <TableCell>{formatAge(pod.age_seconds)}</TableCell>
                          </TableRow>
                        ))
                      )}
                    </TableBody>
                  </Table>
                )}
              </section>
            </>
          ) : null}
        </DrawerContent>
      </Drawer>
    </div>
  )
}
