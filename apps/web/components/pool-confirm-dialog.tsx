"use client"

// 发现池确认入库对话框：选择目标模型（候选模型预选）+ 属性动态行编辑，提交 POST confirm

import { useEffect, useState } from "react"
import { Controller, useFieldArray, useForm } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { z } from "zod"
import { Plus as PlusIcon, Trash2 as Trash2Icon } from "lucide-react"

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
import { ApiError, confirmPoolItem, listModels, type Model, type PoolItem } from "@/lib/api"

const ATTR_CODE_PATTERN = /^[a-z][a-z0-9_]*$/

const confirmFormSchema = z
  .object({
    model_id: z.string().min(1, "请选择目标模型"),
    attributes: z.array(
      z.object({
        key: z
          .string()
          .trim()
          .min(1, "请输入属性编码")
          .regex(ATTR_CODE_PATTERN, "编码须以小写字母开头，仅含小写字母、数字、下划线"),
        value: z.string(),
      }),
    ),
  })
  .superRefine((values, ctx) => {
    const seen = new Set<string>()
    values.attributes.forEach((attr, index) => {
      if (attr.key && seen.has(attr.key)) {
        ctx.addIssue({ code: "custom", message: "属性编码重复", path: ["attributes", index, "key"] })
      }
      seen.add(attr.key)
    })
  })

type ConfirmFormValues = z.infer<typeof confirmFormSchema>

/** 将属性值展示文本转回提交值：形如数字/布尔/null/JSON 的按 JSON 解析，其余按字符串 */
function coerceValue(text: string): unknown {
  const trimmed = text.trim()
  if (trimmed === "") return ""
  if (/^(-?\d+(\.\d+)?([eE][+-]?\d+)?|true|false|null|\[.*\]|\{.*\})$/s.test(trimmed)) {
    try {
      return JSON.parse(trimmed) as unknown
    } catch {
      // 解析失败按原始字符串提交
    }
  }
  return text
}

function toFormValues(record: PoolItem): ConfirmFormValues {
  return {
    model_id: "",
    attributes: Object.entries(record.attributes).map(([key, value]) => ({
      key,
      value: typeof value === "string" ? value : JSON.stringify(value),
    })),
  }
}

function FieldError({ message }: { message?: string }) {
  if (!message) return null
  return <p className="text-xs text-destructive">{message}</p>
}

interface PoolConfirmDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 待确认的发现池条目 */
  record: PoolItem | null
  /** 确认成功后的回调（由父组件刷新列表） */
  onConfirmed: () => void
}

export function PoolConfirmDialog({ open, onOpenChange, record, onConfirmed }: PoolConfirmDialogProps) {
  const [models, setModels] = useState<Model[]>([])
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [submitError, setSubmitError] = useState<string | null>(null)

  const form = useForm<ConfirmFormValues>({
    resolver: zodResolver(confirmFormSchema),
    defaultValues: { model_id: "", attributes: [] },
  })
  const attrFields = useFieldArray({ control: form.control, name: "attributes" })

  // 打开时加载模型列表，并按候选模型编码预选目标模型
  useEffect(() => {
    if (!open || !record) return
    form.reset(toFormValues(record))
    setSubmitError(null)
    setModelsError(null)
    let cancelled = false
    listModels({ page: 1, page_size: 200 })
      .then((res) => {
        if (cancelled) return
        setModels(res.items)
        const candidate = res.items.find((model) => model.code === record.model_candidate)
        if (candidate) form.setValue("model_id", candidate.id)
      })
      .catch((err) => {
        if (cancelled) return
        setModels([])
        setModelsError(err instanceof ApiError ? err.message : "加载模型列表失败")
      })
    return () => {
      cancelled = true
    }
  }, [open, record, form])

  const onSubmit = form.handleSubmit(async (values) => {
    if (!record) return
    setSubmitError(null)
    const attributes: Record<string, unknown> = {}
    for (const attr of values.attributes) {
      attributes[attr.key] = coerceValue(attr.value)
    }
    try {
      await confirmPoolItem(record.id, { model_id: values.model_id, attributes })
      onOpenChange(false)
      onConfirmed()
    } catch (err) {
      setSubmitError(err instanceof ApiError ? err.message : "确认入库失败，请稍后重试")
    }
  })

  const errors = form.formState.errors

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>确认入库</DialogTitle>
          <DialogDescription>
            候选模型：<code className="rounded-md bg-muted px-1.5 py-0.5 text-xs">{record?.model_candidate}</code>
            ，可改选其他模型；属性可增删改，值支持数字/布尔/JSON 文本，其余按字符串入库。
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-5">
          <div className="flex flex-col gap-1.5">
            <Label>
              目标模型<span className="text-destructive">*</span>
            </Label>
            <Controller
              control={form.control}
              name="model_id"
              render={({ field }) => (
                <Select
                  value={field.value || null}
                  onValueChange={(value) => {
                    if (value) field.onChange(value)
                  }}
                >
                  <SelectTrigger>
                    <SelectValue placeholder="选择模型">
                      {(v: string) => models.find((m) => m.id === v)?.name ?? "选择模型"}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {models.map((model) => (
                      <SelectItem key={model.id} value={model.id}>
                        {model.name}（{model.code}）
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            />
            {modelsError ? (
              <p className="text-xs text-destructive">模型列表加载失败：{modelsError}</p>
            ) : null}
            <FieldError message={errors.model_id?.message} />
          </div>

          <section className="flex flex-col gap-3">
            <div className="flex items-center justify-between">
              <h3 className="text-xs font-medium">入库属性（{attrFields.fields.length}）</h3>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => attrFields.append({ key: "", value: "" })}
              >
                <PlusIcon /> 添加属性
              </Button>
            </div>
            {attrFields.fields.length === 0 ? (
              <p className="rounded-lg border border-dashed px-3 py-4 text-center text-xs text-muted-foreground">
                暂无属性，点击「添加属性」补充
              </p>
            ) : (
              <div className="flex max-h-72 flex-col gap-2 overflow-y-auto pr-1">
                {attrFields.fields.map((field, index) => (
                  <div key={field.id} className="grid grid-cols-[1fr_2fr_auto] items-start gap-2">
                    <div className="flex flex-col gap-1">
                      <Input placeholder="属性编码" {...form.register(`attributes.${index}.key`)} />
                      <FieldError message={errors.attributes?.[index]?.key?.message} />
                    </div>
                    <Input placeholder="属性值" {...form.register(`attributes.${index}.value`)} />
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      aria-label="删除属性"
                      onClick={() => attrFields.remove(index)}
                    >
                      <Trash2Icon className="text-destructive" />
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </section>

          {submitError ? (
            <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
              {submitError}
            </p>
          ) : null}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {form.formState.isSubmitting ? "入库中…" : "确认入库"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
