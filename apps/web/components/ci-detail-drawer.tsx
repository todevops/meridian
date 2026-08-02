"use client"

// 通用 CI 详情抽屉：属性（按模型定义标注中文名）+ 关系列表；
// 供网络设备、数据库实例等台账页复用，extra 插槽用于追加集成占位卡（如 Oxidized）

import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react"
import Link from "next/link"
import {
  ArrowLeft as IncomingIcon,
  ArrowRight as OutgoingIcon,
} from "lucide-react"

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
  getModel,
  listCIRelations,
  type CI,
  type CIRelation,
  type CIStatus,
  type Model,
} from "@/lib/api"
import { CI_STATUS_LABELS } from "@/lib/labels"
import { attrText, pickAttr } from "@/lib/format"

const STATUS_VARIANTS: Record<CIStatus, "success" | "secondary" | "outline"> = {
  active: "success",
  discovered: "secondary",
  retired: "outline",
}

/** CI 展示名候选属性编码 */
const CI_NAME_CODES = ["hostname", "ident", "name", "instance_addr", "ip"]

/** 按关系编码推导对端详情链接（主机/机柜有专属详情页，其余不跳转） */
function defaultPeerHref(rel: CIRelation): string | null {
  if (rel.relation_code === "runs_on" || rel.relation_code === "instantiated_by") {
    return `/hosts/${rel.peer_ci.id}`
  }
  if (rel.relation_code === "located_in") {
    return `/dcim/${rel.peer_ci.id}`
  }
  return null
}

interface CIDetailDrawerProps {
  /** 为 null 表示抽屉关闭 */
  ci: CI | null
  onOpenChange: (open: boolean) => void
  /** 覆盖默认的对端链接推导 */
  hrefForPeer?: (rel: CIRelation) => string | null
  /** 追加在关系区之后的扩展内容（如 Oxidized 备份占位卡） */
  extra?: ReactNode
}

export function CIDetailDrawer({
  ci,
  onOpenChange,
  hrefForPeer = defaultPeerHref,
  extra,
}: CIDetailDrawerProps) {
  const [model, setModel] = useState<Model | null>(null)
  const [relations, setRelations] = useState<CIRelation[] | null>(null)

  const load = useCallback(async (target: CI) => {
    setModel(null)
    setRelations(null)
    const [modelResult, relationsResult] = await Promise.allSettled([
      getModel(target.model_id),
      listCIRelations(target.id),
    ])
    setModel(modelResult.status === "fulfilled" ? modelResult.value : null)
    setRelations(
      relationsResult.status === "fulfilled" ? relationsResult.value.items : [],
    )
  }, [])

  useEffect(() => {
    if (ci) void load(ci)
  }, [ci, load])

  // 属性展示：模型定义在前（带中文名），CI 多出的属性兜底在后
  const attrs = useMemo(() => {
    if (!ci) return []
    const defs = model?.attributes ?? []
    const defined = new Set(defs.map((def) => def.code))
    const rows: { code: string; label: string; value: string }[] = defs.map(
      (def) => ({
        code: def.code,
        label: `${def.name}（${def.code}）`,
        value: attrText(ci.attributes[def.code]),
      }),
    )
    for (const [code, value] of Object.entries(ci.attributes)) {
      if (!defined.has(code)) {
        rows.push({ code, label: code, value: attrText(value) })
      }
    }
    return rows
  }, [ci, model])

  const relationNames = useMemo(() => {
    const map = new Map<string, string>()
    for (const rel of model?.relations ?? []) map.set(rel.code, rel.name)
    return map
  }, [model])

  const title = ci ? pickAttr(ci.attributes, CI_NAME_CODES) : ""

  return (
    <Drawer open={ci !== null} onOpenChange={onOpenChange}>
      <DrawerContent>
        {ci ? (
          <>
            <DrawerHeader>
              <DrawerTitle className="flex items-center gap-2">
                {title === "—" ? ci.id : title}
                <Badge variant={STATUS_VARIANTS[ci.status]}>
                  {CI_STATUS_LABELS[ci.status]}
                </Badge>
              </DrawerTitle>
              <DrawerDescription>
                来源：{ci.source}
                {model ? ` · 模型：${model.name}` : ""}
              </DrawerDescription>
            </DrawerHeader>

            <section className="flex flex-col gap-2">
              <h2 className="text-xs font-semibold">属性</h2>
              {model === null ? (
                <div className="flex flex-col gap-2">
                  {Array.from({ length: 4 }).map((_, index) => (
                    <Skeleton key={index} className="h-5 w-full" />
                  ))}
                </div>
              ) : attrs.length === 0 ? (
                <p className="text-xs text-muted-foreground">暂无属性</p>
              ) : (
                <dl className="flex flex-col gap-2 rounded-lg border p-3">
                  {attrs.map((attr) => (
                    <div
                      key={attr.code}
                      className="flex items-baseline justify-between gap-4 text-xs"
                    >
                      <dt className="shrink-0 text-muted-foreground">
                        {attr.label}
                      </dt>
                      <dd className="min-w-0 text-right break-all">
                        {attr.value}
                      </dd>
                    </div>
                  ))}
                </dl>
              )}
            </section>

            <section className="flex flex-col gap-2">
              <h2 className="text-xs font-semibold">
                关系{relations ? `（${relations.length}）` : ""}
              </h2>
              {relations === null ? (
                <Skeleton className="h-10 w-full" />
              ) : relations.length === 0 ? (
                <p className="text-xs text-muted-foreground">暂无关系</p>
              ) : (
                <ul className="flex flex-col gap-2">
                  {relations.map((rel, index) => {
                    const peerName = pickAttr(
                      rel.peer_ci.attributes,
                      CI_NAME_CODES,
                    )
                    const href = hrefForPeer(rel)
                    return (
                      <li
                        key={`${rel.relation_code}-${rel.peer_ci.id}-${index}`}
                        className="flex items-center justify-between gap-3 rounded-lg border px-3 py-2 text-xs"
                      >
                        <span className="flex items-center gap-2">
                          <Badge variant="secondary">
                            {relationNames.get(rel.relation_code) ??
                              rel.relation_code}
                          </Badge>
                          {rel.direction === "outgoing" ? (
                            <span className="flex items-center gap-1 text-muted-foreground">
                              <OutgoingIcon className="size-3.5" /> 出向
                            </span>
                          ) : (
                            <span className="flex items-center gap-1 text-muted-foreground">
                              <IncomingIcon className="size-3.5" /> 入向
                            </span>
                          )}
                        </span>
                        {href ? (
                          <Link
                            href={href}
                            className="min-w-0 truncate font-medium text-primary hover:underline"
                          >
                            {peerName === "—" ? rel.peer_ci.id : peerName}
                          </Link>
                        ) : (
                          <span className="min-w-0 truncate">
                            {peerName === "—" ? rel.peer_ci.id : peerName}
                          </span>
                        )}
                      </li>
                    )
                  })}
                </ul>
              )}
            </section>

            {extra}
          </>
        ) : null}
      </DrawerContent>
    </Drawer>
  )
}
