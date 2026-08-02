"use client"

// 采集任务新建/编辑对话框：collector_type 支持 builtin:n9e-consumer 与 exec:<binary>，
// exec 模式额外录入 binary/args/timeout（存入 config）；可选关联凭据。

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
import {
  ApiError,
  createDiscoveryTask,
  listCredentials,
  patchDiscoveryTask,
  type Credential,
  type DiscoveryTask,
} from "@/lib/api"
import { CREDENTIAL_TYPE_LABELS } from "@/lib/labels"

/** 内置采集器选项（collector_type 真值） */
const BUILTIN_COLLECTORS = ["builtin:n9e-consumer"] as const

/** 凭据下拉「不关联」的哨兵值（Select 不允许空串 value） */
const CREDENTIAL_NONE = "__none__"

const formSchema = z
  .object({
    name: z.string().trim().min(1, "请输入任务名称"),
    mode: z.enum(["builtin", "exec"]),
    binary: z.string().trim(),
    args: z.string(),
    timeout: z.string(),
    credential_id: z.string(),
    interval_seconds: z.string().trim().min(1, "请输入调度频率"),
    enabled: z.boolean(),
  })
  .superRefine((values, ctx) => {
    if (values.mode === "exec") {
      if (!values.binary) {
        ctx.addIssue({ code: "custom", message: "请输入采集器二进制名", path: ["binary"] })
      } else if (!/^[a-zA-Z0-9._-]+$/.test(values.binary)) {
        ctx.addIssue({ code: "custom", message: "二进制名仅允许字母、数字、点、横线、下划线", path: ["binary"] })
      }
      if (values.timeout && !/^\d+$/.test(values.timeout.trim())) {
        ctx.addIssue({ code: "custom", message: "超时须为正整数（秒）", path: ["timeout"] })
      }
    }
    if (!/^\d+$/.test(values.interval_seconds) || Number(values.interval_seconds) < 10) {
      ctx.addIssue({ code: "custom", message: "频率须为不小于 10 的整数（秒）", path: ["interval_seconds"] })
    }
  })

type FormValues = z.infer<typeof formSchema>

interface TaskFormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 传入即为编辑模式，否则为新建 */
  task: DiscoveryTask | null
  onSaved: () => void
}

export function TaskFormDialog({
  open,
  onOpenChange,
  task,
  onSaved,
}: TaskFormDialogProps) {
  const isEdit = task !== null
  const [credentials, setCredentials] = useState<Credential[]>([])
  const [submitError, setSubmitError] = useState<string | null>(null)

  const form = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: {
      name: "",
      mode: "builtin",
      binary: "",
      args: "",
      timeout: "",
      credential_id: CREDENTIAL_NONE,
      interval_seconds: "300",
      enabled: true,
    },
  })

  // 打开时初始化表单，并加载凭据下拉
  useEffect(() => {
    if (!open) return
    setSubmitError(null)
    if (task) {
      const isExec = task.collector_type.startsWith("exec:")
      const configBinary =
        typeof task.config?.binary === "string" ? task.config.binary : ""
      const configArgs =
        typeof task.config?.args === "string" ? task.config.args : ""
      const configTimeout =
        typeof task.config?.timeout === "number" ? String(task.config.timeout) : ""
      form.reset({
        name: task.name,
        mode: isExec ? "exec" : "builtin",
        binary: isExec
          ? task.collector_type.slice("exec:".length) || configBinary
          : "",
        args: configArgs,
        timeout: configTimeout,
        credential_id: task.credential_id ?? CREDENTIAL_NONE,
        interval_seconds: String(task.interval_seconds),
        enabled: task.enabled,
      })
    } else {
      form.reset({
        name: "",
        mode: "builtin",
        binary: "",
        args: "",
        timeout: "",
        credential_id: CREDENTIAL_NONE,
        interval_seconds: "300",
        enabled: true,
      })
    }
    listCredentials({ page: 1, page_size: 200 })
      .then((res) => setCredentials(res.items))
      .catch(() => setCredentials([]))
  }, [open, task, form])

  const mode = form.watch("mode")

  const onSubmit = form.handleSubmit(async (values) => {
    setSubmitError(null)
    const credentialId =
      values.credential_id === CREDENTIAL_NONE ? undefined : values.credential_id
    const intervalSeconds = Number(values.interval_seconds)
    try {
      if (values.mode === "exec") {
        const config: Record<string, unknown> = { binary: values.binary }
        if (values.args.trim()) config.args = values.args.trim()
        if (values.timeout.trim()) config.timeout = Number(values.timeout.trim())
        const body = {
          name: values.name,
          collector_type: `exec:${values.binary}`,
          credential_id: credentialId,
          interval_seconds: intervalSeconds,
          enabled: values.enabled,
          config,
        }
        if (isEdit) await patchDiscoveryTask(task.id, body)
        else await createDiscoveryTask(body)
      } else {
        const body = {
          name: values.name,
          collector_type: BUILTIN_COLLECTORS[0] as string,
          credential_id: credentialId,
          interval_seconds: intervalSeconds,
          enabled: values.enabled,
          // 编辑保留既有 config，新建给空对象
          config: isEdit ? task.config : {},
        }
        if (isEdit) await patchDiscoveryTask(task.id, body)
        else await createDiscoveryTask(body)
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
          <DialogTitle>{isEdit ? "编辑任务" : "新建任务"}</DialogTitle>
          <DialogDescription>
            采集任务按调度周期运行，将外部系统数据上报为发现记录进入调和管道。
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-1.5">
              <Label>
                任务名称<span className="text-destructive">*</span>
              </Label>
              <Input placeholder="如：n9e 主机心跳消费" {...form.register("name")} />
              {errors.name?.message && (
                <p className="text-xs text-destructive">{errors.name.message}</p>
              )}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label>
                采集器类型<span className="text-destructive">*</span>
              </Label>
              <Controller
                control={form.control}
                name="mode"
                render={({ field }) => (
                  <Select
                    value={field.value}
                    onValueChange={(value) => field.onChange(value)}
                    disabled={isEdit}
                  >
                    <SelectTrigger>
                      <SelectValue>
                        {(v: string) =>
                          v === "exec"
                            ? "外部采集器（exec）"
                            : "内置：n9e 消费器"
                        }
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="builtin">
                        内置：n9e 消费器
                      </SelectItem>
                      <SelectItem value="exec">外部采集器（exec）</SelectItem>
                    </SelectContent>
                  </Select>
                )}
              />
              {isEdit && (
                <p className="text-xs text-muted-foreground">
                  任务创建后采集器类型不可变更
                </p>
              )}
            </div>
          </div>

          {mode === "exec" && (
            <div className="grid grid-cols-3 gap-3">
              <div className="flex flex-col gap-1.5">
                <Label>
                  二进制<span className="text-destructive">*</span>
                </Label>
                <Input placeholder="如：vcenter" {...form.register("binary")} />
                {errors.binary?.message && (
                  <p className="text-xs text-destructive">{errors.binary.message}</p>
                )}
              </div>
              <div className="flex flex-col gap-1.5">
                <Label>启动参数</Label>
                <Input placeholder="可选" {...form.register("args")} />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label>超时（秒）</Label>
                <Input placeholder="可选" {...form.register("timeout")} />
                {errors.timeout?.message && (
                  <p className="text-xs text-destructive">{errors.timeout.message}</p>
                )}
              </div>
            </div>
          )}

          <div className="grid grid-cols-3 gap-3">
            <div className="col-span-2 flex flex-col gap-1.5">
              <Label>关联凭据</Label>
              <Controller
                control={form.control}
                name="credential_id"
                render={({ field }) => (
                  <Select
                    value={field.value}
                    onValueChange={(value) => field.onChange(value)}
                  >
                    <SelectTrigger>
                      <SelectValue>
                        {(v: string) => {
                          if (v === CREDENTIAL_NONE) return "不关联"
                          const cred = credentials.find((c) => c.id === v)
                          return cred
                            ? `${cred.name}（${CREDENTIAL_TYPE_LABELS[cred.type] ?? cred.type}）`
                            : "不关联"
                        }}
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={CREDENTIAL_NONE}>不关联</SelectItem>
                      {credentials.map((cred) => (
                        <SelectItem key={cred.id} value={cred.id}>
                          {cred.name}（{CREDENTIAL_TYPE_LABELS[cred.type] ?? cred.type}）
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label>
                调度频率（秒）<span className="text-destructive">*</span>
              </Label>
              <Input
                type="number"
                min={10}
                step={1}
                {...form.register("interval_seconds")}
              />
              {errors.interval_seconds?.message && (
                <p className="text-xs text-destructive">
                  {errors.interval_seconds.message}
                </p>
              )}
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Controller
              control={form.control}
              name="enabled"
              render={({ field }) => (
                <Switch
                  checked={field.value}
                  onCheckedChange={(checked) => field.onChange(checked)}
                  aria-label="启用任务"
                />
              )}
            />
            <Label>创建后立即参与调度</Label>
          </div>

          {submitError && (
            <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {submitError}
            </p>
          )}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
            >
              取消
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {form.formState.isSubmitting
                ? "保存中…"
                : isEdit
                  ? "保存"
                  : "创建"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
