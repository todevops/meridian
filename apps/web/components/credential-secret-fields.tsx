"use client"

// 凭据 secret 动态录入区：按类型（含 SNMP 版本）渲染字段，
// 新建与轮换对话框共用；密文一律 type=password 或文本域，界面明示不可回读。

import {
  Controller,
  type Control,
  type FieldErrors,
  type FieldPath,
  type UseFormRegister,
  type UseFormWatch,
} from "react-hook-form"

import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import {
  secretFieldsFor,
  SNMP_VERSIONS,
  type SnmpVersion,
} from "@/lib/credential-fields"
import type { CredentialType } from "@/lib/api"

/** 含 secret 字段的表单形状（新建/轮换表单均满足） */
export interface SecretFormShape {
  secret: Record<string, string>
}

interface SecretFieldsEditorProps<T extends SecretFormShape> {
  type: CredentialType
  register: UseFormRegister<T>
  control: Control<T>
  watch: UseFormWatch<T>
  errors: FieldErrors<T>
}

function secretError<T extends SecretFormShape>(
  errors: FieldErrors<T>,
  key: string
): string | undefined {
  const secretErrors = errors.secret as Record<string, { message?: string }> | undefined
  return secretErrors?.[key]?.message
}

export function SecretFieldsEditor<T extends SecretFormShape>({
  type,
  register,
  control,
  watch,
  errors,
}: SecretFieldsEditorProps<T>) {
  const snmpVersion =
    type === "snmp"
      ? ((watch("secret.version" as FieldPath<T>) as unknown as SnmpVersion) ??
        "v2c")
      : "v2c"
  const fields = secretFieldsFor(type, snmpVersion)

  return (
    <section className="flex flex-col gap-3">
      <p className="rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-400">
        密文提交后不可回读，仅可通过「轮换」更新
      </p>

      {type === "snmp" && (
        <div className="flex flex-col gap-1.5">
          <Label>SNMP 版本</Label>
          <Controller
            control={control}
            name={"secret.version" as FieldPath<T>}
            render={({ field }) => (
              <Select
                value={(field.value as unknown as string) || "v2c"}
                onValueChange={(value) => {
                  // 切换版本时重置为对应版本的初始字段，避免残留旧版本密文
                  field.onChange(value)
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SNMP_VERSIONS.map((v) => (
                    <SelectItem key={v} value={v}>
                      {v}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          />
        </div>
      )}

      {fields.map((field) => (
        <div key={field.key} className="flex flex-col gap-1.5">
          <Label>
            {field.label}
            {field.required && <span className="text-destructive">*</span>}
          </Label>
          {field.kind === "textarea" ? (
            <Textarea
              className="font-mono text-xs"
              rows={5}
              placeholder={field.placeholder}
              {...register(`secret.${field.key}` as FieldPath<T>)}
            />
          ) : (
            <Input
              type={field.kind === "password" ? "password" : "text"}
              autoComplete="off"
              placeholder={field.placeholder}
              {...register(`secret.${field.key}` as FieldPath<T>)}
            />
          )}
          {secretError(errors, field.key) && (
            <p className="text-xs text-destructive">
              {secretError(errors, field.key)}
            </p>
          )}
        </div>
      ))}
    </section>
  )
}
