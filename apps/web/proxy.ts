import { NextResponse, type NextRequest } from "next/server"

// 会话 cookie 名（与后端 auth.CookieName 一致）
const SESSION_COOKIE = "cmdb_token"

// 无需登录即可访问的页面
const PUBLIC_PATHS = ["/login"]

/**
 * Next 16 Proxy（原 middleware）：对页面路由做乐观的会话预检——
 * 无会话 cookie 直接重定向登录页，避免整页加载后才被 401 踢回。
 * 注意：这只是预检，真正的鉴权在 Go API 层强制执行。
 */
export default function proxy(req: NextRequest) {
  const { pathname } = req.nextUrl
  if (PUBLIC_PATHS.some((p) => pathname === p || pathname.startsWith(`${p}/`))) {
    return NextResponse.next()
  }
  const token = req.cookies.get(SESSION_COOKIE)?.value
  if (!token) {
    const url = req.nextUrl.clone()
    const redirect = pathname + req.nextUrl.search
    url.pathname = "/login"
    url.search = `?redirect=${encodeURIComponent(redirect)}`
    return NextResponse.redirect(url)
  }
  return NextResponse.next()
}

export const config = {
  // 只拦截页面路由：跳过 API 代理、Next 内部资源与静态文件
  matcher: ["/((?!api|_next/static|_next/image|favicon.ico).*)"],
}
