"use client"

// 表格单元格内异步解析单条关系的对端 CI：避免整表 N+1 阻塞首屏，失败降级为占位符

import { useEffect, useState } from "react"
import Link from "next/link"

import { listCIRelations, type CI, type RelationDirection } from "@/lib/api"
import { pickAttr } from "@/lib/format"

/** 对端 CI 的展示名候选属性编码 */
const PEER_NAME_CODES = ["hostname", "ident", "name", "ip"]

interface RelationPeerCellProps {
  ciId: string
  /** 目标关系编码（如 runs_on / instantiated_by） */
  relationCode: string
  /** 限定方向；缺省双向皆可 */
  direction?: RelationDirection
  /** 生成对端详情链接；缺省或返回 null 时渲染纯文本 */
  hrefFor?: (peer: CI) => string | null
}

export function RelationPeerCell({
  ciId,
  relationCode,
  direction,
  hrefFor,
}: RelationPeerCellProps) {
  const [peer, setPeer] = useState<CI | null>(null)
  const [resolved, setResolved] = useState(false)

  useEffect(() => {
    let cancelled = false
    listCIRelations(ciId)
      .then((res) => {
        if (cancelled) return
        const hit = res.items.find(
          (rel) =>
            rel.relation_code === relationCode &&
            (!direction || rel.direction === direction),
        )
        setPeer(hit ? hit.peer_ci : null)
        setResolved(true)
      })
      .catch(() => {
        if (!cancelled) setResolved(true)
      })
    return () => {
      cancelled = true
    }
  }, [ciId, relationCode, direction])

  if (!resolved) {
    return <span className="text-muted-foreground">…</span>
  }
  if (!peer) return <span className="text-muted-foreground">—</span>

  const name = pickAttr(peer.attributes, PEER_NAME_CODES)
  const text = name === "—" ? peer.id : name
  const href = hrefFor ? hrefFor(peer) : null
  if (!href) return <span>{text}</span>
  return (
    <Link
      href={href}
      className="font-medium text-primary hover:underline"
      onClick={(event) => event.stopPropagation()}
    >
      {text}
    </Link>
  )
}
