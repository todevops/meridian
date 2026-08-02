"use client"

// 轻量操作结果提示：固定右下角，由调用方控制展示与自动关闭

import { CircleCheck as CircleCheckIcon, CircleX as CircleXIcon } from "lucide-react"

import { cn } from "@workspace/ui/lib/utils"

export interface ToastData {
  kind: "success" | "error"
  text: string
}

export function Toast({ data }: { data: ToastData | null }) {
  if (!data) return null
  return (
    <div
      role="status"
      className={cn(
        "fixed right-6 bottom-6 z-50 flex max-w-sm items-center gap-2 rounded-lg border px-4 py-3 text-sm shadow-lg",
        data.kind === "success"
          ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400"
          : "border-destructive/30 bg-destructive/10 text-destructive"
      )}
    >
      {data.kind === "success" ? (
        <CircleCheckIcon className="size-4 shrink-0" />
      ) : (
        <CircleXIcon className="size-4 shrink-0" />
      )}
      <span>{data.text}</span>
    </div>
  )
}
