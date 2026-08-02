import * as React from "react"

import { cn } from "@workspace/ui/lib/utils"

/** 原生复选框，可直接配合 react-hook-form 的 register 使用 */
function Checkbox({ className, ...props }: Omit<React.ComponentProps<"input">, "type">) {
  return (
    <input
      type="checkbox"
      data-slot="checkbox"
      className={cn(
        "size-4 shrink-0 cursor-pointer rounded-sm accent-primary disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  )
}

export { Checkbox }
