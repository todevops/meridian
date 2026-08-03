"use client"

// F-027 依赖拓扑：应用↔应用 / 应用↔DB 两跳依赖图（React Flow）。
// 本应用为中心节点高亮；其余节点按 BFS 跳数分环布局（一跳内环、两跳外环），
// 边带关系码标签并按关系码哈希取色；点击应用节点可切换聚合视图选中应用。

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Background,
  Controls,
  Handle,
  MarkerType,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from "@xyflow/react"
import "@xyflow/react/dist/style.css"

import { Skeleton } from "@/components/ui/skeleton"
import {
  getApplicationDependencies,
  type AppDependencyGraph,
} from "@/lib/api"

/** 节点类型中文文案；契约外类型兜底展示原文 */
const TYPE_LABELS: Record<string, string> = {
  app: "应用",
  db: "数据库",
}

/** 边配色盘：按关系码哈希取色，同一关系类型全站同色（与关系图组件口径一致） */
const EDGE_PALETTE = [
  "#0ea5e9",
  "#8b5cf6",
  "#f59e0b",
  "#10b981",
  "#ef4444",
  "#ec4899",
  "#6366f1",
  "#14b8a6",
]

function colorFor(code: string): string {
  let hash = 0
  for (let i = 0; i < code.length; i += 1) {
    hash = (hash * 31 + code.charCodeAt(i)) | 0
  }
  return EDGE_PALETTE[Math.abs(hash) % EDGE_PALETTE.length] ?? "#0ea5e9"
}

type DepNodeData = {
  label: string
  typeLabel?: string
  center?: boolean
  clickable?: boolean
}

/** 依赖图节点：上下各一对 handle，中心节点高亮 */
function DepNode({ data }: NodeProps<Node<DepNodeData>>) {
  return (
    <div
      className={`max-w-44 min-w-28 rounded-lg border px-3 py-2 text-center text-xs shadow-sm ${
        data.center
          ? "border-primary bg-primary text-primary-foreground"
          : data.clickable
            ? "cursor-pointer bg-card hover:border-primary"
            : "bg-card"
      }`}
    >
      <Handle type="target" position={Position.Top} className="!bg-muted-foreground" />
      <p className="truncate font-medium">{data.label}</p>
      {data.typeLabel ? (
        <p
          className={`truncate text-[10px] ${
            data.center ? "text-primary-foreground/70" : "text-muted-foreground"
          }`}
        >
          {data.typeLabel}
        </p>
      ) : null}
      <Handle type="source" position={Position.Bottom} className="!bg-muted-foreground" />
    </div>
  )
}

const nodeTypes = { dep: DepNode }

/** 环半径：一跳内环 / 两跳外环 */
const RING_RADIUS = [260, 500]

/** 自中心 BFS 计算各节点跳数（无向遍历，仅用于布局分环） */
function hopMapOf(graph: AppDependencyGraph, centerId: string): Map<string, number> {
  const adjacency = new Map<string, string[]>()
  for (const edge of graph.edges) {
    adjacency.set(edge.a, [...(adjacency.get(edge.a) ?? []), edge.b])
    adjacency.set(edge.b, [...(adjacency.get(edge.b) ?? []), edge.a])
  }
  const hops = new Map<string, number>([[centerId, 0]])
  const queue = [centerId]
  while (queue.length > 0) {
    const current = queue.shift()!
    const hop = hops.get(current)!
    for (const next of adjacency.get(current) ?? []) {
      if (!hops.has(next)) {
        hops.set(next, hop + 1)
        queue.push(next)
      }
    }
  }
  return hops
}

interface DependencyGraphProps {
  appId: string
  /** 点击应用类型节点时回调（用于切换聚合视图选中应用） */
  onSelectApp?: (appId: string) => void
  /** 画布高度类名，默认 h-96 */
  heightClass?: string
}

export function DependencyGraph({
  appId,
  onSelectApp,
  heightClass = "h-96",
}: DependencyGraphProps) {
  const [graph, setGraph] = useState<AppDependencyGraph | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setGraph(null)
    setError(null)
    try {
      setGraph(await getApplicationDependencies(appId))
    } catch {
      setError("依赖拓扑数据暂不可用")
    }
  }, [appId])

  useEffect(() => {
    void load()
  }, [load])

  const { nodes, edges, appNodeIds } = useMemo(() => {
    if (!graph) {
      return { nodes: [] as Node<DepNodeData>[], edges: [] as Edge[], appNodeIds: new Set<string>() }
    }
    const hops = hopMapOf(graph, appId)
    // 按跳数分桶，同环节点沿圆周均布
    const rings = new Map<number, string[]>()
    for (const node of graph.nodes) {
      if (node.id === appId) continue
      const hop = hops.get(node.id) ?? RING_RADIUS.length
      const ring = Math.min(Math.max(hop, 1), RING_RADIUS.length)
      rings.set(ring, [...(rings.get(ring) ?? []), node.id])
    }
    const positionOf = new Map<string, { x: number; y: number }>([[appId, { x: 0, y: 0 }]])
    for (const [ring, ids] of rings) {
      const radius = RING_RADIUS[ring - 1] ?? RING_RADIUS[RING_RADIUS.length - 1]!
      ids.forEach((id, index) => {
        const angle = (2 * Math.PI * index) / ids.length - Math.PI / 2
        positionOf.set(id, { x: radius * Math.cos(angle), y: radius * Math.sin(angle) })
      })
    }
    const appNodeIds = new Set(
      graph.nodes.filter((node) => (node.type ?? "app") === "app").map((node) => node.id),
    )
    const nodes: Node<DepNodeData>[] = graph.nodes.map((node) => {
      const center = node.id === appId
      const type = node.type ?? "app"
      return {
        id: node.id,
        type: "dep",
        position: positionOf.get(node.id) ?? { x: 0, y: 0 },
        data: {
          label: node.label,
          typeLabel: center ? "本应用" : (TYPE_LABELS[type] ?? type),
          center,
          clickable: !center && appNodeIds.has(node.id) && !!onSelectApp,
        },
      }
    })
    const edges: Edge[] = graph.edges.map((edge, index) => {
      const code = edge.code ?? ""
      const color = colorFor(code || "dep")
      return {
        id: `e-${edge.a}-${edge.b}-${index}`,
        source: edge.a,
        target: edge.b,
        label: code || undefined,
        labelStyle: { fontSize: 10 },
        style: { stroke: color },
        markerEnd: { type: MarkerType.ArrowClosed, color },
      }
    })
    return { nodes, edges, appNodeIds }
  }, [graph, appId, onSelectApp])

  const onNodeClick = useCallback(
    (_: unknown, node: Node) => {
      if (node.id !== appId && appNodeIds.has(node.id)) onSelectApp?.(node.id)
    },
    [appId, appNodeIds, onSelectApp],
  )

  if (error) {
    return <p className="text-xs text-muted-foreground">{error}</p>
  }
  if (!graph) {
    return <Skeleton className={`w-full ${heightClass}`} />
  }
  if (graph.nodes.length <= 1) {
    return (
      <p className="text-xs text-muted-foreground">
        暂无依赖关系：该应用未登记应用间或应用到数据库的依赖
      </p>
    )
  }

  return (
    <div className={`w-full overflow-hidden rounded-lg border ${heightClass}`}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodeClick={onNodeClick}
        fitView
        fitViewOptions={{ padding: 0.25, maxZoom: 1.1 }}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        zoomOnScroll={false}
        panOnDrag
      >
        <Background gap={16} />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  )
}
