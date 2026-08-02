#!/usr/bin/env bash
# 认证辅助（被 demo.sh / seed-models.sh source）：
# 除登录/健康检查外所有 /api/v1 接口需认证，脚本统一先登录获取会话 cookie。
# 可用环境变量：
#   MERIDIAN_AUTH_USER     登录用户名（默认 admin）
#   MERIDIAN_AUTH_PASSWORD 登录密码（默认取 ADMIN_INITIAL_PASSWORD，再缺省 admin123）
#   MERIDIAN_COOKIE_JAR    cookie 存储文件（默认临时文件）
# 使用方在 curl 中携带：-b "${MERIDIAN_COOKIE_JAR}"

MERIDIAN_AUTH_USER="${MERIDIAN_AUTH_USER:-admin}"
MERIDIAN_AUTH_PASSWORD="${MERIDIAN_AUTH_PASSWORD:-${ADMIN_INITIAL_PASSWORD:-admin123}}"
MERIDIAN_COOKIE_JAR="${MERIDIAN_COOKIE_JAR:-$(mktemp)}"

meridian_login() {
  echo "==> 以 ${MERIDIAN_AUTH_USER} 登录 ${BASE_URL}"
  if ! curl -fsS -c "${MERIDIAN_COOKIE_JAR}" -X POST "${BASE_URL}/api/v1/auth/login" \
      -H 'Content-Type: application/json' \
      -d "{\"username\":\"${MERIDIAN_AUTH_USER}\",\"password\":\"${MERIDIAN_AUTH_PASSWORD}\"}" >/dev/null; then
    echo "错误：登录失败（检查服务是否已启动、账号密码是否正确）" >&2
    exit 1
  fi
  echo "    登录成功，会话写入 ${MERIDIAN_COOKIE_JAR}"
}
