"use client"

// 稽核与整改页（F-081 + F-026）：
// Tab1 整改待办：open/closed 两态列表，open 可手动关闭（修复后引擎亦会自动关闭）；
// Tab2 规则管理：声明式稽核规则的新建/编辑、启停、演练标识与手动执行；
// Tab3 待退役：三方会签候选列表（心跳/扫描/云各 ✓✗），确认退役并展示联动结果。

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"
import {
  Check as CheckIcon,
  Play as PlayIcon,
  ShieldCheck as ShieldCheckIcon,
  X as XIcon,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@workspace/ui/components/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { ConfirmDialog } from "@/components/confirm-dialog"
import { RuleFormDialog } from "@/components/rule-form-dialog"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import {
  ApiError,
  closeGovernanceTodo,
  listGovernanceRules,
  listGovernanceTodos,
  listRetireCandidates,
  patchGovernanceRule,
  retireCI,
  runGovernanceRule,
  type GovernanceRule,
  type GovernanceTodo,
  type Paged,
  type RetireActionResult,
  type RetireCandidate,
} from "@/lib/api"
import { formatDateTime, pickAttr } from "@/lib/format"

const PAGE_SIZE = 20

type TabKey = "todos" | "rules" | "retire"

const TABS: { key: TabKey; label: string }[] = [
  { key: "todos", label: "整改待办" },
  { key: "rules", label: "规则管理" },
  { key: "retire", label: "待退役" },
]

/** CI 展示名候选属性编码 */
const CI_NAME_CODES = ["hostname", "ident", "name", "ip"]

function ciDisplay(candidate: RetireCandidate): string {
  const name = pickAttr(candidate.ci.attributes, CI_NAME_CODES)
  return name === "—" ? candidate.ci.id : name
}

// ---------- 整改待办 Tab ----------

function TodosTab() {
  const [status, setStatus] = useState<"open" | "closed">("open")
  const [page, setPage] = useState(1)
  const [data, setData] = useState<Paged<GovernanceTodo> | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [closing, setClosing] = useState<string | null>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      const res = await listGovernanceTodos({ status, page, page_size: PAGE_SIZE })
      setData(res)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "加载整改待办失败")
      setData({ items: [], total: 0, page: 1, page_size: PAGE_SIZE })
    }
  }, [status, page])

  useEffect(() => {
    setData(null)
    void load()
  }, [load])

  const onClose = async (id: string) => {
    setClosing(id)
    setError(null)
    try {
      await closeGovernanceTodo(id)
      await load()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "关闭待办失败")
    } finally {
      setClosing(null)
    }
  }

  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1

  return (
    <Card>
      <CardHeader className="pb-2">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <CardTitle className="text-base">整改待办</CardTitle>
            <CardDescription>
              稽核规则每日执行产出，修复后由引擎自动关闭，也可人工确认关闭
            </CardDescription>
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant={status === "open" ? "default" : "outline"}
              size="sm"
              onClick={() => {
                setPage(1)
                setStatus("open")
              }}
            >
              待处理
            </Button>
            <Button
              variant={status === "closed" ? "default" : "outline"}
              size="sm"
              onClick={() => {
                setPage(1)
                setStatus("closed")
              }}
            >
              已关闭
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="p-0">
        {error && (
          <p className="mx-4 mb-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
            {error}
          </p>
        )}
        {data === null ? (
          <div className="flex flex-col gap-2 p-6">
            {Array.from({ length: 5 }).map((_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        ) : data.items.length === 0 ? (
          <div className="flex flex-col items-center gap-2 py-16">
            <ShieldCheckIcon className="size-8 text-muted-foreground" />
            <p className="text-xs text-muted-foreground">
              {status === "open" ? "没有待处理的整改项，数据治理良好" : "暂无已关闭的整改项"}
            </p>
          </div>
        ) : (
          <>
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b text-left text-muted-foreground">
                  <th className="px-4 py-2.5 font-medium">规则</th>
                  <th className="px-4 py-2.5 font-medium">CI</th>
                  <th className="px-4 py-2.5 font-medium">标题</th>
                  <th className="px-4 py-2.5 font-medium">生成时间</th>
                  {status === "closed" && (
                    <th className="px-4 py-2.5 font-medium">关闭时间</th>
                  )}
                  {status === "open" && (
                    <th className="px-4 py-2.5 font-medium">操作</th>
                  )}
                </tr>
              </thead>
              <tbody>
                {data.items.map((todo) => (
                  <tr key={todo.id} className="border-b last:border-0 hover:bg-muted/40">
                    <td className="px-4 py-2.5 font-medium">
                      {todo.rule_name ?? todo.rule_id}
                    </td>
                    <td className="px-4 py-2.5">
                      {todo.ci_id ? (
                        <Link
                          href={`/hosts/${todo.ci_id}`}
                          className="text-primary underline-offset-2 hover:underline"
                        >
                          查看 CI
                        </Link>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td className="max-w-[360px] truncate px-4 py-2.5" title={todo.title}>
                      {todo.title}
                    </td>
                    <td className="px-4 py-2.5 text-muted-foreground">
                      {formatDateTime(todo.created_at)}
                    </td>
                    {status === "closed" && (
                      <td className="px-4 py-2.5 text-muted-foreground">
                        {formatDateTime(todo.closed_at)}
                      </td>
                    )}
                    {status === "open" && (
                      <td className="px-4 py-2.5">
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={closing === todo.id}
                          onClick={() => void onClose(todo.id)}
                        >
                          <CheckIcon className="mr-1 size-3.5" />
                          {closing === todo.id ? "关闭中…" : "关闭"}
                        </Button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
            <div className="flex items-center justify-between border-t px-4 py-3 text-xs text-muted-foreground">
              <span>共 {data.total} 条</span>
              <div className="flex items-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={page <= 1}
                  onClick={() => setPage((p) => p - 1)}
                >
                  上一页
                </Button>
                <span>
                  第 {page} / {totalPages} 页
                </span>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={page >= totalPages}
                  onClick={() => setPage((p) => p + 1)}
                >
                  下一页
                </Button>
              </div>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}

// ---------- 规则管理 Tab ----------

function RulesTab() {
  const [rules, setRules] = useState<GovernanceRule[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<GovernanceRule | null>(null)
  const [running, setRunning] = useState<string | null>(null)
  const [toggling, setToggling] = useState<string | null>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      const res = await listGovernanceRules({ page: 1, page_size: 100 })
      setRules(res.items)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "加载稽核规则失败")
      setRules([])
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const onToggleEnabled = async (rule: GovernanceRule, enabled: boolean) => {
    setToggling(rule.id)
    setError(null)
    try {
      await patchGovernanceRule(rule.id, { enabled })
      await load()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "更新规则失败")
    } finally {
      setToggling(null)
    }
  }

  const onRun = async (rule: GovernanceRule) => {
    setRunning(rule.id)
    setError(null)
    setNotice(null)
    try {
      await runGovernanceRule(rule.id)
      setNotice(`规则「${rule.name}」已执行完成`)
      await load()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "执行规则失败")
    } finally {
      setRunning(null)
    }
  }

  return (
    <Card>
      <CardHeader className="pb-2">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <CardTitle className="text-base">稽核规则</CardTitle>
            <CardDescription>
              声明式规则：模型过滤条件 + 断言表达式；稽核规则每日执行产出整改待办，自动入库白名单规则对判定为新建的发现记录直接建档
            </CardDescription>
          </div>
          <Button
            size="sm"
            onClick={() => {
              setEditing(null)
              setDialogOpen(true)
            }}
          >
            新建规则
          </Button>
        </div>
      </CardHeader>
      <CardContent className="p-0">
        {error && (
          <p className="mx-4 mb-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
            {error}
          </p>
        )}
        {notice && (
          <p className="mx-4 mb-2 rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-700 dark:text-emerald-400">
            {notice}
          </p>
        )}
        {rules === null ? (
          <div className="flex flex-col gap-2 p-6">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        ) : rules.length === 0 ? (
          <div className="flex flex-col items-center gap-2 py-16">
            <ShieldCheckIcon className="size-8 text-muted-foreground" />
            <p className="text-xs text-muted-foreground">暂无稽核规则，点击「新建规则」创建</p>
          </div>
        ) : (
          <table className="w-full text-xs">
            <thead>
              <tr className="border-b text-left text-muted-foreground">
                <th className="px-4 py-2.5 font-medium">名称</th>
                <th className="px-4 py-2.5 font-medium">类型</th>
                <th className="px-4 py-2.5 font-medium">模型</th>
                <th className="px-4 py-2.5 font-medium">启用</th>
                <th className="px-4 py-2.5 font-medium">模式</th>
                <th className="px-4 py-2.5 font-medium">最近执行</th>
                <th className="px-4 py-2.5 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {rules.map((rule) => (
                <tr key={rule.id} className="border-b last:border-0 hover:bg-muted/40">
                  <td className="max-w-[240px] truncate px-4 py-2.5 font-medium" title={rule.name}>
                    {rule.name}
                  </td>
                  <td className="px-4 py-2.5">
                    {/* 旧数据无 type 字段时按稽核展示 */}
                    {rule.type === "auto_ingest" ? (
                      <Badge variant="outline">自动入库白名单</Badge>
                    ) : (
                      <Badge variant="secondary">稽核</Badge>
                    )}
                  </td>
                  <td className="px-4 py-2.5 text-muted-foreground">{rule.model_code}</td>
                  <td className="px-4 py-2.5">
                    <Switch
                      checked={rule.enabled}
                      disabled={toggling === rule.id}
                      onCheckedChange={(checked) => void onToggleEnabled(rule, checked)}
                      aria-label={`启用规则 ${rule.name}`}
                    />
                  </td>
                  <td className="px-4 py-2.5">
                    {rule.dry_run ? (
                      <Badge className="bg-amber-500/15 text-amber-700 dark:text-amber-400">
                        演练
                      </Badge>
                    ) : (
                      <Badge variant="secondary">正式</Badge>
                    )}
                  </td>
                  <td className="px-4 py-2.5 text-muted-foreground">
                    {formatDateTime(rule.last_run_at)}
                  </td>
                  <td className="px-4 py-2.5">
                    <div className="flex items-center gap-2">
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={running === rule.id}
                        onClick={() => void onRun(rule)}
                      >
                        <PlayIcon className="mr-1 size-3.5" />
                        {running === rule.id ? "执行中…" : "执行"}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => {
                          setEditing(rule)
                          setDialogOpen(true)
                        }}
                      >
                        编辑
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </CardContent>

      <RuleFormDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        rule={editing}
        onSaved={() => void load()}
      />
    </Card>
  )
}

// ---------- 待退役 Tab ----------

function SignMark({ ok }: { ok: boolean }) {
  return ok ? (
    <span className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
      <CheckIcon className="size-3.5" /> 是
    </span>
  ) : (
    <span className="inline-flex items-center gap-1 text-muted-foreground">
      <XIcon className="size-3.5" /> 否
    </span>
  )
}

function RetireTab() {
  const [items, setItems] = useState<RetireCandidate[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<RetireCandidate | null>(null)
  const [retiring, setRetiring] = useState(false)
  const [confirmError, setConfirmError] = useState<string | null>(null)
  const [result, setResult] = useState<{ ciId: string; actions: RetireActionResult[] } | null>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      const res = await listRetireCandidates()
      setItems(res.items)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "加载待退役候选失败")
      setItems([])
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const onRetire = async () => {
    if (!confirming) return
    setRetiring(true)
    setConfirmError(null)
    try {
      const res = await retireCI(confirming.ci.id, true)
      setResult({ ciId: res.ci_id, actions: res.actions })
      setConfirming(null)
      await load()
    } catch (e) {
      setConfirmError(e instanceof ApiError ? e.message : "退役失败，请稍后重试")
    } finally {
      setRetiring(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      {result && (
        <Card className="border-emerald-500/40">
          <CardHeader className="pb-2">
            <CardTitle className="text-base">退役联动结果</CardTitle>
            <CardDescription>CI {result.ciId} 的联动动作执行回执</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {result.actions.length === 0 ? (
              <p className="text-xs text-muted-foreground">无联动动作回执</p>
            ) : (
              <ul className="flex flex-col gap-1.5">
                {result.actions.map((action, i) => (
                  <li
                    key={`${action.type}-${i}`}
                    className="flex items-center gap-2 rounded-lg border px-3 py-2 text-xs"
                  >
                    {action.ok ? (
                      <CheckIcon className="size-3.5 shrink-0 text-emerald-600 dark:text-emerald-400" />
                    ) : (
                      <XIcon className="size-3.5 shrink-0 text-destructive" />
                    )}
                    <span className="font-medium">{action.type}</span>
                    <span className="truncate text-muted-foreground">
                      {action.detail ?? (action.ok ? "成功" : "失败")}
                    </span>
                  </li>
                ))}
              </ul>
            )}
            <Button
              variant="outline"
              size="sm"
              className="w-fit"
              onClick={() => setResult(null)}
            >
              知道了
            </Button>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">待退役候选</CardTitle>
          <CardDescription>
            三方会签：心跳停更 + 扫描不存活 + 云 / vCenter 无实例；全部满足方可执行退役
          </CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          {error && (
            <p className="mx-4 mb-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
              {error}
            </p>
          )}
          {items === null ? (
            <div className="flex flex-col gap-2 p-6">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : items.length === 0 ? (
            <div className="flex flex-col items-center gap-2 py-16">
              <ShieldCheckIcon className="size-8 text-muted-foreground" />
              <p className="text-xs text-muted-foreground">暂无待退役候选资产</p>
            </div>
          ) : (
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b text-left text-muted-foreground">
                  <th className="px-4 py-2.5 font-medium">CI</th>
                  <th className="px-4 py-2.5 font-medium">心跳停更</th>
                  <th className="px-4 py-2.5 font-medium">扫描不存活</th>
                  <th className="px-4 py-2.5 font-medium">云无实例</th>
                  <th className="px-4 py-2.5 font-medium">会签结论</th>
                  <th className="px-4 py-2.5 font-medium">操作</th>
                </tr>
              </thead>
              <tbody>
                {items.map((candidate) => (
                  <tr key={candidate.ci.id} className="border-b last:border-0 hover:bg-muted/40">
                    <td className="px-4 py-2.5">
                      <Link
                        href={`/hosts/${candidate.ci.id}`}
                        className="font-medium text-primary underline-offset-2 hover:underline"
                      >
                        {ciDisplay(candidate)}
                      </Link>
                    </td>
                    <td className="px-4 py-2.5">
                      <SignMark ok={candidate.heartbeat_ok} />
                    </td>
                    <td className="px-4 py-2.5">
                      <SignMark ok={candidate.scan_ok} />
                    </td>
                    <td className="px-4 py-2.5">
                      <SignMark ok={candidate.cloud_ok} />
                    </td>
                    <td className="px-4 py-2.5">
                      {candidate.eligible ? (
                        <Badge variant="success">可退役</Badge>
                      ) : (
                        <Badge variant="secondary">未满足</Badge>
                      )}
                    </td>
                    <td className="px-4 py-2.5">
                      <Button
                        variant="destructive"
                        size="sm"
                        disabled={!candidate.eligible}
                        onClick={() => {
                          setConfirmError(null)
                          setConfirming(candidate)
                        }}
                      >
                        确认退役
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </CardContent>
      </Card>

      <ConfirmDialog
        open={confirming !== null}
        onOpenChange={(open) => {
          if (!open) setConfirming(null)
        }}
        title={`确认退役 ${confirming ? ciDisplay(confirming) : ""}？`}
        description="退役将联动执行：n9e 摘除监控 target、JumpServer 禁用资产、IPAM 回收 IP、Oxidized 清单移除；动作全部留审计，IP 回收不可自动恢复。"
        confirmText="确认退役"
        error={confirmError}
        loading={retiring}
        onConfirm={() => void onRetire()}
      />
    </div>
  )
}

// ---------- 页面 ----------

export default function GovernancePage() {
  const [tab, setTab] = useState<TabKey>("todos")

  return (
    <div className="flex w-full flex-col gap-5 p-6">
      <header>
        <h1 className="text-xl font-semibold tracking-tight">稽核与整改</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          稽核规则、整改待办闭环与待退役资产三方会签处置
        </p>
      </header>

      <div className="flex items-center gap-2">
        {TABS.map((t) => (
          <Button
            key={t.key}
            variant={tab === t.key ? "default" : "outline"}
            size="sm"
            onClick={() => setTab(t.key)}
          >
            {t.label}
          </Button>
        ))}
      </div>

      {tab === "todos" && <TodosTab />}
      {tab === "rules" && <RulesTab />}
      {tab === "retire" && <RetireTab />}
    </div>
  )
}
