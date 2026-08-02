// 各凭据类型的 secret 字段规格：新建与轮换对话框据此动态渲染录入项。
// 密文提交后不可回读，仅可通过轮换更新。

import type { CredentialType } from "@/lib/api"

export interface SecretFieldDef {
  key: string
  label: string
  kind: "text" | "password" | "textarea"
  /** 是否必填（ssh_ipmi 的密码/私钥二选一由表单层校验） */
  required: boolean
  placeholder?: string
}

const VCENTER_FIELDS: SecretFieldDef[] = [
  { key: "url", label: "vCenter 地址", kind: "text", required: true, placeholder: "https://vcenter.example.com" },
  { key: "username", label: "用户名", kind: "text", required: true },
  { key: "password", label: "密码", kind: "password", required: true },
]

const CLOUD_FIELDS: SecretFieldDef[] = [
  { key: "access_key_id", label: "AccessKey ID", kind: "text", required: true },
  { key: "access_key_secret", label: "AccessKey Secret", kind: "password", required: true },
  { key: "region", label: "Region", kind: "text", required: true, placeholder: "cn-hangzhou" },
]

const SNMP_V2C_FIELDS: SecretFieldDef[] = [
  { key: "community", label: "Community", kind: "password", required: true },
]

const SNMP_V3_FIELDS: SecretFieldDef[] = [
  { key: "username", label: "v3 用户名", kind: "text", required: true },
  { key: "auth_password", label: "认证密码（Auth）", kind: "password", required: true },
  { key: "priv_password", label: "加密密码（Priv）", kind: "password", required: true },
]

const DB_FIELDS: SecretFieldDef[] = [
  { key: "host", label: "主机地址", kind: "text", required: true },
  { key: "port", label: "端口", kind: "text", required: true, placeholder: "3306" },
  { key: "username", label: "用户名", kind: "text", required: true },
  { key: "password", label: "密码", kind: "password", required: true },
]

const KUBECONFIG_FIELDS: SecretFieldDef[] = [
  { key: "kubeconfig", label: "Kubeconfig 内容", kind: "textarea", required: true, placeholder: "粘贴完整 kubeconfig YAML" },
]

const SSH_IPMI_FIELDS: SecretFieldDef[] = [
  { key: "username", label: "用户名", kind: "text", required: true },
  { key: "password", label: "密码", kind: "password", required: false },
  { key: "private_key", label: "私钥", kind: "textarea", required: false, placeholder: "密码与私钥至少填写一项" },
]

const API_TOKEN_FIELDS: SecretFieldDef[] = [
  { key: "api_url", label: "API 地址", kind: "text", required: true, placeholder: "https://api.example.com" },
  { key: "token", label: "Token", kind: "password", required: true },
]

/** SNMP 版本选项 */
export const SNMP_VERSIONS = ["v2c", "v3"] as const
export type SnmpVersion = (typeof SNMP_VERSIONS)[number]

/** 按类型（与 SNMP 版本）返回需渲染的 secret 字段 */
export function secretFieldsFor(
  type: CredentialType,
  snmpVersion: SnmpVersion = "v2c"
): SecretFieldDef[] {
  switch (type) {
    case "vcenter":
      return VCENTER_FIELDS
    case "aliyun":
    case "volc":
      return CLOUD_FIELDS
    case "snmp":
      return snmpVersion === "v3" ? SNMP_V3_FIELDS : SNMP_V2C_FIELDS
    case "db":
      return DB_FIELDS
    case "kubeconfig":
      return KUBECONFIG_FIELDS
    case "ssh_ipmi":
      return SSH_IPMI_FIELDS
    case "n9e":
    case "netbox":
      return API_TOKEN_FIELDS
  }
}

/** 新建表单切换类型时的初始 secret */
export function defaultSecretFor(type: CredentialType): Record<string, string> {
  return type === "snmp" ? { version: "v2c" } : {}
}

/** 校验 secret 必填项，返回字段级错误（ssh_ipmi 校验密码/私钥至少一项） */
export function validateSecret(
  type: CredentialType,
  secret: Record<string, string>
): { key: string; message: string }[] {
  const snmpVersion: SnmpVersion =
    type === "snmp" && secret.version === "v3" ? "v3" : "v2c"
  const issues: { key: string; message: string }[] = []
  for (const field of secretFieldsFor(type, snmpVersion)) {
    if (field.required && !(secret[field.key] ?? "").trim()) {
      issues.push({ key: field.key, message: `请输入${field.label}` })
    }
  }
  if (
    type === "ssh_ipmi" &&
    !(secret.password ?? "").trim() &&
    !(secret.private_key ?? "").trim()
  ) {
    issues.push({ key: "password", message: "密码与私钥至少填写一项" })
  }
  return issues
}

/** 剔除空白值，得到提交用的 secret 对象 */
export function cleanSecret(
  secret: Record<string, string>
): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(secret)) {
    if (value !== "") out[key] = value
  }
  return out
}
