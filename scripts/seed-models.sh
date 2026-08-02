#!/usr/bin/env bash
# 种子模型导入：等待服务就绪后，按 scripts/seed/*.json 逐个创建模型（已存在则跳过）。
# 用法：BASE_URL=http://localhost:8080 bash scripts/seed-models.sh
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SEED_DIR="${SCRIPT_DIR}/seed"

echo "==> 等待 ${BASE_URL} 就绪（最多 60 秒）"
ready=0
for _ in $(seq 1 60); do
  if curl -fsS "${BASE_URL}/healthz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
[[ "${ready}" -eq 1 ]] || { echo "错误：等待 60 秒后服务仍未就绪" >&2; exit 1; }
echo "    服务已就绪"

# 业务接口需认证：先登录获取会话 cookie。
# shellcheck source=auth-login.sh
source "${SCRIPT_DIR}/auth-login.sh"
meridian_login

shopt -s nullglob
seed_files=("${SEED_DIR}"/*.json)
[[ ${#seed_files[@]} -gt 0 ]] || { echo "错误：${SEED_DIR} 下没有种子文件" >&2; exit 1; }

created=0
skipped=0

for f in "${seed_files[@]}"; do
  # 取文件顶层第一个 "code" 字段作为模型编码（种子文件约定 code 位于 attributes 之前）。
  code="$(grep -o '"code"[[:space:]]*:[[:space:]]*"[^"]*"' "$f" | head -n 1 | sed 's/^.*:[[:space:]]*"//; s/"$//')"
  [[ -n "${code}" ]] || { echo "错误：无法从 ${f} 解析模型编码" >&2; exit 1; }

  # 已存在则跳过：按 keyword 预筛后，在结果中精确匹配 "code":"<code>"。
  if curl -fsS -b "${MERIDIAN_COOKIE_JAR}" "${BASE_URL}/api/v1/models?keyword=${code}&page_size=200" \
      | grep -E "\"code\"[[:space:]]*:[[:space:]]*\"${code}\"" >/dev/null; then
    echo "--- 跳过 ${code}（已存在）"
    skipped=$((skipped + 1))
    continue
  fi

  resp="$(curl -sS -b "${MERIDIAN_COOKIE_JAR}" -w $'\n%{http_code}' -X POST "${BASE_URL}/api/v1/models" \
    -H 'Content-Type: application/json' \
    --data-binary "@${f}")"
  http_code="${resp##*$'\n'}"
  body="${resp%$'\n'*}"
  if [[ "${http_code}" == 2* ]]; then
    echo "--- 创建 ${code} 成功"
    created=$((created + 1))
  else
    echo "错误：创建模型 ${code} 失败（HTTP ${http_code}）：${body}" >&2
    exit 1
  fi
done

echo "==> 种子模型导入完成：新建 ${created} 个，跳过 ${skipped} 个"
