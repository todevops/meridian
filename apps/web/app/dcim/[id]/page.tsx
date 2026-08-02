// 机柜详情页（服务端壳）：解析路由参数后交给客户端组件加载 U 位矩阵

import { RackDetail } from "@/components/rack-detail"

export default async function RackDetailPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params
  return <RackDetail id={id} />
}
