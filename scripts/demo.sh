#!/usr/bin/env bash
# 垂直切片一键演示：
#   启动 server（SQLite 临时库）→ 等待 /healthz 就绪 → 导入 13 个种子模型
#   → 逐条写入样例发现记录 → 查询主机 CI 清单 → 停止 server 并清理。
# 前置：已执行 source ../.tools/env.sh（go 与 node 在 PATH 上）。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="${SCRIPT_DIR}/../server"
BASE_URL="${BASE_URL:-http://localhost:8080}"

TMP_DIR="$(mktemp -d)"

# Windows/Git Bash 下 server 为原生进程，传入的环境变量须为 Windows 路径。
DB_FILE_UNIX="${TMP_DIR}/meridian-demo.db"
if command -v cygpath >/dev/null 2>&1; then
  DB_SQLITE_PATH="$(cygpath -w "${DB_FILE_UNIX}")"
else
  DB_SQLITE_PATH="${DB_FILE_UNIX}"
fi
export DB_SQLITE_PATH

SERVER_PID=""
cleanup() {
  if [[ -n "${SERVER_PID}" ]] && kill -0 "${SERVER_PID}" 2>/dev/null; then
    kill "${SERVER_PID}" 2>/dev/null || true
    wait "${SERVER_PID}" 2>/dev/null || true
  fi
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

# json_pp 用 node 美化 JSON 输出，非法 JSON 时原样打印。
json_pp() {
  node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{try{console.log(JSON.stringify(JSON.parse(d),null,2))}catch{process.stdout.write(d+"\n")}})'
}

echo "==> [1/5] 构建并启动 server（SQLite: ${DB_SQLITE_PATH}）"
# 说明：Windows 下 go run 的子进程无法随父进程可靠退出（会残留占用端口），
# 故先构建再运行，等价于 go run ./cmd/server。
(cd "${SERVER_DIR}" && go build -o "${TMP_DIR}/server.exe" ./cmd/server)
"${TMP_DIR}/server.exe" >"${TMP_DIR}/server.log" 2>&1 &
SERVER_PID=$!

echo "==> [2/5] 等待 /healthz 就绪（最多 60 秒）"
ready=0
for _ in $(seq 1 60); do
  if ! kill -0 "${SERVER_PID}" 2>/dev/null; then
    echo "错误：server 进程提前退出，日志末尾：" >&2
    tail -n 20 "${TMP_DIR}/server.log" >&2 || true
    exit 1
  fi
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

echo "==> [3/5] 导入 13 个种子模型"
BASE_URL="${BASE_URL}" bash "${SCRIPT_DIR}/seed-models.sh"

echo "==> [4/5] 逐条写入样例发现记录"
declare -A EXPECT=(
  ["01-create-web-01.json"]="新主机建档（ident=web-01 ip=10.0.1.11，预期调和动作 create）"
  ["02-update-web-01-os.json"]="同 ident 更新（os 字段变化，预期调和动作 update）"
  ["03-conflict-web-01-clone.json"]="同 IP 不同 ident（ident=web-01-clone ip=10.0.1.11，预期调和动作 conflict 入发现池）"
)
for f in "${SCRIPT_DIR}"/sample-records/*.json; do
  name="${f##*/}"
  echo "--- POST /api/v1/discovery-records <- ${name}：${EXPECT[${name}]:-样例记录}"
  curl -fsS -b "${MERIDIAN_COOKIE_JAR}" -X POST "${BASE_URL}/api/v1/discovery-records" \
    -H 'Content-Type: application/json' \
    --data-binary "@${f}" | json_pp
done

echo "==> [5/5] 查询主机 CI 清单"
HOST_MODEL_ID="$(
  curl -fsS -b "${MERIDIAN_COOKIE_JAR}" "${BASE_URL}/api/v1/models?keyword=host&page_size=200" | node -e '
    let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{
      const j=JSON.parse(d);
      const m=(j.items||[]).find(x=>x.code==="host");
      if(!m){console.error("未找到 code=host 的模型");process.exit(1)}
      console.log(m.id);
    })'
)"
echo "    host 模型 ID: ${HOST_MODEL_ID}"
curl -fsS -b "${MERIDIAN_COOKIE_JAR}" "${BASE_URL}/api/v1/cis?model_id=${HOST_MODEL_ID}" | json_pp
echo "    注：调和为异步流程，若 items 为空说明记录仍在队列或发现池中"

# 正常执行至此即全部步骤成功；trap 会停止 server 并清理临时目录。
echo
echo "演示完成："
echo "  [OK] server 启动并就绪（SQLite）"
echo "  [OK] 13 个种子模型导入"
echo "  [OK] 三条样例发现记录写入"
echo "  [OK] 主机 CI 清单查询"
