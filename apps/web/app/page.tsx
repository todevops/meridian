// 总览页：平台功能域入口导航

import Link from "next/link"
import {
  Boxes as BoxesIcon,
  Building2 as Building2Icon,
  Network as NetworkIcon,
  Radar as RadarIcon,
  Server as ServerIcon,
} from "lucide-react"

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"

const SECTIONS = [
  {
    href: "/models",
    icon: BoxesIcon,
    title: "模型管理",
    description: "定义 CI 模型的属性、校验规则与模型间关系",
  },
  {
    href: "/hosts",
    icon: ServerIcon,
    title: "主机",
    description: "n9e 心跳、vSphere、云 API 等来源自动调和建档的主机 CI",
  },
  {
    href: "/pool",
    icon: RadarIcon,
    title: "发现池",
    description: "待人工处置的发现记录：确认入库或忽略",
  },
  {
    href: "/ipam",
    icon: NetworkIcon,
    title: "IPAM 地址管理",
    description: "子网前缀与 IP 地址的登记、分配与利用率统计",
  },
  {
    href: "/dcim",
    icon: Building2Icon,
    title: "机柜",
    description: "机房机柜 U 位占用视图与设备挂载管理",
  },
] as const

export default function Page() {
  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-5 p-6">
      <header>
        <h1 className="text-xl font-semibold">CMDB 配置管理中心</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          企业级纯自研 CMDB 平台：模型引擎、发现调和、IPAM/DCIM 一体化配置管理
        </p>
      </header>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {SECTIONS.map((section) => (
          <Link key={section.href} href={section.href} className="group">
            <Card className="h-full transition-colors group-hover:border-primary/50">
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <section.icon className="size-4 text-muted-foreground transition-colors group-hover:text-primary" />
                  {section.title}
                </CardTitle>
                <CardDescription>{section.description}</CardDescription>
              </CardHeader>
              <CardContent />
            </Card>
          </Link>
        ))}
      </div>
    </div>
  )
}
