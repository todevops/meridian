"use client"

// 右侧抽屉：基于 Base UI Dialog 原语实现，与 dialog.tsx 同套的遮罩与动画行为

import * as React from "react"
import { Dialog as DialogPrimitive } from "@base-ui/react/dialog"
import { X as XIcon } from "lucide-react"

import { cn } from "@workspace/ui/lib/utils"

function Drawer({ ...props }: React.ComponentProps<typeof DialogPrimitive.Root>) {
  return <DialogPrimitive.Root data-slot="drawer" {...props} />
}

function DrawerContent({
  className,
  children,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Popup>) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Backdrop
        data-slot="drawer-backdrop"
        className="fixed inset-0 z-50 bg-black/50 transition-opacity data-[open]:animate-in data-[open]:fade-in-0 data-[closed]:animate-out data-[closed]:fade-out-0"
      />
      <DialogPrimitive.Viewport className="fixed inset-0 z-50 flex justify-end">
        <DialogPrimitive.Popup
          data-slot="drawer-content"
          className={cn(
            "flex h-full w-full max-w-xl flex-col gap-4 overflow-y-auto border-l bg-background p-6 shadow-lg transition-transform data-[open]:animate-in data-[open]:slide-in-from-right data-[closed]:animate-out data-[closed]:slide-out-to-right",
            className,
          )}
          {...props}
        >
          {children}
          <DialogPrimitive.Close
            data-slot="drawer-close-button"
            aria-label="关闭"
            className="absolute top-4 right-4 rounded-md p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <XIcon className="size-4" />
          </DialogPrimitive.Close>
        </DialogPrimitive.Popup>
      </DialogPrimitive.Viewport>
    </DialogPrimitive.Portal>
  )
}

function DrawerHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="drawer-header"
      className={cn("flex flex-col gap-1.5 text-left", className)}
      {...props}
    />
  )
}

function DrawerTitle({
  className,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Title>) {
  return (
    <DialogPrimitive.Title
      data-slot="drawer-title"
      className={cn("text-base leading-none font-semibold", className)}
      {...props}
    />
  )
}

function DrawerDescription({
  className,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Description>) {
  return (
    <DialogPrimitive.Description
      data-slot="drawer-description"
      className={cn("text-sm text-muted-foreground", className)}
      {...props}
    />
  )
}

export { Drawer, DrawerContent, DrawerHeader, DrawerTitle, DrawerDescription }
