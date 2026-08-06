"use client"

// 稽核规则新建/编辑对话框：声明式规则 = 模型过滤条件 + 断言表达式 + 待办文案模板。
// filter / assertion 为文本域录入，附示例占位；dry_run 演练模式只评估不生成待办。

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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  ApiError,
  createGovernanceRule,
  listModels,
  patchGovernanceRule,
  type GovernanceRule,
  type GovernanceRuleType,
  type Model,
} from "@/lib/api"

const formSchema = z.object({
  name: z.string().trim().min(1, "请输入规则名称"),
  type: z.enum(["audit", "auto_ingest"]),
  model_code: z.string().trim().min(1, "请选择目标模型"),
  filter: z.string().trim(),
  assertion: z.string().trim().min(1, "请输入断言表达式"),
  message: z.string().trim().min(1, "请输入待办文案模板"),
  enabled: z.boolean(),
  dry_run: z.boolean(),
})

type FormValues = z.infer<typeof formSchema>

interface RuleFormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 传入即为编辑模式，否则为新建 */
  rule: GovernanceRule | null
  onSaved: () => void
}

export function RuleFormDialog({
  open,
  onOpenChange,
  rule,
  onSaved,
}: RuleFormDialogProps) {
  const isEdit = rule !== null
  const [models, setModels] = useState<Model[]>([])
  const [submitError, setSubmitError] = useState<string | null>(null)

  const form = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: {
      name: "",
      type: "audit",
      model_code: "",
      filter: "",
      assertion: "",
      message: "",
      enabled: true,
      dry_run: false,
    },
  })

  // 打开时初始化表单，并加载模型下拉
  useEffect(() => {
    if (!open) return
    setSubmitError(null)
    form.reset(
      rule
        ? {
            name: rule.name,
            // 旧数据无 type 字段时按稽核回填
            type: rule.type ?? "audit",
            model_code: rule.model_code,
            filter: rule.filter,
            assertion: rule.assertion,
            message: rule.message,
            enabled: rule.enabled,
            dry_run: rule.dry_run,
          }
        : {
            name: "",
            type: "audit",
            model_code: "",
            filter: "",
            assertion: "",
            message: "",
            enabled: true,
            dry_run: false,
          }
    )
    listModels({ page: 1, page_size: 200 })
      .then((res) => setModels(res.items))
      .catch(() => setModels([]))
  }, [open, rule, form])

  const onSubmit = form.handleSubmit(async (values) => {
    setSubmitError(null)
    try {
      if (isEdit) {
        // 类型创建后不可修改，编辑时不下发 type
        const { type: _type, ...rest } = values
        await patchGovernanceRule(rule.id, rest)
      } else {
        await createGovernanceRule(values)
      }
      onOpenChange(false)
      onSaved()
    } catch (err) {
      setSubmitError(err instanceof ApiError ? err.message : "保存失败，请稍后重试")
    }
  })

  const errors = form.formState.errors

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{isEdit ? "编辑规则" : "新建规则"}</DialogTitle>
          <DialogDescription>
            规则每日定时执行：filter 圈定适用 CI，assertion 不满足即违规并生成整改待办，修复后自动关闭。
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-1.5">
              <Label>
                规则名称<span className="text-destructive">*</span>
              </Label>
              <Input placeholder="如：生产主机必须有负责人" {...form.register("name")} />
              {errors.name?.message && (
                <p className="text-xs text-destructive">{errors.name.message}</p>
              )}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label>
                目标模型<span className="text-destructive">*</span>
              </Label>
              <Controller
                control={form.control}
                name="model_code"
                render={({ field }) => (
                  <Select value={field.value} onValueChange={(v) => v && field.onChange(v)}>
                    <SelectTrigger>
                      <SelectValue>
                        {(v: string) => {
                          if (!v) return "选择模型"
                          const m = models.find((item) => item.code === v)
                          return m ? `${m.name}（${m.code}）` : v
                        }}
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {models.map((m) => (
                        <SelectItem key={m.id} value={m.code}>
                          {m.name}（{m.code}）
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
              {errors.model_code?.message && (
                <p className="text-xs text-destructive">{errors.model_code.message}</p>
              )}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label>
                规则类型<span className="text-destructive">*</span>
              </Label>
              <Controller
                control={form.control}
                name="type"
                render={({ field }) => (
                  <Select
                    value={field.value}
                    onValueChange={(v) =>
                      v && field.onChange(v as GovernanceRuleType)
                    }
                    disabled={isEdit}
                  >
                    <SelectTrigger>
                      <SelectValue>
                        {(v: GovernanceRuleType) =>
                          v === "auto_ingest" ? "自动入库白名单" : "稽核"
                        }
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="audit">稽核</SelectItem>
                      <SelectItem value="auto_ingest">
                        自动入库白名单
                      </SelectItem>
                    </SelectContent>
                  </Select>
                )}
              />
              {isEdit ? (
                <p className="text-xs text-muted-foreground">
                  规则类型创建后不可修改
                </p>
              ) : form.watch("type") === "auto_ingest" ? (
                <p className="text-xs text-muted-foreground">
                  仅对判定为新建的记录直接建档，update/conflict 不受影响
                </p>
              ) : null}
            </div>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label>过滤条件（filter）</Label>
            <Textarea
              placeholder={`示例：attributes.env == "prod" && status == "active"\n留空表示对该模型全部 CI 生效`}
              {...form.register("filter")}
            />
            <p className="text-xs text-muted-foreground">
              圈定规则适用的 CI 范围，留空则对目标模型全部 CI 生效
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label>
              断言表达式（assertion）<span className="text-destructive">*</span>
            </Label>
            <Textarea
              placeholder={`示例：attributes.owner != ""\n不满足断言的 CI 判定为违规`}
              {...form.register("assertion")}
            />
            {errors.assertion?.message && (
              <p className="text-xs text-destructive">{errors.assertion.message}</p>
            )}
          </div>

          <div className="flex flex-col gap-1.5">
            <Label>
              待办文案模板<span className="text-destructive">*</span>
            </Label>
            <Input
              placeholder="如：生产主机缺少负责人，请补充 owner 属性"
              {...form.register("message")}
            />
            {errors.message?.message && (
              <p className="text-xs text-destructive">{errors.message.message}</p>
            )}
          </div>

          <div className="flex flex-wrap items-center gap-6">
            <div className="flex items-center gap-2">
              <Controller
                control={form.control}
                name="enabled"
                render={({ field }) => (
                  <Switch
                    checked={field.value}
                    onCheckedChange={(checked) => field.onChange(checked)}
                    aria-label="启用规则"
                  />
                )}
              />
              <Label>启用规则</Label>
            </div>
            <div className="flex items-center gap-2">
              <Controller
                control={form.control}
                name="dry_run"
                render={({ field }) => (
                  <Switch
                    checked={field.value}
                    onCheckedChange={(checked) => field.onChange(checked)}
                    aria-label="演练模式"
                  />
                )}
              />
              <Label>演练模式（只评估不生成待办）</Label>
            </div>
          </div>

          {submitError && (
            <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
              {submitError}
            </p>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {form.formState.isSubmitting ? "保存中…" : isEdit ? "保存" : "创建"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
