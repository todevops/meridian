// 测试共享辅助：构建含凭据加解密与调度器的完整路由。
package httpapi

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"meridian/server/internal/auth"
	"meridian/server/internal/credentials"
	"meridian/server/internal/discovery"
	"meridian/server/internal/n9e"
	"meridian/server/internal/scheduler"
)

// newTestRouter 以测试密钥构建完整路由（凭据/任务端点可用；exec 白名单为临时目录）。
// n9eClient 可空（默认 nil，即"n9e 未配置"形态）。
func newTestRouter(t *testing.T, db *gorm.DB, authSvc *auth.Service, n9eClient ...*n9e.Client) *gin.Engine {
	t.Helper()
	cipher, err := credentials.NewCipher("httpapi-test-key")
	if err != nil {
		t.Fatalf("创建加解密器失败: %v", err)
	}
	pipeline := discovery.NewPipeline(db)
	sched := scheduler.New(db, pipeline, cipher, t.TempDir(), 5*time.Second)
	var nc *n9e.Client
	if len(n9eClient) > 0 {
		nc = n9eClient[0]
	}
	return NewRouter(db, pipeline, authSvc, cipher, sched, nc)
}
