"use client"

// 凭据新建/编辑对话框：新建可选类型并按类型动态渲染 secret 字段；
// 编辑仅可改名称与描述（类型不可变，secret 走轮换接口）。

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
import {
  SecretFieldsEditor,
  type SecretFormShape,
} from "@/components/credential-secret-fields"
import {
  ApiError,
  createCredential,
  patchCredential,
  type Credential,
  type CredentialType,
} from "@/lib/api"
import { CREDENTIAL_TYPES, CREDENTIAL_TYPE_LABELS } from "@/lib/labels"
import {
  cleanSecret,
  defaultSecretFor,
  validateSecret,
} from "@/lib/credential-fields"

const formSchema = z
  .object({
    name: z.string().trim().min(1, "请输入凭据名称"),
    type: z.enum(CREDENTIAL_TYPES),
    description: z.string(),
    secret: z.record(z.string(), z.string()),
  })
  .superRefine((values, ctx) => {
    // 编辑模式（secret 为空且不需要录入）由对话框另行控制，这里仅校验有录入场景的必填项
    for (const issue of validateSecret(values.type, values.secret)) {
      ctx.addIssue({ code: "custom", message: issue.message, path: ["secret", issue.key] })
    }
  })

type FormValues = z.infer<typeof formSchema> & SecretFormShape

const editSchema = z.object({
  name: z.string().trim().min(1, "请输入凭据名称"),
  type: z.enum(CREDENTIAL_TYPES),
  description: z.string(),
  secret: z.record(z.string(), z.string()),
})

interface CredentialFormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 传入即为编辑模式，否则为新建 */
  credential: Credential | null
  onSaved: () => void
}

export function CredentialFormDialog({
  open,
  onOpenChange,
  credential,
  onSaved,
}: CredentialFormDialogProps) {
  const isEdit = credential !== null
  const [submitError, setSubmitError] = useState<string | null>(null)

  const form = useForm<FormValues>({
    resolver: zodResolver(isEdit ? editSchema : formSchema),
    defaultValues: {
      name: "",
      type: "vcenter",
      description: "",
      secret: defaultSecretFor("vcenter"),
    },
  })

  // 打开时按模式初始化表单
  useEffect(() => {
    if (!open) return
    setSubmitError(null)
    if (credential) {
      form.reset({
        name: credential.name,
        type: credential.type,
        description: credential.description ?? "",
        secret: {},
      })
    } else {
      form.reset({
        name: "",
        type: "vcenter",
        description: "",
        secret: defaultSecretFor("vcenter"),
      })
    }
  }, [open, credential, form])

  const currentType = form.watch("type")

  const onSubmit = form.handleSubmit(async (values) => {
    setSubmitError(null)
    try {
      if (isEdit) {
        await patchCredential(credential.id, {
          name: values.name,
          description: values.description || undefined,
        })
      } else {
        await createCredential({
          name: values.name,
          type: values.type,
          description: values.description || undefined,
          secret: cleanSecret(values.secret),
        })
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
          <DialogTitle>{isEdit ? "编辑凭据" : "新建凭据"}</DialogTitle>
          <DialogDescription>
            {isEdit
              ? "凭据类型不可变更；密文不可回读，如需更新请使用「轮换」。"
              : "凭据用于采集器接入外部系统，密文加密托管。"}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-1.5">
              <Label>
                名称<span className="text-destructive">*</span>
              </Label>
              <Input placeholder="如：生产 vCenter" {...form.register("name")} />
              {errors.name?.message && (
                <p className="text-xs text-destructive">{errors.name.message}</p>
              )}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label>
                类型<span className="text-destructive">*</span>
              </Label>
              <Controller
                control={form.control}
                name="type"
                render={({ field }) => (
                  <Select
                    value={field.value}
                    onValueChange={(value) => {
                      field.onChange(value)
                      // 切换类型时重置 secret，避免残留上一类型的字段
                      form.setValue(
                        "secret",
                        defaultSecretFor(value as CredentialType)
                      )
                    }}
                    disabled={isEdit}
                  >
                    <SelectTrigger>
                      <SelectValue>
                        {(v: CredentialType) => CREDENTIAL_TYPE_LABELS[v] ?? v}
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {CREDENTIAL_TYPES.map((t) => (
                        <SelectItem key={t} value={t}>
                          {CREDENTIAL_TYPE_LABELS[t]}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              />
            </div>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label>描述</Label>
            <Input
              placeholder="用途说明（可选）"
              {...form.register("description")}
            />
          </div>

          {!isEdit && (
            <SecretFieldsEditor
              type={currentType}
              register={form.register}
              control={form.control}
              watch={form.watch}
              errors={errors}
            />
          )}

          {submitError && (
            <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
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
