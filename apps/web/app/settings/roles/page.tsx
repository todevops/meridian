"use client"

// 角色管理页（需 role:manage 权限）：角色列表 + 新建/编辑（权限点勾选）+ 删除自定义角色

import { useCallback, useEffect, useState } from "react"
import { Plus as PlusIcon, Trash2 as Trash2Icon } from "lucide-react"

import { Button } from "@workspace/ui/components/button"
import { Badge } from "@/components/ui/badge"
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
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  ApiError,
  createRole,
  deleteRole,
  listPermissions,
  listRoles,
  patchRole,
  type PermissionItem,
  type Role,
} from "@/lib/api"

export default function RolesPage() {
  const [roles, setRoles] = useState<Role[]>([])
  const [permissions, setPermissions] = useState<PermissionItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<Role | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [roleList, permList] = await Promise.all([
        listRoles(),
        listPermissions(),
      ])
      setRoles(roleList.items)
      setPermissions(permList.items)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载角色列表失败")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  async function onDelete(role: Role) {
    if (
      !window.confirm(
        `确认删除角色「${role.name}」(${role.code})？此操作不可恢复。`
      )
    )
      return
    try {
      await deleteRole(role.id)
      load()
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "删除角色失败")
    }
  }

  const permName = (code: string) =>
    permissions.find((p) => p.code === code)?.name ?? code

  return (
    <div className="flex flex-col gap-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold">角色管理</h1>
          <p className="text-xs text-muted-foreground">
            角色是权限点的集合，用户经角色获得权限
          </p>
        </div>
        <Button
          onClick={() => {
            setEditing(null)
            setDialogOpen(true)
          }}
        >
          <PlusIcon /> 新建角色
        </Button>
      </div>

      {error && (
        <div className="flex items-center gap-3 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {error}
          <Button variant="outline" size="sm" onClick={load}>
            重试
          </Button>
        </div>
      )}

      {/* 数据范围说明：system_owner 为强制范围角色（阶段四 4A，F-005） */}
      <div className="rounded-lg border border-primary/30 bg-primary/5 px-3 py-2 text-xs text-muted-foreground">
        内置角色 <span className="font-medium text-foreground">系统负责人（system_owner）</span>
        为强制数据范围角色：该角色用户仅可见「用户管理」页为其绑定的业务应用及其关联资产；未绑定应用时无任何业务数据可见。
        其余角色不受数据范围裁剪，默认全量可见。
      </div>

      <div className="rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>编码</TableHead>
              <TableHead>名称</TableHead>
              <TableHead>权限点</TableHead>
              <TableHead className="w-16">用户数</TableHead>
              <TableHead className="w-32 text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              Array.from({ length: 4 }).map((_, i) => (
                <TableRow key={i}>
                  {Array.from({ length: 5 }).map((_, j) => (
                    <TableCell key={j}>
                      <Skeleton className="h-4 w-full" />
                    </TableCell>
                  ))}
                </TableRow>
              ))
            ) : roles.length > 0 ? (
              roles.map((role) => (
                <TableRow key={role.id}>
                  <TableCell className="font-medium">
                    {role.code}
                    {role.is_builtin && (
                      <Badge variant="outline" className="ml-2">
                        内置
                      </Badge>
                    )}
                    {role.code === "system_owner" && (
                      <Badge variant="secondary" className="ml-2">
                        强制范围
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell>
                    <div>{role.name}</div>
                    {role.description && (
                      <div className="text-xs text-muted-foreground">
                        {role.description}
                      </div>
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex max-w-md flex-wrap gap-1">
                      {role.permissions.length > 0 ? (
                        role.permissions.map((code) => (
                          <Badge key={code} variant="secondary" title={code}>
                            {permName(code)}
                          </Badge>
                        ))
                      ) : (
                        <span className="text-muted-foreground">无权限</span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>{role.user_count}</TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        setEditing(role)
                        setDialogOpen(true)
                      }}
                    >
                      编辑
                    </Button>
                    {!role.is_builtin && (
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label="删除角色"
                        disabled={role.user_count > 0}
                        onClick={() => onDelete(role)}
                      >
                        <Trash2Icon className="text-destructive" />
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))
            ) : (
              <TableRow>
                <TableCell
                  colSpan={5}
                  className="py-10 text-center text-muted-foreground"
                >
                  暂无角色
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <RoleFormDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        role={editing}
        permissions={permissions}
        onSaved={load}
      />
    </div>
  )
}

interface RoleFormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 为 null 表示新建，否则为编辑该角色 */
  role: Role | null
  permissions: PermissionItem[]
  onSaved: () => void
}

function RoleFormDialog({
  open,
  onOpenChange,
  role,
  permissions,
  onSaved,
}: RoleFormDialogProps) {
  const isEdit = role !== null
  // 内置 admin 角色权限点锁定（服务端同样强制），防止把管理员锁死
  const permissionsLocked = isEdit && role.code === "admin"
  const [code, setCode] = useState("")
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [selected, setSelected] = useState<string[]>([])
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (open) {
      setCode(role?.code ?? "")
      setName(role?.name ?? "")
      setDescription(role?.description ?? "")
      setSelected(role?.permissions ?? [])
      setSubmitError(null)
    }
  }, [open, role])

  function toggle(perm: string, checked: boolean) {
    setSelected((prev) =>
      checked ? [...prev, perm] : prev.filter((p) => p !== perm)
    )
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSubmitError(null)
    if (!isEdit && !code.trim()) {
      setSubmitError("请输入角色编码")
      return
    }
    if (!name.trim()) {
      setSubmitError("请输入角色名称")
      return
    }
    setSubmitting(true)
    try {
      if (isEdit) {
        await patchRole(role.id, {
          name: name.trim(),
          description: description.trim(),
          ...(permissionsLocked ? {} : { permissions: selected }),
        })
      } else {
        await createRole({
          code: code.trim(),
          name: name.trim(),
          description: description.trim(),
          permissions: selected,
        })
      }
      onOpenChange(false)
      onSaved()
    } catch (err) {
      setSubmitError(
        err instanceof ApiError ? err.message : "保存失败，请稍后重试"
      )
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {isEdit ? `编辑角色：${role.name}` : "新建角色"}
          </DialogTitle>
          <DialogDescription>
            {permissionsLocked
              ? "内置 admin 角色的权限点不可修改，仅可调整名称与描述。"
              : "勾选该角色拥有的权限点，用户经角色获得权限并集。"}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          <div className="grid grid-cols-2 gap-4">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="role-code">
                角色编码{!isEdit && <span className="text-destructive">*</span>}
              </Label>
              <Input
                id="role-code"
                value={code}
                disabled={isEdit}
                onChange={(e) => setCode(e.target.value)}
                placeholder="如：netops"
              />
              {isEdit && (
                <p className="text-xs text-muted-foreground">
                  编码创建后不可修改
                </p>
              )}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="role-name">
                角色名称<span className="text-destructive">*</span>
              </Label>
              <Input
                id="role-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="如：网络运维"
              />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="role-desc">描述</Label>
            <Input
              id="role-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="角色用途说明（可选）"
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label>权限点（{selected.length}）</Label>
            <div className="grid grid-cols-2 gap-x-4 gap-y-2 rounded-lg border p-3">
              {permissions.map((perm) => (
                <label
                  key={perm.code}
                  className="flex cursor-pointer items-center gap-1.5 text-xs"
                  title={perm.description}
                >
                  <Checkbox
                    checked={selected.includes(perm.code)}
                    disabled={permissionsLocked}
                    onChange={(e) => toggle(perm.code, e.target.checked)}
                  />
                  {perm.name}
                  <span className="text-xs text-muted-foreground">
                    ({perm.code})
                  </span>
                </label>
              ))}
            </div>
          </div>
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
            <Button type="submit" disabled={submitting}>
              {submitting ? "保存中…" : "保存"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
