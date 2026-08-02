"use client"

// 机柜挂载对话框：选择待挂载 CI（网络设备/物理机）+ U 位 + 高度，409 冲突红字提示

import { useCallback, useEffect, useState } from "react"
import { Controller, useForm } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { z } from "zod"

import { Button } from "@workspace/ui/components/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import {
  ApiError,
  listCIs,
  listModels,
  mountRackUnit,
  type CI,
} from "@/lib/api"
import { pickAttr } from "@/lib/format"

/** 可上柜的 CI 所属模型编码（网络设备/物理机） */
const MOUNTABLE_MODEL_CODES = ["network_device", "physical_server"] as const

const CI_NAME_CODES = ["hostname", "name", "ident", "ip"]

const POSITIVE_INT = /^\d+$/

const mountFormSchema = z.object({
  ci_id: z.string().min(1, "请选择要挂载的 CI"),
  u_position: z.string().trim().regex(POSITIVE_INT, "U 位须为正整数"),
  u_height: z.string().trim().regex(POSITIVE_INT, "高度须为正整数"),
})

type MountFormValues = z.infer<typeof mountFormSchema>

/** 下拉选项：CI + 所属模型中文名 */
interface MountableOption {
  ci: CI
  modelName: string
}

function ciDisplayName(ci: CI): string {
  const name = pickAttr(ci.attributes, [...CI_NAME_CODES])
  return name === "—" ? ci.id : name
}

function FieldError({ message }: { message?: string }) {
  if (!message) return null
  return <p className="text-xs text-destructive">{message}</p>
}

interface RackMountDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  rackCiId: string
  /** 机柜总 U 数，用于边界校验 */
  uTotal: number
  /** 点击空格时预选的起始 U 位 */
  initialU: number
  /** 点击的空闲段长度（该段内可容纳的最大高度），未知时传 undefined */
  maxHeight?: number
  onMounted: () => void
}

export function RackMountDialog({
  open,
  onOpenChange,
  rackCiId,
  uTotal,
  initialU,
  maxHeight,
  onMounted,
}: RackMountDialogProps) {
  const [options, setOptions] = useState<MountableOption[]>([])
  const [optionsError, setOptionsError] = useState<string | null>(null)
  const [submitError, setSubmitError] = useState<string | null>(null)

  const form = useForm<MountFormValues>({
    resolver: zodResolver(mountFormSchema),
    defaultValues: { ci_id: "", u_position: String(initialU), u_height: "1" },
  })

  // 打开时重置表单并加载可挂载 CI 候选
  useEffect(() => {
    if (!open) return
    form.reset({ ci_id: "", u_position: String(initialU), u_height: "1" })
    setSubmitError(null)
    setOptionsError(null)
    let cancelled = false

    const loadOptions = async () => {
      try {
        // 先解析网络设备/物理机两个模型的 id，再分别拉取 CI 列表
        const modelsRes = await listModels({ page: 1, page_size: 200 })
        const targets = MOUNTABLE_MODEL_CODES.map((code) => {
          const model = modelsRes.items.find((m) => m.code === code)
          return model ? { id: model.id, name: model.name } : null
        }).filter((target): target is { id: string; name: string } => target !== null)
        if (targets.length === 0) {
          if (!cancelled) {
            setOptions([])
            setOptionsError("未找到 network_device / physical_server 模型，请先在模型管理中建模")
          }
          return
        }
        const ciLists = await Promise.all(
          targets.map(async (target) => {
            const res = await listCIs({ model_id: target.id, page: 1, page_size: 200 })
            return res.items.map((ci) => ({ ci, modelName: target.name }))
          }),
        )
        if (!cancelled) setOptions(ciLists.flat())
      } catch (err) {
        if (!cancelled) {
          setOptions([])
          setOptionsError(err instanceof ApiError ? err.message : "加载候选 CI 失败")
        }
      }
    }
    void loadOptions()
    return () => {
      cancelled = true
    }
  }, [open, initialU, form])

  const onSubmit = form.handleSubmit(async (values) => {
    setSubmitError(null)
    const uPosition = Number(values.u_position)
    const uHeight = Number(values.u_height)
    if (uPosition < 1 || uPosition > uTotal) {
      form.setError("u_position", { message: `U 位须在 1-${uTotal} 之间` })
      return
    }
    const heightCap = maxHeight ?? uTotal - uPosition + 1
    if (uHeight < 1 || uHeight > heightCap) {
      form.setError("u_height", { message: `高度须在 1-${heightCap} 之间` })
      return
    }
    try {
      await mountRackUnit(rackCiId, { ci_id: values.ci_id, u_position: uPosition, u_height: uHeight })
      onOpenChange(false)
      onMounted()
    } catch (err) {
      // 409 U 位重叠等冲突直接红字展示契约错误文案
      setSubmitError(
        err instanceof ApiError
          ? err.status === 409
            ? `挂载冲突：${err.message}`
            : err.message
          : "挂载失败，请稍后重试",
      )
    }
  })

  const errors = form.formState.errors
  const selectedLabel = useCallback(
    (ciId: string) => {
      const option = options.find((item) => item.ci.id === ciId)
      return option ? `${ciDisplayName(option.ci)}（${option.modelName}）` : "选择 CI"
    },
    [options],
  )

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>挂载设备</DialogTitle>
          <DialogDescription>
            将网络设备或物理机挂载到机柜 U{initialU} 起始的位置，U 位重叠将返回 409 冲突。
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          <div className="flex flex-col gap-1.5">
            <Label>
              待挂载 CI<span className="text-destructive">*</span>
            </Label>
            <Controller
              control={form.control}
              name="ci_id"
              render={({ field }) => (
                <Select
                  value={field.value || null}
                  onValueChange={(value) => {
                    if (value) field.onChange(value)
                  }}
                >
                  <SelectTrigger>
                    <SelectValue placeholder="选择 CI">
                      {(v: string) => selectedLabel(v)}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {options.map((option) => (
                      <SelectItem key={option.ci.id} value={option.ci.id}>
                        {ciDisplayName(option.ci)}（{option.modelName}）
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            />
            {optionsError ? <p className="text-xs text-destructive">{optionsError}</p> : null}
            {!optionsError && options.length === 0 ? (
              <p className="text-xs text-muted-foreground">候选加载中，或暂无可挂载的 CI</p>
            ) : null}
            <FieldError message={errors.ci_id?.message} />
          </div>

          <div className="grid grid-cols-2 gap-4">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="mount-u">
                起始 U 位<span className="text-destructive">*</span>
              </Label>
              <Input id="mount-u" inputMode="numeric" {...form.register("u_position")} />
              <FieldError message={errors.u_position?.message} />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="mount-height">
                占用高度（U）<span className="text-destructive">*</span>
              </Label>
              <Input id="mount-height" inputMode="numeric" {...form.register("u_height")} />
              <FieldError message={errors.u_height?.message} />
            </div>
          </div>

          {submitError ? (
            <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {submitError}
            </p>
          ) : null}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {form.formState.isSubmitting ? "挂载中…" : "挂载"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
