#!/usr/bin/env bash
# 认证辅助（被 demo.sh / seed-models.sh source）：
# 除登录/健康检查外所有 /api/v1 接口需认证，脚本统一先登录获取会话 cookie。
# 可用环境变量：
#   CMDB_AUTH_USER     登录用户名（默认 admin）
#   CMDB_AUTH_PASSWORD 登录密码（默认取 ADMIN_INITIAL_PASSWORD，再缺省 admin123）
#   CMDB_COOKIE_JAR    cookie 存储文件（默认临时文件）
# 使用方在 curl 中携带：-b "${CMDB_COOKIE_JAR}"

CMDB_AUTH_USER="${CMDB_AUTH_USER:-admin}"
CMDB_AUTH_PASSWORD="${CMDB_AUTH_PASSWORD:-${ADMIN_INITIAL_PASSWORD:-admin123}}"
CMDB_COOKIE_JAR="${CMDB_COOKIE_JAR:-$(mktemp)}"

cmdb_login() {
  echo "==> 以 ${CMDB_AUTH_USER} 登录 ${BASE_URL}"
  if ! curl -fsS -c "${CMDB_COOKIE_JAR}" -X POST "${BASE_URL}/api/v1/auth/login" \
      -H 'Content-Type: application/json' \
      -d "{\"username\":\"${CMDB_AUTH_USER}\",\"password\":\"${CMDB_AUTH_PASSWORD}\"}" >/dev/null; then
    echo "错误：登录失败（检查服务是否已启动、账号密码是否正确）" >&2
    exit 1
  fi
  echo "    登录成功，会话写入 ${CMDB_COOKIE_JAR}"
}
