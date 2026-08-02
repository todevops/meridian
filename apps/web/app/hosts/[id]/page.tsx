// 主机详情页（服务端壳）：解析路由参数后交给客户端组件加载数据

import { HostDetail } from "@/components/host-detail"

export default async function HostDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params
  return <HostDetail id={id} />
}
