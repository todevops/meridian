"use client"

// F-021 关系可视化：以当前 CI 为中心的一跳局部拓扑（React Flow）。
// 出向对端排右侧、入向对端排左侧，边按关系类型着色并带箭头方向；
// 点击对端节点且有可推导链接时跳转其详情页。原始关系列表由调用方保留为折叠表格。

import { useCallback, useMemo } from "react"
import { useRouter } from "next/navigation"
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

import type { CI, CIRelation } from "@/lib/api"
import { pickAttr } from "@/lib/format"

/** 对端 CI 的展示名候选属性编码 */
const PEER_NAME_CODES = ["hostname", "ident", "name", "ip"]

/** 边配色盘：按关系编码哈希取色，同一关系类型全站同色 */
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

type GraphNodeData = { label: string; sub?: string; center?: boolean }

/** 通用节点：左右各一对 handle，出向边从右出、入向边从右进中心 */
function GraphNode({ data }: NodeProps<Node<GraphNodeData>>) {
  return (
    <div
      className={`max-w-44 min-w-28 rounded-lg border px-3 py-2 text-center text-xs shadow-sm ${
        data.center
          ? "border-primary bg-primary text-primary-foreground"
          : "cursor-pointer bg-card hover:border-primary"
      }`}
    >
      <Handle type="target" position={Position.Left} className="!bg-muted-foreground" />
      <p className="truncate font-medium">{data.label}</p>
      {data.sub ? (
        <p
          className={`truncate text-[10px] ${
            data.center ? "text-primary-foreground/70" : "text-muted-foreground"
          }`}
        >
          {data.sub}
        </p>
      ) : null}
      <Handle type="source" position={Position.Right} className="!bg-muted-foreground" />
    </div>
  )
}

const nodeTypes = { graph: GraphNode }

const X_GAP = 300
const Y_GAP = 84

interface RelationGraphProps {
  /** 中心 CI */
  ci: CI
  relations: CIRelation[]
  /** 关系编码 → 中文名（来自模型定义） */
  relationNames: Map<string, string>
  /** 生成对端详情链接；返回 null 时对端节点不可跳转 */
  hrefForPeer: (rel: CIRelation) => string | null
  /** 画布高度类名，默认 h-72 */
  heightClass?: string
}

export function RelationGraph({
  ci,
  relations,
  relationNames,
  hrefForPeer,
  heightClass = "h-72",
}: RelationGraphProps) {
  const router = useRouter()

  const { nodes, edges, hrefByNode } = useMemo(() => {
    const centerName = pickAttr(ci.attributes, PEER_NAME_CODES)
    const outgoing = relations.filter((rel) => rel.direction === "outgoing")
    const incoming = relations.filter((rel) => rel.direction === "incoming")

    const nodes: Node<GraphNodeData>[] = [
      {
        id: ci.id,
        type: "graph",
        position: { x: 0, y: 0 },
        data: { label: centerName === "—" ? ci.id : centerName, sub: "当前 CI", center: true },
      },
    ]
    const edges: Edge[] = []
    const hrefByNode = new Map<string, string>()

    const placePeers = (list: CIRelation[], isOutgoing: boolean) => {
      list.forEach((rel, index) => {
        const peerName = pickAttr(rel.peer_ci.attributes, PEER_NAME_CODES)
        const relName = relationNames.get(rel.relation_code) ?? rel.relation_code
        // 两侧各自垂直堆叠，围绕中心点对称分布
        const offset = index - (list.length - 1) / 2
        nodes.push({
          id: rel.peer_ci.id,
          type: "graph",
          position: { x: isOutgoing ? X_GAP : -X_GAP, y: offset * Y_GAP },
          data: {
            label: peerName === "—" ? rel.peer_ci.id : peerName,
            sub: relName,
          },
        })
        const color = colorFor(rel.relation_code)
        const edge: Edge = isOutgoing
          ? { id: `e-${ci.id}-${rel.peer_ci.id}-${index}`, source: ci.id, target: rel.peer_ci.id }
          : { id: `e-${rel.peer_ci.id}-${ci.id}-${index}`, source: rel.peer_ci.id, target: ci.id }
        edge.label = relName
        edge.labelStyle = { fontSize: 10 }
        edge.style = { stroke: color }
        edge.markerEnd = { type: MarkerType.ArrowClosed, color }
        edges.push(edge)
        const href = hrefForPeer(rel)
        if (href) hrefByNode.set(rel.peer_ci.id, href)
      })
    }
    placePeers(outgoing, true)
    placePeers(incoming, false)
    return { nodes, edges, hrefByNode }
  }, [ci, relations, relationNames, hrefForPeer])

  const onNodeClick = useCallback(
    (_: unknown, node: Node) => {
      const href = hrefByNode.get(node.id)
      if (href) router.push(href)
    },
    [hrefByNode, router],
  )

  return (
    <div className={`w-full overflow-hidden rounded-lg border ${heightClass}`}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodeClick={onNodeClick}
        fitView
        fitViewOptions={{ padding: 0.3, maxZoom: 1.2 }}
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
