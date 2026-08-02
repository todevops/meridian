// 契约枚举的中文展示文案，集中维护

import type {
  AttributeType,
  CIStatus,
  PoolStatus,
  ReconcileAction,
  RelationCardinality,
  RelationDirection,
} from "@/lib/api"

export const ATTRIBUTE_TYPES: AttributeType[] = ["string", "number", "bool", "enum", "ip", "date"]

export const ATTR_TYPE_LABELS: Record<AttributeType, string> = {
  string: "字符串",
  number: "数字",
  bool: "布尔",
  enum: "枚举",
  ip: "IP",
  date: "日期",
}

export const CI_STATUSES: CIStatus[] = ["discovered", "active", "retired"]

export const CI_STATUS_LABELS: Record<CIStatus, string> = {
  discovered: "已发现",
  active: "在用",
  retired: "已退役",
}

export const RELATION_CARDINALITIES: RelationCardinality[] = [
  "one_to_one",
  "one_to_many",
  "many_to_many",
]

export const CARDINALITY_LABELS: Record<RelationCardinality, string> = {
  one_to_one: "一对一",
  one_to_many: "一对多",
  many_to_many: "多对多",
}

export const RELATION_DIRECTIONS: RelationDirection[] = ["outgoing", "incoming"]

export const DIRECTION_LABELS: Record<RelationDirection, string> = {
  outgoing: "出向",
  incoming: "入向",
}

export const POOL_STATUSES: PoolStatus[] = ["pending", "confirmed", "ignored"]

export const POOL_STATUS_LABELS: Record<PoolStatus, string> = {
  pending: "待处理",
  confirmed: "已入库",
  ignored: "已忽略",
}

export const RECONCILE_ACTION_LABELS: Record<ReconcileAction, string> = {
  create: "新建",
  update: "更新",
  conflict: "冲突",
}

/** IPAM IP 状态的中文文案；契约枚举以外的新值兜底展示原文 */
export const IP_STATUS_LABELS: Record<string, string> = {
  available: "可用",
  free: "空闲",
  used: "已用",
  allocated: "已分配",
  reserved: "保留",
  disabled: "禁用",
}

export function ipStatusLabel(status: string): string {
  return IP_STATUS_LABELS[status] ?? status
}
