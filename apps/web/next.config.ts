import type { NextConfig } from "next"

// 开发期将 /api/* 代理到本机 Go 后端，前端始终走同源请求，免 CORS
const API_PROXY_TARGET = "http://localhost:8080"

const nextConfig: NextConfig = {
  transpilePackages: ["@workspace/ui"],
  async rewrites() {
    return [
      {
        source: "/api/:path*",
        destination: `${API_PROXY_TARGET}/api/:path*`,
      },
    ]
  },
}

export default nextConfig
