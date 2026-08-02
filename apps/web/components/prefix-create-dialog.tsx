"use client"

// 新建前缀对话框：CIDR 客户端格式校验（服务端仍会复核 400/409），可选父前缀

import { useEffect, useState } from "react"
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
import { ApiError, createPrefix, type IpamPrefix } from "@/lib/api"

/** IPv4 CIDR 格式（如 10.0.0.0/24），仅做格式校验，重叠与合法性由服务端裁定 */
const CIDR_PATTERN =
  /^((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\/(3[0-2]|[12]?\d)$/

const NO_PARENT = "__none__"

const prefixFormSchema = z.object({
  cidr: z
    .string()
    .trim()
    .min(1, "请输入 CIDR")
    .regex(CIDR_PATTERN, "CIDR 格式非法，应为 IPv4 地址/前缀长度，如 10.0.0.0/24"),
  name: z.string().trim().min(1, "请输入名称"),
  vlan_id: z.string().trim(),
  description: z.string().trim(),
  parent_id: z.string(),
})

type PrefixFormValues = z.infer<typeof prefixFormSchema>

function FieldError({ message }: { message?: string }) {
  if (!message) return null
  return <p className="text-xs text-destructive">{message}</p>
}

interface PrefixCreateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 可作为父前缀的候选列表 */
  prefixes: IpamPrefix[]
  onSaved: () => void
}

export function PrefixCreateDialog({ open, onOpenChange, prefixes, onSaved }: PrefixCreateDialogProps) {
  const [submitError, setSubmitError] = useState<string | null>(null)

  const form = useForm<PrefixFormValues>({
    resolver: zodResolver(prefixFormSchema),
    defaultValues: { cidr: "", name: "", vlan_id: "", description: "", parent_id: NO_PARENT },
  })

  useEffect(() => {
    if (open) {
      form.reset({ cidr: "", name: "", vlan_id: "", description: "", parent_id: NO_PARENT })
      setSubmitError(null)
    }
  }, [open, form])

  const onSubmit = form.handleSubmit(async (values) => {
    setSubmitError(null)
    const vlanId = values.vlan_id === "" ? undefined : Number(values.vlan_id)
    if (vlanId !== undefined && (!Number.isInteger(vlanId) || vlanId < 1 || vlanId > 4094)) {
      form.setError("vlan_id", { message: "VLAN ID 须为 1-4094 的整数" })
      return
    }
    try {
      await createPrefix({
        cidr: values.cidr,
        name: values.name,
        ...(vlanId !== undefined ? { vlan_id: vlanId } : {}),
        ...(values.description ? { description: values.description } : {}),
        ...(values.parent_id !== NO_PARENT ? { parent_id: values.parent_id } : {}),
      })
      onOpenChange(false)
      onSaved()
    } catch (err) {
      // 400 CIDR 非法、409 同级重叠等错误由服务端返回，直接展示契约错误文案
      setSubmitError(err instanceof ApiError ? err.message : "创建前缀失败，请稍后重试")
    }
  })

  const errors = form.formState.errors

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>新建前缀</DialogTitle>
          <DialogDescription>
            登记一个子网前缀。CIDR 非法将返回 400，与同级前缀重叠将返回 409。
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="prefix-cidr">
                CIDR<span className="text-destructive">*</span>
              </Label>
              <Input id="prefix-cidr" placeholder="如：10.0.0.0/24" {...form.register("cidr")} />
              <FieldError message={errors.cidr?.message} />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="prefix-name">
                名称<span className="text-destructive">*</span>
              </Label>
              <Input id="prefix-name" placeholder="如：生产业务网段" {...form.register("name")} />
              <FieldError message={errors.name?.message} />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="prefix-vlan">VLAN ID</Label>
              <Input
                id="prefix-vlan"
                inputMode="numeric"
                placeholder="可选，1-4094"
                {...form.register("vlan_id")}
              />
              <FieldError message={errors.vlan_id?.message} />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label>父前缀</Label>
              <Controller
                control={form.control}
                name="parent_id"
                render={({ field }) => (
                  <Select
                    value={field.value}
                    onValueChange={(value) => {
                      if (value) field.onChange(value)
                    }}
                  >
                    <SelectTrigger>
                      <SelectValue>
                        {(v: string) =>
                          v === NO_PARENT
                            ? "无（顶级前缀）"
                            : (prefixes.find((p) => p.id === v)?.cidr ?? v)
                        }
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={NO_PARENT}>无（顶级前缀）</SelectItem>
                      {prefixes.map((prefix) => (
                        <SelectItem key={prefix.id} value={prefix.id}>
                          {prefix.cidr}（{prefix.name}）
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="prefix-desc">描述</Label>
            <Input id="prefix-desc" placeholder="可选" {...form.register("description")} />
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
              {form.formState.isSubmitting ? "创建中…" : "创建"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
