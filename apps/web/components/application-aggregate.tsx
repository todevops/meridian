"use client"

// F-027 应用聚合视图：负责人/等级/业务线头卡 + 五类资源分区卡 + 依赖拓扑。
// 聚合数据由 GET /api/v1/applications/{id}/aggregate 沿关系图实时组装；
// 各资源行可跳转对应详情页（主机到 /hosts/{id}，其余到所属台账页）。

import { useCallback, useEffect, useState, type ReactNode } from "react"
import { useRouter } from "next/navigation"
import {
  AppWindow as AppWindowIcon,
  Cloud as CloudIcon,
  Container as ContainerIcon,
  Database as DatabaseIcon,
  Network as NetworkIcon,
  Server as ServerIcon,
  Waypoints as WaypointsIcon,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@workspace/ui/components/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { DependencyGraph } from "@/components/dependency-graph"
import {
  getApplicationAggregate,
  type ApplicationAggregate,
  type AppTreeApp,
  type CIStatus,
} from "@/lib/api"
import { CI_STATUS_LABELS } from "@/lib/labels"
import { attrText } from "@/lib/format"

/** 等级徽标样式：应用 L1/L2/L3 三档（与业务线 critical/high/normal 共用色阶） */
const LEVEL_STYLES: Record<string, string> = {
  critical: "bg-red-500/15 text-red-700 dark:text-red-400",
  l1: "bg-red-500/15 text-red-700 dark:text-red-400",
  high: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  l2: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  normal: "bg-muted text-muted-foreground",
  l3: "bg-muted text-muted-foreground",
}

function LevelBadge({ level }: { level: string }) {
  if (level === "—") return <Badge variant="outline">未评级</Badge>
  const className =
    LEVEL_STYLES[level.toLowerCase()] ?? "bg-muted text-muted-foreground"
  return <Badge className={className}>{level}</Badge>
}

/** 主机在线状态中文文案；契约枚举外的值兜底展示原文 */
function hostStatusLabel(status: string): string {
  if (status === "—") return status
  return CI_STATUS_LABELS[status as CIStatus] ?? status
}

interface ResourceCardProps {
  icon: ReactNode
  title: string
  description: string
  count: number
  children: ReactNode
}

/** 资源分区卡：空数据时展示占位文案，否则渲染传入的表格 */
function ResourceCard({ icon, title, description, count, children }: ResourceCardProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          {icon} {title}
          <Badge variant="secondary">{count}</Badge>
        </CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent>
        {count === 0 ? (
          <p className="text-xs text-muted-foreground">暂无数据</p>
        ) : (
          children
        )}
      </CardContent>
    </Card>
  )
}

interface ApplicationAggregateViewProps {
  appId: string
  /** 业务树上的应用摘要（头卡即时展示，聚合数据返回后以后者为准） */
  appHint?: AppTreeApp | null
  /** 所属业务线名称（来自业务树，聚合响应缺 line 字段时兜底） */
  lineName?: string
  /** 切换选中应用（依赖拓扑点击应用节点时） */
  onSelectApp: (appId: string) => void
}

export function ApplicationAggregateView({
  appId,
  appHint,
  lineName,
  onSelectApp,
}: ApplicationAggregateViewProps) {
  const router = useRouter()
  const [data, setData] = useState<ApplicationAggregate | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setData(null)
    setError(null)
    try {
      setData(await getApplicationAggregate(appId))
    } catch {
      setError("加载应用聚合数据失败")
    }
  }, [appId])

  useEffect(() => {
    void load()
  }, [load])

  const app = data?.app
  const title = attrText(app?.name ?? appHint?.name)
  const line = attrText(app?.line ?? lineName)

  return (
    <div className="flex min-w-0 flex-1 flex-col gap-5">
      {/* 头卡：负责人 / 等级 / 业务线 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex flex-wrap items-center gap-2 text-lg">
            <AppWindowIcon className="size-5 text-muted-foreground" />
            {title === "—" ? appId : title}
            <LevelBadge level={attrText(app?.level ?? appHint?.level)} />
          </CardTitle>
          <CardDescription className="flex flex-wrap items-center gap-x-4 gap-y-1">
            <span>编码：{attrText(app?.code ?? appHint?.code)}</span>
            <span>负责人：{attrText(app?.owner ?? appHint?.owner)}</span>
            <span>业务线：{line}</span>
          </CardDescription>
        </CardHeader>
      </Card>

      {error ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed py-12">
          <p className="text-xs text-destructive">{error}</p>
          <Button variant="outline" size="sm" onClick={() => void load()}>
            重试
          </Button>
        </div>
      ) : !data ? (
        <div className="flex flex-col gap-3">
          {Array.from({ length: 3 }).map((_, index) => (
            <Skeleton key={index} className="h-36 w-full" />
          ))}
        </div>
      ) : (
        <>
          <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
            {/* 部署主机 */}
            <ResourceCard
              icon={<ServerIcon className="size-4 text-muted-foreground" />}
              title="部署主机"
              description="应用 deployed_on 关系挂载的主机"
              count={data.hosts.length}
            >
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>标识</TableHead>
                    <TableHead>IP</TableHead>
                    <TableHead>在线状态</TableHead>
                    <TableHead>来源</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.hosts.map((host) => (
                    <TableRow
                      key={host.id}
                      className="cursor-pointer"
                      onClick={() => router.push(`/hosts/${host.id}`)}
                    >
                      <TableCell className="font-medium">
                        {attrText(host.ident)}
                      </TableCell>
                      <TableCell>{attrText(host.ip)}</TableCell>
                      <TableCell>{hostStatusLabel(attrText(host.status))}</TableCell>
                      <TableCell>{attrText(host.source)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </ResourceCard>

            {/* 依赖数据库与中间件 */}
            <ResourceCard
              icon={<DatabaseIcon className="size-4 text-muted-foreground" />}
              title="依赖数据库与中间件"
              description="应用依赖的 DB / 中间件实例"
              count={data.db_instances.length}
            >
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>实例地址</TableHead>
                    <TableHead>版本</TableHead>
                    <TableHead>角色</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.db_instances.map((db) => (
                    <TableRow
                      key={db.id}
                      className="cursor-pointer"
                      onClick={() => router.push("/dbms")}
                    >
                      <TableCell className="font-medium">
                        {attrText(db.instance_addr)}
                      </TableCell>
                      <TableCell>{attrText(db.version)}</TableCell>
                      <TableCell>{attrText(db.role)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </ResourceCard>

            {/* K8s 工作负载 */}
            <ResourceCard
              icon={<ContainerIcon className="size-4 text-muted-foreground" />}
              title="K8s 工作负载"
              description="经命名空间归属链挂接到应用的工作负载"
              count={data.k8s_workloads.length}
            >
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>类型</TableHead>
                    <TableHead>名称</TableHead>
                    <TableHead>命名空间</TableHead>
                    <TableHead>归属链</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.k8s_workloads.map((workload) => (
                    <TableRow
                      key={workload.id}
                      className="cursor-pointer"
                      onClick={() => router.push("/k8s")}
                    >
                      <TableCell>
                        <Badge variant="outline">{attrText(workload.kind)}</Badge>
                      </TableCell>
                      <TableCell className="font-medium">
                        {attrText(workload.name)}
                      </TableCell>
                      <TableCell>{attrText(workload.namespace)}</TableCell>
                      <TableCell className="text-muted-foreground">
                        {workload.via_namespace
                          ? `经命名空间 ${workload.via_namespace} 整挂`
                          : "直接归属"}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </ResourceCard>

            {/* 占用 IP 与前缀 */}
            <ResourceCard
              icon={<NetworkIcon className="size-4 text-muted-foreground" />}
              title="占用 IP 与前缀"
              description="部署主机占用的 IPAM 地址与所属前缀"
              count={data.ips.length}
            >
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>IP</TableHead>
                    <TableHead>所属前缀</TableHead>
                    <TableHead>关联主机</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.ips.map((ip) => (
                    <TableRow
                      key={`${ip.ip}-${ip.host_id ?? ""}`}
                      className="cursor-pointer"
                      onClick={() =>
                        router.push(ip.host_id ? `/hosts/${ip.host_id}` : "/ipam")
                      }
                    >
                      <TableCell className="font-medium">{ip.ip}</TableCell>
                      <TableCell>{attrText(ip.prefix)}</TableCell>
                      <TableCell className="text-muted-foreground">
                        {ip.host_id ? "查看主机" : "—"}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </ResourceCard>

            {/* 云资源 */}
            <ResourceCard
              icon={<CloudIcon className="size-4 text-muted-foreground" />}
              title="云资源"
              description="应用关联的云实例资源"
              count={data.clouds.length}
            >
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>云厂商</TableHead>
                    <TableHead>规格</TableHead>
                    <TableHead>可用区</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.clouds.map((cloud) => (
                    <TableRow
                      key={cloud.id}
                      className="cursor-pointer"
                      onClick={() => router.push("/cloud")}
                    >
                      <TableCell className="font-medium">
                        {attrText(cloud.provider)}
                      </TableCell>
                      <TableCell>{attrText(cloud.spec)}</TableCell>
                      <TableCell>{attrText(cloud.zone)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </ResourceCard>
          </div>

          {/* 依赖拓扑：应用↔应用 / 应用↔DB，两跳截断 */}
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2 text-base">
                <WaypointsIcon className="size-4 text-muted-foreground" /> 依赖拓扑
              </CardTitle>
              <CardDescription>
                应用↔应用、应用↔数据库依赖关系（两跳截断），边标签为关系码；点击应用节点可切换聚合视图
              </CardDescription>
            </CardHeader>
            <CardContent>
              <DependencyGraph appId={appId} onSelectApp={onSelectApp} />
            </CardContent>
          </Card>
        </>
      )}
    </div>
  )
}
