import { Geist_Mono, Inter } from "next/font/google"
import type { Metadata } from "next"

import "@workspace/ui/globals.css"
import { ThemeProvider } from "@/components/theme-provider"
import { AppSidebar } from "@/components/app-sidebar"
import { cn } from "@workspace/ui/lib/utils"

const inter = Inter({ subsets: ["latin"], variable: "--font-sans" })

const fontMono = Geist_Mono({
  subsets: ["latin"],
  variable: "--font-mono",
})

export const metadata: Metadata = {
  title: "CMDB 配置管理中心",
  description: "企业级纯自研 CMDB 平台",
}

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode
}>) {
  return (
    <html
      lang="zh-CN"
      suppressHydrationWarning
      className={cn("antialiased", fontMono.variable, "font-sans", inter.variable)}
    >
      <body>
        <ThemeProvider>
          <div className="flex min-h-svh">
            <AppSidebar />
            <main className="min-w-0 flex-1">{children}</main>
          </div>
        </ThemeProvider>
      </body>
    </html>
  )
}
