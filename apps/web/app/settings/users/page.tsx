"use client"

// 用户管理页（需 user:manage 权限）：用户列表 + 新建/编辑（角色分配、启停、重置密码）

import { useCallback, useEffect, useState } from "react"
import { Plus as PlusIcon, Search as SearchIcon } from "lucide-react"

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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import {
  ApiError,
  createUser,
  listRoles,
  listUsers,
  patchUser,
  type Paged,
  type Role,
  type User,
  type UserStatus,
} from "@/lib/api"

const PAGE_SIZE = 20

export default function UsersPage() {
  const [keyword, setKeyword] = useState("")
  const [debounced, setDebounced] = useState("")
  const [page, setPage] = useState(1)
  const [data, setData] = useState<Paged<User> | null>(null)
  const [roles, setRoles] = useState<Role[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<User | null>(null)

  useEffect(() => {
    const timer = setTimeout(() => setDebounced(keyword.trim()), 300)
    return () => clearTimeout(timer)
  }, [keyword])

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [users, roleList] = await Promise.all([
        listUsers({ keyword: debounced || undefined, page, page_size: PAGE_SIZE }),
        listRoles(),
      ])
      setData(users)
      setRoles(roleList.items)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "加载用户列表失败")
    } finally {
      setLoading(false)
    }
  }, [debounced, page])

  useEffect(() => {
    load()
  }, [load])

  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1

  return (
    <div className="flex flex-col gap-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold">用户管理</h1>
          <p className="text-sm text-muted-foreground">系统账号的新建、角色分配、启停与密码重置</p>
        </div>
        <Button
          onClick={() => {
            setEditing(null)
            setDialogOpen(true)
          }}
        >
          <PlusIcon /> 新建用户
        </Button>
      </div>

      <div className="relative w-72">
        <SearchIcon className="absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
        <Input
          className="pl-8"
          placeholder="按用户名 / 显示名搜索"
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            setPage(1)
          }}
        />
      </div>

      {error && (
        <div className="flex items-center gap-3 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
          <Button variant="outline" size="sm" onClick={load}>
            重试
          </Button>
        </div>
      )}

      <div className="rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>用户名</TableHead>
              <TableHead>显示名</TableHead>
              <TableHead>角色</TableHead>
              <TableHead>状态</TableHead>
              <TableHead>创建时间</TableHead>
              <TableHead className="w-20 text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              Array.from({ length: 5 }).map((_, i) => (
                <TableRow key={i}>
                  {Array.from({ length: 6 }).map((_, j) => (
                    <TableCell key={j}>
                      <Skeleton className="h-4 w-full" />
                    </TableCell>
                  ))}
                </TableRow>
              ))
            ) : data && data.items.length > 0 ? (
              data.items.map((user) => (
                <TableRow key={user.id}>
                  <TableCell className="font-medium">
                    {user.username}
                    {user.is_builtin && (
                      <Badge variant="outline" className="ml-2">
                        内置
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell>{user.display_name}</TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {user.roles.length > 0 ? (
                        user.roles.map((code) => (
                          <Badge key={code} variant="secondary">
                            {roles.find((r) => r.code === code)?.name ?? code}
                          </Badge>
                        ))
                      ) : (
                        <span className="text-muted-foreground">未分配</span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant={user.status === "active" ? "default" : "outline"}>
                      {user.status === "active" ? "在用" : "停用"}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {new Date(user.created_at).toLocaleString("zh-CN")}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        setEditing(user)
                        setDialogOpen(true)
                      }}
                    >
                      编辑
                    </Button>
                  </TableCell>
                </TableRow>
              ))
            ) : (
              <TableRow>
                <TableCell colSpan={6} className="py-10 text-center text-muted-foreground">
                  暂无用户
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      {data && data.total > PAGE_SIZE && (
        <div className="flex items-center justify-end gap-2 text-sm text-muted-foreground">
          <span>
            第 {data.page} / {totalPages} 页，共 {data.total} 条
          </span>
          <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>
            上一页
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={page >= totalPages}
            onClick={() => setPage(page + 1)}
          >
            下一页
          </Button>
        </div>
      )}

      <UserFormDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        user={editing}
        roles={roles}
        onSaved={load}
      />
    </div>
  )
}

interface UserFormDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 为 null 表示新建，否则为编辑该用户 */
  user: User | null
  roles: Role[]
  onSaved: () => void
}

function UserFormDialog({ open, onOpenChange, user, roles, onSaved }: UserFormDialogProps) {
  const isEdit = user !== null
  const [username, setUsername] = useState("")
  const [displayName, setDisplayName] = useState("")
  const [password, setPassword] = useState("")
  const [status, setStatus] = useState<UserStatus>("active")
  const [selectedRoles, setSelectedRoles] = useState<string[]>([])
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (open) {
      setUsername(user?.username ?? "")
      setDisplayName(user?.display_name ?? "")
      setPassword("")
      setStatus(user?.status ?? "active")
      setSelectedRoles(user?.roles ?? [])
      setSubmitError(null)
    }
  }, [open, user])

  function toggleRole(code: string, checked: boolean) {
    setSelectedRoles((prev) => (checked ? [...prev, code] : prev.filter((c) => c !== code)))
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    setSubmitError(null)
    if (!isEdit && username.trim().length < 2) {
      setSubmitError("用户名至少 2 个字符")
      return
    }
    if (!displayName.trim()) {
      setSubmitError("请输入显示名")
      return
    }
    if (!isEdit && password.length < 6) {
      setSubmitError("密码长度至少 6 位")
      return
    }
    if (isEdit && password && password.length < 6) {
      setSubmitError("新密码长度至少 6 位")
      return
    }
    setSubmitting(true)
    try {
      if (isEdit) {
        await patchUser(user.id, {
          display_name: displayName.trim(),
          ...(password ? { password } : {}),
          ...(user.is_builtin ? {} : { status, roles: selectedRoles }),
        })
      } else {
        await createUser({
          username: username.trim(),
          display_name: displayName.trim(),
          password,
          roles: selectedRoles,
        })
      }
      onOpenChange(false)
      onSaved()
    } catch (err) {
      setSubmitError(err instanceof ApiError ? err.message : "保存失败，请稍后重试")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{isEdit ? `编辑用户：${user.username}` : "新建用户"}</DialogTitle>
          <DialogDescription>
            {isEdit
              ? "可修改显示名、重置密码" + (user.is_builtin ? "；内置账号的角色与状态不可修改。" : "、分配角色与启停账号。")
              : "创建后可登录系统，权限由所分配角色决定。"}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-4">
          <div className="grid grid-cols-2 gap-4">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="user-username">
                用户名{!isEdit && <span className="text-destructive">*</span>}
              </Label>
              <Input
                id="user-username"
                value={username}
                disabled={isEdit}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="登录账号，至少 2 个字符"
              />
              {isEdit && <p className="text-xs text-muted-foreground">用户名创建后不可修改</p>}
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="user-display-name">
                显示名<span className="text-destructive">*</span>
              </Label>
              <Input
                id="user-display-name"
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
                placeholder="如：张三"
              />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="user-password">
              {isEdit ? "重置密码（留空则不修改）" : "初始密码"}
              {!isEdit && <span className="text-destructive">*</span>}
            </Label>
            <Input
              id="user-password"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="至少 6 位"
            />
          </div>
          {(!isEdit || !user.is_builtin) && (
            <>
              <div className="flex flex-col gap-1.5">
                <Label>角色分配</Label>
                <div className="flex flex-wrap gap-x-4 gap-y-2 rounded-lg border p-3">
                  {roles.map((role) => (
                    <label key={role.code} className="flex cursor-pointer items-center gap-1.5 text-sm">
                      <Checkbox
                        checked={selectedRoles.includes(role.code)}
                        onChange={(e) => toggleRole(role.code, e.target.checked)}
                      />
                      {role.name}
                      <span className="text-xs text-muted-foreground">({role.code})</span>
                    </label>
                  ))}
                </div>
              </div>
              {isEdit && (
                <div className="flex flex-col gap-1.5">
                  <Label>账号状态</Label>
                  <Select value={status} onValueChange={(v) => v && setStatus(v as UserStatus)}>
                    <SelectTrigger>
                      <SelectValue>{(v: UserStatus) => (v === "active" ? "在用" : "停用")}</SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="active">在用</SelectItem>
                      <SelectItem value="disabled">停用</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              )}
            </>
          )}
          {submitError && (
            <p className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {submitError}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
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
