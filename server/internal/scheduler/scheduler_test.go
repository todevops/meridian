// 调度器单测：exec 白名单/环境注入/产出解析/超时 kill、任务级互斥防重入、凭据使用计数。
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"meridian/server/internal/credentials"
	"meridian/server/internal/discovery"
	"meridian/server/internal/store"
)

// setup 打开独立内存库并构建调度器（含加解密器与临时白名单目录）。
func setup(t *testing.T) (*gorm.DB, *Scheduler, string) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(store.AllModels()...); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	cipher, err := credentials.NewCipher("sched-test-key")
	if err != nil {
		t.Fatalf("创建加解密器失败: %v", err)
	}
	allowDir := t.TempDir()
	s := New(db, discovery.NewPipeline(db), cipher, allowDir, 5*time.Second)
	return db, s, allowDir
}

// buildSleeper 编译测试辅助二进制到白名单目录，返回二进制文件名。
func buildSleeper(t *testing.T, allowDir string) string {
	t.Helper()
	name := "sleeper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(allowDir, name)
	cmd := exec.Command("go", "build", "-o", out, "./testdata/sleeper")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译 sleeper 失败: %v\n%s", err, combined)
	}
	return name
}

// mustCredential 创建加密凭据并入库，返回凭据 ID。
func mustCredential(t *testing.T, db *gorm.DB, s *Scheduler, secret map[string]any) string {
	t.Helper()
	raw, _ := jsonMarshal(secret)
	ct, err := s.cipher.Encrypt(raw)
	if err != nil {
		t.Fatalf("加密 secret 失败: %v", err)
	}
	cred := store.Credential{Name: "测试凭据", Type: store.CredentialTypeN9E, SecretCiphertext: ct}
	if err := db.Create(&cred).Error; err != nil {
		t.Fatalf("创建凭据失败: %v", err)
	}
	return cred.ID
}

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// mustTask 创建采集任务。
func mustTask(t *testing.T, db *gorm.DB, name, collectorType string, credID *string, cfg map[string]any) store.DiscoveryTask {
	t.Helper()
	task := store.DiscoveryTask{
		Name:            name,
		CollectorType:   collectorType,
		CredentialID:    credID,
		IntervalSeconds: 60,
		Enabled:         true,
		Config:          datatypes.JSONMap(cfg),
		Status:          store.TaskStatusIdle,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	return task
}

func TestExecBinarySuccess(t *testing.T) {
	db, s, allowDir := setup(t)
	binary := buildSleeper(t, allowDir)
	credID := mustCredential(t, db, s, map[string]any{"token": "secret-token-123"})
	task := mustTask(t, db, "exec-ok", "exec:"+binary, &credID, map[string]any{
		"binary": binary, "args": []any{"10"}, "env": map[string]any{"MY_CONFIG": "from-config"},
	})

	run, err := s.RunTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !run.Success {
		t.Fatalf("执行应成功: %+v", run)
	}
	if run.Produced != 3 {
		t.Fatalf("应解析 CMDB_PRODUCED=3，得到 %d", run.Produced)
	}
	// 凭据以键名大写注入环境变量；config.env 生效。
	if !strings.Contains(run.Output, "SECRET_TOKEN=secret-token-123") {
		t.Fatalf("凭据注入缺失: %q", run.Output)
	}
	if !strings.Contains(run.Output, "CONFIG_ENV=from-config") {
		t.Fatalf("config.env 注入缺失: %q", run.Output)
	}

	// 任务状态回写：run_count=1、status=idle、last_success_at 非空。
	var updated store.DiscoveryTask
	if err := db.First(&updated, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if updated.RunCount != 1 || updated.Status != store.TaskStatusIdle || updated.LastSuccessAt == nil {
		t.Fatalf("任务状态回写异常: %+v", updated)
	}

	// 凭据使用计数 +1 并记 use 审计。
	var cred store.Credential
	if err := db.First(&cred, "id = ?", credID).Error; err != nil {
		t.Fatalf("查询凭据失败: %v", err)
	}
	if cred.UseCount != 1 {
		t.Fatalf("凭据使用计数应为 1，得到 %d", cred.UseCount)
	}
	var auditCount int64
	db.Model(&store.CredentialAudit{}).
		Where("credential_id = ? AND action = ? AND source = ?", credID, "use", "task:exec-ok").
		Count(&auditCount)
	if auditCount != 1 {
		t.Fatalf("应有 1 条 use 审计，得到 %d", auditCount)
	}
}

func TestExecBinaryTimeout(t *testing.T) {
	db, s, allowDir := setup(t)
	binary := buildSleeper(t, allowDir)
	task := mustTask(t, db, "exec-timeout", "exec:"+binary, nil, map[string]any{
		"binary": binary, "args": []any{"60000"}, "timeout_seconds": 1,
	})

	start := time.Now()
	run, err := s.RunTask(context.Background(), task.ID)
	if err == nil {
		t.Fatal("超时任务应返回错误")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("超时未在预期时间内 kill 进程")
	}
	if run.Success {
		t.Fatal("超时执行记录 success 应为 false")
	}
	if !strings.Contains(run.ErrorSummary, "超时") {
		t.Fatalf("错误摘要应含超时说明: %q", run.ErrorSummary)
	}
	// 任务进入 error 状态并累计失败次数。
	var updated store.DiscoveryTask
	db.First(&updated, "id = ?", task.ID)
	if updated.Status != store.TaskStatusError || updated.FailCount != 1 {
		t.Fatalf("失败状态回写异常: %+v", updated)
	}
}

func TestExecBinaryWhitelist(t *testing.T) {
	db, s, _ := setup(t)
	// 白名单外绝对路径。
	task := mustTask(t, db, "exec-outside", "exec:anything", nil, map[string]any{
		"binary": os.TempDir() + string(filepath.Separator) + "not-allowed-bin",
	})
	if _, err := s.RunTask(context.Background(), task.ID); err == nil ||
		!strings.Contains(err.Error(), "白名单") {
		t.Fatalf("白名单外二进制应被拒绝: %v", err)
	}
	// 路径穿越。
	task2 := mustTask(t, db, "exec-traverse", "exec:anything", nil, map[string]any{
		"binary": "../../../etc/passwd",
	})
	if _, err := s.RunTask(context.Background(), task2.ID); err == nil ||
		!strings.Contains(err.Error(), "白名单") {
		t.Fatalf("路径穿越应被拒绝: %v", err)
	}
}

func TestTaskMutexPreventsReentry(t *testing.T) {
	db, s, allowDir := setup(t)
	binary := buildSleeper(t, allowDir)
	task := mustTask(t, db, "exec-mutex", "exec:"+binary, nil, map[string]any{
		"binary": binary, "args": []any{"800"},
	})

	// 第一次执行在后台运行（约 800ms）。
	done := make(chan error, 1)
	go func() {
		_, err := s.RunTask(context.Background(), task.ID)
		done <- err
	}()
	// 等待其进入 running 状态。
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		running := s.running[task.ID]
		s.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("后台执行未进入运行状态")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 重入应被拒绝。
	if _, err := s.RunTask(context.Background(), task.ID); err != ErrTaskRunning {
		t.Fatalf("重入应返回 ErrTaskRunning，得到 %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("首次执行应成功: %v", err)
	}
	// 执行完成后互斥释放，可再次运行。
	if _, err := s.RunTask(context.Background(), task.ID); err != nil {
		t.Fatalf("释放后再次执行应成功: %v", err)
	}
}

func TestUnsupportedCollectorType(t *testing.T) {
	db, s, _ := setup(t)
	task := mustTask(t, db, "bad-type", "builtin:not-exist", nil, map[string]any{})
	if _, err := s.RunTask(context.Background(), task.ID); err == nil ||
		!strings.Contains(err.Error(), "不支持") {
		t.Fatalf("未知采集器类型应报错: %v", err)
	}
}
