// 台账页共用的 CI 拉取辅助：按编码解析模型 id、分页拉全量 CI

import { listCIs, listModels, type CI, type ListCIsParams } from "@/lib/api"

/** 拉全量的安全上限（页数 × 页大小），防止异常数据导致死循环 */
const MAX_PAGES = 10
const PAGE_SIZE = 100

/**
 * 按模型编码解析模型 uuid；找不到时回退为字面量编码，
 * 兼容后端同时支持按编码过滤的实现（与主机列表页同一策略）。
 */
export async function resolveModelId(code: string): Promise<string> {
  try {
    const res = await listModels({ page_size: 100 })
    const hit = res.items.find((model) => model.code === code)
    return hit ? hit.id : code
  } catch {
    return code
  }
}

/** 分页拉取某模型的全部 CI（最多 MAX_PAGES 页），用于客户端聚合/筛选的台账页 */
export async function listAllCIs(
  params: ListCIsParams = {},
): Promise<CI[]> {
  const items: CI[] = []
  for (let page = 1; page <= MAX_PAGES; page += 1) {
    const res = await listCIs({ ...params, page, page_size: PAGE_SIZE })
    items.push(...res.items)
    if (items.length >= res.total || res.items.length < PAGE_SIZE) break
  }
  return items
}
