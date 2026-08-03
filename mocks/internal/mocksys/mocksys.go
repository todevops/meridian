// Package mocksys 装配并运行全部 9 个官方系统 mock 服务：
// n9e(:19001)、NetBox(:19002)、LibreNMS(:19003)、TSDB(:19004)、
// 阿里云(:19005)、火山引擎(:19006)、vcsim(:19007)、Oxidized(:19008)、
// JumpServer(:19010)，各自独立端口、goroutine 并行。
package mocksys

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// System 描述一个独立端口上的 mock 系统。
type System struct {
	Name    string       // 系统名（日志标识）
	Addr    string       // 监听地址
	Handler http.Handler // 该系统的完整路由
}

// listenAddr 读取环境变量覆盖监听地址，缺省用端口分配约定值。
func listenAddr(envKey, def string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return def
}

// Load 读取全部 fixture 并构建 9 个 mock 系统；任一 fixture 非法即失败。
func Load() ([]System, error) {
	builders := []struct {
		name   string
		envKey string
		def    string
		build  func() (http.Handler, error)
	}{
		{"n9e", "MOCK_N9E_ADDR", ":19001", newN9E},
		{"netbox", "MOCK_NETBOX_ADDR", ":19002", newNetBox},
		{"librenms", "MOCK_LIBRENMS_ADDR", ":19003", newLibreNMS},
		{"tsdb", "MOCK_TSDB_ADDR", ":19004", newTSDB},
		{"aliyun", "MOCK_ALIYUN_ADDR", ":19005", newAliyun},
		{"volcengine", "MOCK_VOLCENGINE_ADDR", ":19006", newVolcengine},
		{"vcsim", "MOCK_VCSIM_ADDR", ":19007", newVCSim},
		{"oxidized", "MOCK_OXIDIZED_ADDR", ":19008", newOxidized},
		{"jumpserver", "MOCK_JUMPSERVER_ADDR", ":19010", newJumpServer},
	}

	systems := make([]System, 0, len(builders))
	for _, b := range builders {
		h, err := b.build()
		if err != nil {
			return nil, fmt.Errorf("构建 %s mock 失败: %w", b.name, err)
		}
		addr := listenAddr(b.envKey, b.def)
		systems = append(systems, System{Name: b.name, Addr: addr, Handler: h})
		if b.name == "vcsim" {
			// vcsim 客户端需要的是完整 SDK URL 与登录凭据，启动时显式打印。
			log.Printf("[mockd] vcsim SDK 端点 http://127.0.0.1%s/sdk（默认凭据 user:pass，任意凭据均可登录）", addr)
		}
	}
	return systems, nil
}

// Run 并行启动全部 mock 系统，阻塞至 ctx 取消（优雅关闭）或任一系统运行失败。
func Run(ctx context.Context) error {
	systems, err := Load()
	if err != nil {
		return err
	}

	errCh := make(chan error, len(systems))
	servers := make([]*http.Server, 0, len(systems))
	for _, sys := range systems {
		srv := &http.Server{
			Addr:              sys.Addr,
			Handler:           sys.Handler,
			ReadHeaderTimeout: 5 * time.Second,
		}
		servers = append(servers, srv)
		go func() {
			log.Printf("[mockd] %-10s 监听于 %s", sys.Name, sys.Addr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("%s(%s) 运行失败: %w", sys.Name, sys.Addr, err)
			}
		}()
		// 可选启动钩子（如 Oxidized 一次性上报流程），后台执行、不阻塞启动。
		if s, ok := sys.Handler.(interface{ Start(context.Context) }); ok {
			s.Start(ctx)
		}
	}

	select {
	case <-ctx.Done():
		log.Println("[mockd] 收到退出信号，正在关闭全部 mock 系统……")
	case err := <-errCh:
		shutdownAll(servers)
		return err
	}
	shutdownAll(servers)
	log.Println("[mockd] 全部 mock 系统已退出")
	return nil
}

// shutdownAll 给全部服务 3 秒宽限完成优雅关闭。
func shutdownAll(servers []*http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(ctx)
	}
}
