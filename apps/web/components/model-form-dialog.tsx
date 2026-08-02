"use client"

// 模型新建/编辑对话框：基本信息 + 属性子表单 + 关系子表单（useFieldArray 动态增删行）

import { useEffect, useMemo, useState } from "react"
import { Controller, useFieldArray, useForm } from "react-hook-form"
import { zodResolver } from "@hookform/resolvers/zod"
import { z } from "zod"
import { Plus as PlusIcon, Trash2 as Trash2Icon } from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Checkbox } from "@/components/ui/checkbox"
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
  createModel,
  patchModel,
  type AttributeDefinition,
  type AttributeType,
  type Model,
  type RelationCardinality,
  type RelationDefinition,
  type RelationDirection,
} from "@/lib/api"
import {
  ATTR_TYPE_LABELS,
  ATTRIBUTE_TYPES,
  CARDINALITY_LABELS,
  DIRECTION_LABELS,
  RELATION_CARDINALITIES,
  RELATION_DIRECTIONS,
} from "@/lib/labels"

const CODE_PATTERN = /^[a-z][a-z0-9_]*$/
const CODE_MESSAGE = "编码须以小写字母开头，仅含小写字母、数字、下划线"

const attributeFormSchema = z
  .object({
    name: z.string().trim().min(1, "请输入属性名称"),
    code: z.string().trim().min(1, "请输入属性编码").regex(CODE_PATTERN, CODE_MESSAGE),
    type: z.enum(["string", "number", "bool", "enum", "ip", "date"]),
    required: z.boolean(),
    unique: z.boolean(),
    /** 逗号分隔的枚举候选值（表单态），提交时拆分为数组 */
    enumValuesText: z.string(),
    regex: z.string(),
    source: z.string(),
  })
  .superRefine((attr, ctx) => {
    if (attr.type === "enum" && splitEnumValues(attr.enumValuesText).length === 0) {
      ctx.addIssue({ code: "custom", message: "枚举类型至少填写一个候选值", path: ["enumValuesText"] })
    }
  })

const relationFormSchema = z.object({
  name: z.string().trim().min(1, "请输入关系名称"),
  code: z.string().trim().min(1, "请输入关系编码").regex(CODE_PATTERN, CODE_MESSAGE),
  target_model: z.string().min(1, "请选择目标模型"),
  cardinality: z.enum(["one_to_one", "one_to_many", "many_to_many"]),
  direction: z.enum(["outgoing", "incoming"]),
})

const modelFormSchema = z
  .object({
    name: z.string().trim().min(1, "请输入模型名称"),
    code: z.string().trim().min(1, "请输入模型编码").regex(CODE_PATTERN, CODE_MESSAGE),
    attributes: z.array(attributeFormSchema),
    relations: z.array(relationFormSchema),
  })
  .superRefine((values, ctx) => {
    const attrCodes = new Set<string>()
    values.attributes.forEach((attr, index) => {
      if (attr.code && attrCodes.has(attr.code)) {
        ctx.addIssue({ code: "custom", message: "属性编码重复", path: ["attributes", index, "code"] })
      }
      attrCodes.add(attr.code)
    })
    const relCodes = new Set<string>()
    values.relations.forEach((rel, index) => {
      if (rel.code && relCodes.has(rel.code)) {
        ctx.addIssue({ code: "custom", message: "关系编码重复", path: ["relations", index, "code"] })
      }
      relCodes.add(rel.code)
    })
  })

type ModelFormValues = z.infer<typeof modelFormSchema>

function splitEnumValues(text: string): string[] {
  return text
    .split(/[,，]/)
    .map((item) => item.trim())
    .filter(Boolean)
}

function emptyAttribute(): ModelFormValues["attributes"][number] {
  return {
    name: "",
    code: "",
    type: "string",
    required: false,
    unique: false,
    enumValuesText: "",
    regex: "",
    source: "",
  }
}

function emptyRelation(): ModelFormValues["relations"][number] {
  return { name: "", code: "", target_model: "", cardinality: "one_to_many", direction: "outgoing" }
}

function toFormValues(model: Model | null): ModelFormValues {
  if (!model) {
    return { name: "", code: "", attributes: [], relations: [] }
  }
  return {
    name: model.name,
    code: model.code,
    attributes: model.attributes.map((attr) => ({
      name: attr.name,
      code: attr.code,
      type: attr.type,
      required: attr.required ?? false,
      unique: attr.unique ?? false,
      enumValuesText: (attr.enum_values ?? []).join(", "),
      regex: attr.regex ?? "",
      source: attr.source ?? "",
    })),
    relations: model.relations.map((rel) => ({
      name: rel.name,
      code: rel.code,
      target_model: rel.target_model,
      cardinality: rel.cardinality,
      direction: rel.direction,
    })),
  }
}

function FieldError({ message }: { message?: string }) {
  if (!message) return null
  return <p className="text-xs text-destructive">{message}</p>
}

interface ModelFormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 为 null 表示新建，否则为编辑该模型 */
  model: Model | null
  /** 已有模型编码列表，用于关系目标模型下拉 */
  modelCodes: string[]
  /** 保存成功后的回调（由父组件刷新列表） */
  onSaved: () => void
}

export function ModelFormDialog({ open, onOpenChange, model, modelCodes, onSaved }: ModelFormDialogProps) {
  const isEdit = model !== null
  const [submitError, setSubmitError] = useState<string | null>(null)

  const form = useForm<ModelFormValues>({
    resolver: zodResolver(modelFormSchema),
    defaultValues: toFormValues(null),
  })

  const attrFields = useFieldArray({ control: form.control, name: "attributes" })
  const relFields = useFieldArray({ control: form.control, name: "relations" })
  const watchedRelations = form.watch("relations")

  // 目标模型候选：已有模型编码 ∪ 表单中已填写的值（兼容目标模型已被删除的编辑场景）
  const targetModelOptions = useMemo(() => {
    const set = new Set<string>(modelCodes)
    for (const rel of watchedRelations) {
      if (rel.target_model) set.add(rel.target_model)
    }
    return Array.from(set).sort()
  }, [modelCodes, watchedRelations])

  useEffect(() => {
    if (open) {
      form.reset(toFormValues(model))
      setSubmitError(null)
    }
  }, [open, model, form])

  const onSubmit = form.handleSubmit(async (values) => {
    setSubmitError(null)
    const attributes: AttributeDefinition[] = values.attributes.map((attr) => ({
      name: attr.name,
      code: attr.code,
      type: attr.type,
      required: attr.required,
      unique: attr.unique,
      ...(attr.type === "enum" ? { enum_values: splitEnumValues(attr.enumValuesText) } : {}),
      ...(attr.regex.trim() ? { regex: attr.regex.trim() } : {}),
      ...(attr.source.trim() ? { source: attr.source.trim() } : {}),
    }))
    const relations: RelationDefinition[] = values.relations.map((rel) => ({
      name: rel.name,
      code: rel.code,
      target_model: rel.target_model,
      cardinality: rel.cardinality,
      direction: rel.direction,
    }))
    try {
      if (isEdit) {
        await patchModel(model.id, { name: values.name, attributes, relations })
      } else {
        await createModel({ name: values.name, code: values.code, attributes, relations })
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
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle>{isEdit ? `编辑模型：${model.name}` : "新建模型"}</DialogTitle>
          <DialogDescription>
            定义模型的属性与关系。属性校验规则（必填/唯一/枚举/正则）将在 CI 入库时由服务端强制执行。
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={onSubmit} className="flex flex-col gap-6">
          {/* 基本信息 */}
          <section className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="model-name">
                模型名称<span className="text-destructive">*</span>
              </Label>
              <Input id="model-name" placeholder="如：主机" {...form.register("name")} />
              <FieldError message={errors.name?.message} />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="model-code">
                模型编码<span className="text-destructive">*</span>
              </Label>
              <Input
                id="model-code"
                placeholder="如：host"
                disabled={isEdit}
                {...form.register("code")}
              />
              {isEdit ? (
                <p className="text-xs text-muted-foreground">编码创建后不可修改</p>
              ) : (
                <FieldError message={errors.code?.message} />
              )}
            </div>
          </section>

          {/* 属性子表单 */}
          <section className="flex flex-col gap-3">
            <div className="flex items-center justify-between">
              <h3 className="text-xs font-medium">属性定义（{attrFields.fields.length}）</h3>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => attrFields.append(emptyAttribute())}
              >
                <PlusIcon /> 添加属性
              </Button>
            </div>
            {attrFields.fields.length === 0 ? (
              <p className="rounded-lg border border-dashed px-3 py-4 text-center text-xs text-muted-foreground">
                暂无属性，点击「添加属性」开始定义
              </p>
            ) : (
              attrFields.fields.map((field, index) => {
                const attrType = form.watch(`attributes.${index}.type`)
                return (
                  <div key={field.id} className="flex flex-col gap-3 rounded-lg border p-3">
                    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                      <div className="flex flex-col gap-1.5">
                        <Label>名称</Label>
                        <Input placeholder="如：主机名" {...form.register(`attributes.${index}.name`)} />
                        <FieldError message={errors.attributes?.[index]?.name?.message} />
                      </div>
                      <div className="flex flex-col gap-1.5">
                        <Label>编码</Label>
                        <Input placeholder="如：hostname" {...form.register(`attributes.${index}.code`)} />
                        <FieldError message={errors.attributes?.[index]?.code?.message} />
                      </div>
                      <div className="flex flex-col gap-1.5">
                        <Label>类型</Label>
                        <Controller
                          control={form.control}
                          name={`attributes.${index}.type`}
                          render={({ field: typeField }) => (
                            <Select
                              value={typeField.value}
                              onValueChange={(value) => {
                                if (value) typeField.onChange(value)
                              }}
                            >
                              <SelectTrigger>
                                <SelectValue>{(v: AttributeType) => ATTR_TYPE_LABELS[v]}</SelectValue>
                              </SelectTrigger>
                              <SelectContent>
                                {ATTRIBUTE_TYPES.map((t) => (
                                  <SelectItem key={t} value={t}>
                                    {ATTR_TYPE_LABELS[t]}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          )}
                        />
                      </div>
                      <div className="flex flex-col gap-1.5">
                        <Label>来源</Label>
                        <Input placeholder="如：n9e / 人工" {...form.register(`attributes.${index}.source`)} />
                      </div>
                    </div>
                    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                      {attrType === "enum" ? (
                        <div className="col-span-2 flex flex-col gap-1.5">
                          <Label>枚举值（逗号分隔）</Label>
                          <Input
                            placeholder="如：生产, 测试, 开发"
                            {...form.register(`attributes.${index}.enumValuesText`)}
                          />
                          <FieldError message={errors.attributes?.[index]?.enumValuesText?.message} />
                        </div>
                      ) : null}
                      <div className="col-span-2 flex flex-col gap-1.5">
                        <Label>校验正则</Label>
                        <Input
                          placeholder="如：^[a-z0-9-]+$"
                          {...form.register(`attributes.${index}.regex`)}
                        />
                      </div>
                      <div className="flex items-end gap-4 pb-2">
                        <label className="flex cursor-pointer items-center gap-1.5 text-xs">
                          <Checkbox {...form.register(`attributes.${index}.required`)} /> 必填
                        </label>
                        <label className="flex cursor-pointer items-center gap-1.5 text-xs">
                          <Checkbox {...form.register(`attributes.${index}.unique`)} /> 唯一
                        </label>
                      </div>
                      <div className="flex items-end justify-end pb-1">
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
                    </div>
                  </div>
                )
              })
            )}
          </section>

          {/* 关系子表单 */}
          <section className="flex flex-col gap-3">
            <div className="flex items-center justify-between">
              <h3 className="text-xs font-medium">关系定义（{relFields.fields.length}）</h3>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => relFields.append(emptyRelation())}
              >
                <PlusIcon /> 添加关系
              </Button>
            </div>
            {relFields.fields.length === 0 ? (
              <p className="rounded-lg border border-dashed px-3 py-4 text-center text-xs text-muted-foreground">
                暂无关系，点击「添加关系」定义与其他模型的关联
              </p>
            ) : (
              relFields.fields.map((field, index) => (
                <div
                  key={field.id}
                  className="grid grid-cols-2 items-end gap-3 rounded-lg border p-3 sm:grid-cols-6"
                >
                  <div className="flex flex-col gap-1.5">
                    <Label>名称</Label>
                    <Input placeholder="如：运行于" {...form.register(`relations.${index}.name`)} />
                    <FieldError message={errors.relations?.[index]?.name?.message} />
                  </div>
                  <div className="flex flex-col gap-1.5">
                    <Label>编码</Label>
                    <Input placeholder="如：runs_on" {...form.register(`relations.${index}.code`)} />
                    <FieldError message={errors.relations?.[index]?.code?.message} />
                  </div>
                  <div className="flex flex-col gap-1.5">
                    <Label>目标模型</Label>
                    <Controller
                      control={form.control}
                      name={`relations.${index}.target_model`}
                      render={({ field: targetField }) => (
                        <Select
                          value={targetField.value || null}
                          onValueChange={(value) => {
                            if (value) targetField.onChange(value)
                          }}
                        >
                          <SelectTrigger>
                            <SelectValue placeholder="选择模型">
                              {(v: string) => v || "选择模型"}
                            </SelectValue>
                          </SelectTrigger>
                          <SelectContent>
                            {targetModelOptions.map((code) => (
                              <SelectItem key={code} value={code}>
                                {code}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      )}
                    />
                    <FieldError message={errors.relations?.[index]?.target_model?.message} />
                  </div>
                  <div className="flex flex-col gap-1.5">
                    <Label>基数</Label>
                    <Controller
                      control={form.control}
                      name={`relations.${index}.cardinality`}
                      render={({ field: cardField }) => (
                        <Select
                          value={cardField.value}
                          onValueChange={(value) => {
                            if (value) cardField.onChange(value)
                          }}
                        >
                          <SelectTrigger>
                            <SelectValue>{(v: RelationCardinality) => CARDINALITY_LABELS[v]}</SelectValue>
                          </SelectTrigger>
                          <SelectContent>
                            {RELATION_CARDINALITIES.map((c) => (
                              <SelectItem key={c} value={c}>
                                {CARDINALITY_LABELS[c]}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      )}
                    />
                  </div>
                  <div className="flex flex-col gap-1.5">
                    <Label>方向</Label>
                    <Controller
                      control={form.control}
                      name={`relations.${index}.direction`}
                      render={({ field: dirField }) => (
                        <Select
                          value={dirField.value}
                          onValueChange={(value) => {
                            if (value) dirField.onChange(value)
                          }}
                        >
                          <SelectTrigger>
                            <SelectValue>{(v: RelationDirection) => DIRECTION_LABELS[v]}</SelectValue>
                          </SelectTrigger>
                          <SelectContent>
                            {RELATION_DIRECTIONS.map((d) => (
                              <SelectItem key={d} value={d}>
                                {DIRECTION_LABELS[d]}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      )}
                    />
                  </div>
                  <div className="flex justify-end">
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      aria-label="删除关系"
                      onClick={() => relFields.remove(index)}
                    >
                      <Trash2Icon className="text-destructive" />
                    </Button>
                  </div>
                </div>
              ))
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
              {form.formState.isSubmitting ? "保存中…" : "保存"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
