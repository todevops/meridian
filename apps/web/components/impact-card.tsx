"use client"

// F-027 资源影响面卡：从主机/数据库等资源反查受影响的应用及路径链路。
// 数据源 GET /api/v1/cis/{id}/impact；接口未就绪或失败时降级为提示文本，不阻塞详情页。

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"
import { Waypoints as WaypointsIcon } from "lucide-react"

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { getCIImpact, type CIImpactItem } from "@/lib/api"
import { attrText } from "@/lib/format"

interface ImpactCardProps {
  ciId: string
}

export function ImpactCard({ ciId }: ImpactCardProps) {
  const [affected, setAffected] = useState<CIImpactItem[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setAffected(null)
    setError(null)
    try {
      const res = await getCIImpact(ciId)
      setAffected(res.affected ?? [])
    } catch {
      setError("影响面数据暂不可用")
    }
  }, [ciId])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <WaypointsIcon className="size-4" /> 影响面
          {affected && affected.length > 0 ? (
            <span className="text-xs font-normal text-muted-foreground">
              （{affected.length} 个受影响应用）
            </span>
          ) : null}
        </CardTitle>
        <CardDescription>
          沿关系链反查依赖当前资源的应用系统，用于变更与故障的影响评估
        </CardDescription>
      </CardHeader>
      <CardContent>
        {affected === null ? (
          error ? (
            <p className="text-xs text-muted-foreground">{error}</p>
          ) : (
            <div className="flex flex-col gap-2">
              {Array.from({ length: 2 }).map((_, index) => (
                <Skeleton key={index} className="h-9 w-full" />
              ))}
            </div>
          )
        ) : affected.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            暂无受影响应用：该资源未关联到任何应用系统
          </p>
        ) : (
          <ul className="flex flex-col gap-2">
            {affected.map((item) => (
              <li
                key={`${item.app_id}-${item.path ?? ""}`}
                className="flex flex-col gap-1 rounded-lg border px-3 py-2 text-xs"
              >
                <Link
                  href={`/applications?app=${encodeURIComponent(item.app_id)}`}
                  className="w-fit font-medium text-primary hover:underline"
                >
                  {attrText(item.app_name)}
                </Link>
                {item.path ? (
                  <p className="break-all text-muted-foreground">
                    链路：{item.path}
                  </p>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}
