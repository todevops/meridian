import * as React from "react"

import { cn } from "@workspace/ui/lib/utils"

function Label({ className, ...props }: React.ComponentProps<"label">) {
  return (
    <label
      data-slot="label"
      className={cn(
        "flex items-center gap-1.5 text-xs leading-none font-medium select-none",
        className,
      )}
      {...props}
    />
  )
}

export { Label }
