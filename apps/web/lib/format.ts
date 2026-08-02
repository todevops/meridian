// 展示格式化工具

const dateTimeFormatter = new Intl.DateTimeFormat("zh-CN", {
  dateStyle: "medium",
  timeStyle: "short",
})

/** ISO 时间格式化为本地可读文本，非法或空值返回占位符 */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return "—"
  const time = new Date(iso).getTime()
  if (Number.isNaN(time)) return "—"
  return dateTimeFormatter.format(new Date(time))
}

/** 将 CI 属性值渲染为展示文本 */
export function attrText(value: unknown): string {
  if (value === null || value === undefined || value === "") return "—"
  if (typeof value === "boolean") return value ? "是" : "否"
  if (typeof value === "string" || typeof value === "number") return String(value)
  if (Array.isArray(value)) {
    const parts = value.map((item) => attrText(item)).filter((part) => part !== "—")
    return parts.length > 0 ? parts.join("、") : "—"
  }
  return JSON.stringify(value)
}

/** 按候选编码顺序取第一个非空属性值的展示文本（不同来源上报的属性编码可能不同，做兜底匹配） */
export function pickAttr(attrs: Record<string, unknown>, codes: string[]): string {
  for (const code of codes) {
    const text = attrText(attrs[code])
    if (text !== "—") return text
  }
  return "—"
}
