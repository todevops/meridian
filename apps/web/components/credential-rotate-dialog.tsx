"use client"

// 凭据轮换对话框：按凭据类型重新录入完整 secret，提交 POST rotate

import { useEffect, useState } from "react"
import { useForm } from "react-hook-form"
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
import {
  SecretFieldsEditor,
  type SecretFormShape,
} from "@/components/credential-secret-fields"
import { ApiError, rotateCredential, type Credential } from "@/lib/api"
import { CREDENTIAL_TYPE_LABELS } from "@/lib/labels"
import {
  cleanSecret,
  defaultSecretFor,
  validateSecret,
} from "@/lib/credential-fields"

const rotateSchema = z.object({ secret: z.record(z.string(), z.string()) })

type RotateValues = z.infer<typeof rotateSchema> & SecretFormShape

interface CredentialRotateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  credential: Credential | null
  onRotated: () => void
}

export function CredentialRotateDialog({
  open,
  onOpenChange,
  credential,
  onRotated,
}: CredentialRotateDialogProps) {
  const [submitError, setSubmitError] = useState<string | null>(null)

  const form = useForm<RotateValues>({
    resolver: zodResolver(rotateSchema),
    defaultValues: { secret: {} },
  })

  // 打开时按凭据类型初始化 secret 字段
  useEffect(() => {
    if (!open || !credential) return
    setSubmitError(null)
    form.reset({ secret: defaultSecretFor(credential.type) })
  }, [open, credential, form])

  const onSubmit = form.handleSubmit(async (values) => {
    if (!credential) return
    setSubmitError(null)
    // schema 无法感知外部类型，提交前按类型执行必填校验
    const issues = validateSecret(credential.type, values.secret)
    if (issues.length > 0) {
      for (const issue of issues) {
        form.setError(`secret.${issue.key}`, { message: issue.message })
      }
      return
    }
    try {
      await rotateCredential(credential.id, cleanSecret(values.secret))
      onOpenChange(false)
      onRotated()
    } catch (err) {
      setSubmitError(err instanceof ApiError ? err.message : "轮换失败，请稍后重试")
    }
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>轮换凭据</DialogTitle>
          <DialogDescription>
            重新录入「{credential?.name}」（
            {credential ? CREDENTIAL_TYPE_LABELS[credential.type] : ""}
            ）的完整密文，提交后旧密文立即失效。
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          {credential && (
            <SecretFieldsEditor
              type={credential.type}
              register={form.register}
              control={form.control}
              watch={form.watch}
              errors={form.formState.errors}
            />
          )}

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
              {form.formState.isSubmitting ? "轮换中…" : "确认轮换"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
