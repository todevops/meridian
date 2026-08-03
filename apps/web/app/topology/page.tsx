"use client"

// 网络拓扑页（F-061）：React Flow 渲染 GET /api/v1/topology 的 LLDP/CDP 链路图。
// 设备节点按机房（room）分行分组着色、按 model_code 分图标；链路边悬停显示端口对，
// manual 手工链路虚线区分；支持缩放/拖拽/MiniMap。右侧边栏点节点显示设备摘要并跳转台账。
// 顶部「主机接入定位」：输入 IP 交叉 ARP/MAC 表定位接入交换机端口（无命中 404 友好提示）。

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import {
  Background,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"
import {
  Cable as CableIcon,
  LocateFixed as LocateIcon,
  Network as NetworkIcon,
  Router as RouterIcon,
  Search as SearchIcon,
  Server as ServerIcon,
  Shield as ShieldIcon,
  Waypoints as WaypointsIcon,
  type LucideIcon,
} from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import {
  ApiError,
  getHostLocation,
  getTopology,
  type HostLocation,
  type TopologyGraph,
} from "@/lib/api"

/** 机房配色盘：按机房名哈希取色，同一机房全图同色 */
const ROOM_PALETTE = [
  "#0ea5e9",
  "#8b5cf6",
  "#f59e0b",
  "#10b981",
  "#ef4444",
  "#ec4899",
  "#6366f1",
  "#14b8a6",
]

function roomColor(room: string): string {
  let hash = 0
  for (let i = 0; i < room.length; i += 1) {
    hash = (hash * 31 + room.charCodeAt(i)) | 0
  }
  return ROOM_PALETTE[Math.abs(hash) % ROOM_PALETTE.length] ?? "#0ea5e9"
}

/** 按 model_code 推断设备图标 */
function deviceIcon(modelCode: string): LucideIcon {
  const code = modelCode.toLowerCase()
  if (code.includes("switch")) return NetworkIcon
  if (code.includes("router")) return RouterIcon
  if (code.includes("firewall")) return ShieldIcon
  if (code.includes("server")) return ServerIcon
  return CableIcon
}

type TopoNodeData = { label: string; sub?: string; roomColor: string; Icon: LucideIcon }

/** 设备节点：左侧机房色条 + 图标 + 名称 */
function TopoNode({ data }: NodeProps<Node<TopoNodeData>>) {
  return (
    <div className="flex w-44 cursor-pointer items-center gap-2 rounded-lg border bg-card px-2.5 py-2 text-xs shadow-sm hover:border-primary">
      <Handle type="target" position={Position.Left} className="!bg-muted-foreground" />
      <span
        className="h-8 w-1 shrink-0 rounded-full"
        style={{ backgroundColor: data.roomColor }}
      />
      <data.Icon className="size-4 shrink-0 text-muted-foreground" />
      <span className="min-w-0">
        <span className="block truncate font-medium">{data.label}</span>
        {data.sub ? (
          <span className="block truncate text-[10px] text-muted-foreground">{data.sub}</span>
        ) : null}
      </span>
      <Handle type="source" position={Position.Right} className="!bg-muted-foreground" />
    </div>
  )
}

const nodeTypes = { topo: TopoNode }

const X_GAP = 260
const Y_GAP = 140

export default function TopologyPage() {
  const [graph, setGraph] = useState<TopologyGraph | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [hoveredEdgeId, setHoveredEdgeId] = useState<string | null>(null)

  // 主机接入定位
  const [ip, setIp] = useState("")
  const [locating, setLocating] = useState(false)
  const [location, setLocation] = useState<HostLocation | null>(null)
  const [locationError, setLocationError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setGraph(await getTopology())
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载网络拓扑失败")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  /** 布局：按机房分行，行内网格排列 */
  const { nodes, edges, nodeById } = useMemo(() => {
    const nodeById = new Map((graph?.nodes ?? []).map((n) => [n.id, n]))
    if (!graph) return { nodes: [] as Node<TopoNodeData>[], edges: [] as Edge[], nodeById }

    // 机房分组（保持机房名字典序，行位置稳定）
    const rooms = new Map<string, typeof graph.nodes>()
    for (const node of graph.nodes) {
      const room = node.room || "未分配机房"
      const list = rooms.get(room) ?? []
      list.push(node)
      rooms.set(room, list)
    }
    const sortedRooms = [...rooms.entries()].sort(([a], [b]) => a.localeCompare(b))

    const nodes: Node<TopoNodeData>[] = []
    sortedRooms.forEach(([room, members], rowIndex) => {
      const color = roomColor(room)
      members.forEach((node, colIndex) => {
        nodes.push({
          id: node.id,
          type: "topo",
          position: { x: colIndex * X_GAP, y: rowIndex * Y_GAP },
          data: { label: node.name, sub: room, roomColor: color, Icon: deviceIcon(node.model_code) },
        })
      })
    })

    const edges: Edge[] = graph.edges.map((edge, index) => {
      const id = `e-${edge.a}-${edge.b}-${index}`
      const portPair = `${edge.a_port ?? "?"} ↔ ${edge.b_port ?? "?"}`
      const manual = edge.source === "manual"
      return {
        id,
        source: edge.a,
        target: edge.b,
        // 悬停时才显示端口对，避免密图标签糊屏
        label: hoveredEdgeId === id ? portPair : undefined,
        labelStyle: { fontSize: 10 },
        labelBgPadding: [4, 2] as [number, number],
        labelBgBorderRadius: 4,
        interactionWidth: 16,
        style: manual ? { strokeDasharray: "6 4" } : undefined,
        markerEnd: { type: MarkerType.ArrowClosed },
      }
    })
    return { nodes, edges, nodeById }
  }, [graph, hoveredEdgeId])

  const selectedNode = selectedNodeId ? nodeById.get(selectedNodeId) : null
  /** 选中设备的链路数 */
  const selectedDegree = useMemo(() => {
    if (!selectedNodeId || !graph) return 0
    return graph.edges.filter((e) => e.a === selectedNodeId || e.b === selectedNodeId).length
  }, [selectedNodeId, graph])

  const locate = useCallback(async () => {
    const target = ip.trim()
    if (!target || locating) return
    setLocating(true)
    setLocation(null)
    setLocationError(null)
    try {
      setLocation(await getHostLocation(target))
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        setLocationError(
          `未找到 ${target} 的接入位置：ARP/MAC 表中无该地址记录，可能主机离线或尚未被采集覆盖`,
        )
      } else {
        setLocationError(err instanceof ApiError ? err.message : "接入定位查询失败")
      }
    } finally {
      setLocating(false)
    }
  }, [ip, locating])

  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold">网络拓扑</h1>
          <p className="mt-1 text-xs text-muted-foreground">
            由 LLDP/CDP 邻居表自动建链（虚线为管理员手工链路），按机房分行着色
          </p>
        </div>
        {/* 主机接入定位：IP → 交换机端口 */}
        <form
          className="flex items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault()
            void locate()
          }}
        >
          <Input
            value={ip}
            onChange={(event) => setIp(event.target.value)}
            placeholder="主机接入定位：输入 IP"
            className="h-8 w-56 text-xs"
            data-testid="host-location-input"
          />
          <Button type="submit" size="sm" disabled={locating || !ip.trim()}>
            <SearchIcon className="size-3.5" />
            {locating ? "定位中…" : "定位"}
          </Button>
        </form>
      </header>

      {/* 接入定位结果卡 */}
      {locationError ? (
        <p className="rounded-lg border border-dashed px-4 py-3 text-xs text-muted-foreground">
          {locationError}
        </p>
      ) : location ? (
        <div className="flex flex-wrap items-center gap-3 rounded-lg border px-4 py-3 text-xs">
          <LocateIcon className="size-4 text-primary" />
          <span className="font-medium">{location.ip}</span>
          <span className="text-muted-foreground">接入</span>
          <Badge variant="secondary">交换机：{location.switch ?? "—"}</Badge>
          <Badge variant="secondary">端口：{location.port ?? "—"}</Badge>
          <Badge variant="outline">协议：{location.protocol ?? "—"}</Badge>
          {location.mac ? (
            <span className="font-mono text-muted-foreground">MAC：{location.mac}</span>
          ) : null}
        </div>
      ) : null}

      {loading ? (
        <Skeleton className="h-[28rem] w-full" />
      ) : error ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
          <p className="text-xs text-destructive">{error}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      ) : (graph?.nodes.length ?? 0) === 0 ? (
        <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed py-20 text-muted-foreground">
          <WaypointsIcon className="size-8" />
          <p className="text-xs">暂无拓扑数据，等待 LibreNMS 邻居表同步</p>
        </div>
      ) : (
        <div className="flex flex-col gap-5 xl:flex-row">
          {/* 拓扑画布 */}
          <div
            className="h-[30rem] min-w-0 flex-1 overflow-hidden rounded-xl border"
            data-testid="topology-canvas"
          >
            <ReactFlow
              nodes={nodes}
              edges={edges}
              nodeTypes={nodeTypes}
              onNodeClick={(_, node) => setSelectedNodeId(node.id)}
              onPaneClick={() => setSelectedNodeId(null)}
              onEdgeMouseEnter={(_, edge) => setHoveredEdgeId(edge.id)}
              onEdgeMouseLeave={() => setHoveredEdgeId(null)}
              fitView
              fitViewOptions={{ padding: 0.2 }}
              nodesConnectable={false}
            >
              <Background gap={16} />
              <Controls showInteractive={false} />
              <MiniMap pannable zoomable />
            </ReactFlow>
          </div>

          {/* 右侧边栏：选中设备摘要 */}
          <aside className="w-full shrink-0 rounded-xl border p-4 xl:w-72">
            {!selectedNode ? (
              <div className="flex h-full flex-col items-center justify-center gap-2 py-16 text-muted-foreground">
                <NetworkIcon className="size-8" />
                <p className="text-xs">点击拓扑节点查看设备摘要</p>
              </div>
            ) : (
              <div className="flex flex-col gap-3">
                <h2 className="flex items-center gap-2 text-xs font-semibold">
                  {(() => {
                    const Icon = deviceIcon(selectedNode.model_code)
                    return <Icon className="size-4 text-muted-foreground" />
                  })()}
                  {selectedNode.name}
                </h2>
                <dl className="flex flex-col gap-2 rounded-lg border p-3 text-xs">
                  <div className="flex items-center justify-between gap-4">
                    <dt className="text-muted-foreground">模型</dt>
                    <dd>{selectedNode.model_code}</dd>
                  </div>
                  <div className="flex items-center justify-between gap-4">
                    <dt className="text-muted-foreground">机房</dt>
                    <dd>
                      <span
                        className="mr-1.5 inline-block h-2 w-2 rounded-full align-middle"
                        style={{ backgroundColor: roomColor(selectedNode.room || "未分配机房") }}
                      />
                      {selectedNode.room || "未分配机房"}
                    </dd>
                  </div>
                  <div className="flex items-center justify-between gap-4">
                    <dt className="text-muted-foreground">链路数</dt>
                    <dd>{selectedDegree}</dd>
                  </div>
                </dl>
                <Link
                  href="/network/devices"
                  className="w-fit text-xs font-medium text-primary hover:underline"
                >
                  跳转网络设备台账 →
                </Link>
              </div>
            )}
          </aside>
        </div>
      )}
    </div>
  )
}
