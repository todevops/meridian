// 全局导航配置（F-090 六组结构），侧边栏、面包屑与首页共用同一份真源。
// permission 为菜单项可见所需权限点；badge="pool-pending" 表示展示发现池待处理数徽标。

import {
  AppWindow as AppWindowIcon,
  BellRing as BellRingIcon,
  Boxes as BoxesIcon,
  Building2 as Building2Icon,
  Cable as CableIcon,
  Cloud as CloudIcon,
  Database as DatabaseIcon,
  KeyRound as KeyRoundIcon,
  Layers as LayersIcon,
  LayoutDashboard as LayoutDashboardIcon,
  ListChecks as ListChecksIcon,
  Network as NetworkIcon,
  Radar as RadarIcon,
  Server as ServerIcon,
  Settings as SettingsIcon,
  UsersRound as UsersRoundIcon,
  type LucideIcon,
} from "lucide-react"

export interface NavItemDef {
  href: string
  label: string
  icon: LucideIcon
  /** 可见所需权限点，缺省表示登录即可见 */
  permission?: string
  /** 待处理数徽标来源 */
  badge?: "pool-pending" | "alerts-unacked"
  /** 是否仅在精确匹配时高亮（如 /） */
  exact?: boolean
}

export interface NavGroupDef {
  key: string
  label: string
  items: NavItemDef[]
}

/** 总览为独立顶部入口，不属于任何折叠组 */
export const OVERVIEW_ITEM: NavItemDef = {
  href: "/",
  label: "总览",
  icon: LayoutDashboardIcon,
  exact: true,
}

export const NAV_GROUPS: NavGroupDef[] = [
  {
    key: "assets",
    label: "资产管理",
    items: [
      { href: "/hosts", label: "主机", icon: ServerIcon },
      { href: "/applications", label: "应用归属", icon: AppWindowIcon },
      { href: "/virtualization", label: "虚拟化", icon: LayersIcon },
      { href: "/cloud", label: "云资源", icon: CloudIcon },
      { href: "/network/devices", label: "网络设备", icon: CableIcon },
      { href: "/dcim", label: "机房与机柜", icon: Building2Icon },
    ],
  },
  {
    key: "network",
    label: "网络与地址",
    items: [{ href: "/ipam", label: "IPAM", icon: NetworkIcon }],
  },
  {
    key: "discovery",
    label: "发现与采集",
    items: [
      { href: "/pool", label: "发现池", icon: RadarIcon, badge: "pool-pending" },
      {
        href: "/alerts",
        label: "告警事件",
        icon: BellRingIcon,
        permission: "alert:read",
        badge: "alerts-unacked",
      },
      {
        href: "/discovery",
        label: "采集任务",
        icon: ListChecksIcon,
        permission: "task:read",
      },
      {
        href: "/integrations",
        label: "凭据管理",
        icon: KeyRoundIcon,
        permission: "credential:read",
      },
    ],
  },
  {
    key: "dbms",
    label: "数据库与中间件",
    items: [{ href: "/dbms", label: "数据库实例", icon: DatabaseIcon }],
  },
  {
    key: "system",
    label: "系统管理",
    items: [
      { href: "/models", label: "模型管理", icon: BoxesIcon },
      {
        href: "/settings/users",
        label: "用户管理",
        icon: UsersRoundIcon,
        permission: "user:manage",
      },
      {
        href: "/settings/roles",
        label: "角色管理",
        icon: SettingsIcon,
        permission: "role:manage",
      },
    ],
  },
]

/** 判断菜单项是否当前路由 */
export function isNavItemActive(pathname: string, item: NavItemDef): boolean {
  if (item.exact) return pathname === item.href
  return pathname === item.href || pathname.startsWith(`${item.href}/`)
}

/** 按权限点过滤后的可见分组（空组剔除） */
export function visibleNavGroups(permissions: string[] | undefined): NavGroupDef[] {
  return NAV_GROUPS.map((group) => ({
    ...group,
    items: group.items.filter(
      (item) => !item.permission || (permissions ?? []).includes(item.permission),
    ),
  })).filter((group) => group.items.length > 0)
}

/** 面包屑：由当前路径反查「组 / 页」 */
export function breadcrumbFor(pathname: string): { group: string; page: string } | null {
  if (pathname === "/" || pathname === "") {
    return { group: "", page: OVERVIEW_ITEM.label }
  }
  // 最长前缀匹配，避免短前缀误中
  let best: { group: string; page: string; len: number } | null = null
  for (const group of NAV_GROUPS) {
    for (const item of group.items) {
      if (pathname === item.href || pathname.startsWith(`${item.href}/`)) {
        if (!best || item.href.length > best.len) {
          best = { group: group.label, page: item.label, len: item.href.length }
        }
      }
    }
  }
  return best ? { group: best.group, page: best.page } : null
}
