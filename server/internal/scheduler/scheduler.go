// Package scheduler 实现采集任务调度与执行（F-033）：
// 后台 goroutine 每 10 秒扫描到期的 enabled 任务并执行，任务级互斥防重入；
// 手动触发（POST /discovery/tasks/{id}/run）与周期调度走同一互斥通道。
//
// 执行器两类：
//   - builtin:n9e-consumer：进程内复用 n9e 消费器逻辑（拉 targets → 摄入管道）；
//   - exec:<binary>：执行白名单目录内的采集器二进制，按 credential_id 解密
//     secret 注入环境变量（键名大写），支持超时 kill 与输出截断 4KB 入执行记录。
package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"meridian/server/internal/credentials"
	"meridian/server/internal/discovery"
	"meridian/server/internal/n9e"
	"meridian/server/internal/store"
)

// ScanInterval 是调度扫描间隔（10 秒）。
const ScanInterval = 10 * time.Second

// DefaultExecTimeout 是 exec 执行器默认超时（5 分钟）。
const DefaultExecTimeout = 5 * time.Minute

// maxErrorLen / maxOutputLen 为错误摘要与输出尾巴的截断上限。
const (
	maxErrorLen  = 1000
	maxOutputLen = 4096
)

// ErrTaskRunning 表示任务已在运行中（互斥防重入）。
var ErrTaskRunning = fmt.Errorf("任务正在运行中，拒绝重入")

// Scheduler 是采集任务调度器。
type Scheduler struct {
	db           *gorm.DB
	pipeline     *discovery.Pipeline
	cipher       *credentials.Cipher
	execAllowDir string        // exec 执行器二进制白名单目录（绝对路径）
	execTimeout  time.Duration // exec 默认超时

	mu      sync.Mutex
	running map[string]bool // 任务级互斥锁集合
}

// New 创建调度器。execAllowDir 为空时 exec 执行器一律拒绝执行。
func New(db *gorm.DB, pipeline *discovery.Pipeline, cipher *credentials.Cipher, execAllowDir string, execTimeout time.Duration) *Scheduler {
	if execTimeout <= 0 {
		execTimeout = DefaultExecTimeout
	}
	if execAllowDir != "" {
		if abs, err := filepath.Abs(execAllowDir); err == nil {
			execAllowDir = abs
		}
	}
	return &Scheduler{
		db:           db,
		pipeline:     pipeline,
		cipher:       cipher,
		execAllowDir: execAllowDir,
		execTimeout:  execTimeout,
		running:      map[string]bool{},
	}
}

// Run 启动调度循环：每 10 秒扫描一次到期任务，直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanDue(ctx)
		}
	}
}

// scanDue 扫描到期任务（enabled 且到达下次执行时间）并异步执行。
func (s *Scheduler) scanDue(ctx context.Context) {
	var tasks []store.DiscoveryTask
	if err := s.db.WithContext(ctx).
		Where("enabled = ? AND status <> ?", true, store.TaskStatusRunning).
		Find(&tasks).Error; err != nil {
		return
	}
	now := time.Now()
	for _, task := range tasks {
		if task.LastRunAt != nil && task.LastRunAt.Add(time.Duration(task.IntervalSeconds)*time.Second).After(now) {
			continue // 未到期
		}
		go func(t store.DiscoveryTask) {
			if _, err := s.runTask(ctx, t, "schedule"); err != nil && err != ErrTaskRunning {
				// 执行失败已落执行记录与任务状态，此处仅兜底日志场景可忽略。
				_ = err
			}
		}(task)
	}
}

// RunTask 手动触发任务执行（同步等待完成），返回执行记录。
func (s *Scheduler) RunTask(ctx context.Context, taskID string) (store.DiscoveryTaskRun, error) {
	var task store.DiscoveryTask
	if err := s.db.WithContext(ctx).First(&task, "id = ?", taskID).Error; err != nil {
		return store.DiscoveryTaskRun{}, err
	}
	return s.runTask(ctx, task, "manual")
}

// tryLock 获取任务级互斥锁。
func (s *Scheduler) tryLock(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[taskID] {
		return false
	}
	s.running[taskID] = true
	return true
}

// unlock 释放任务级互斥锁。
func (s *Scheduler) unlock(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, taskID)
}

// runTask 执行一次任务：建执行记录 → 调度执行器 → 回写执行结果与任务状态。
func (s *Scheduler) runTask(ctx context.Context, task store.DiscoveryTask, trigger string) (store.DiscoveryTaskRun, error) {
	if !s.tryLock(task.ID) {
		return store.DiscoveryTaskRun{}, ErrTaskRunning
	}
	defer s.unlock(task.ID)

	// 标记运行中并创建执行记录。
	s.db.WithContext(ctx).Model(&store.DiscoveryTask{}).Where("id = ?", task.ID).
		Update("status", store.TaskStatusRunning)
	run := store.DiscoveryTaskRun{TaskID: task.ID, Trigger: trigger, StartedAt: time.Now()}
	if err := s.db.WithContext(ctx).Create(&run).Error; err != nil {
		s.db.WithContext(ctx).Model(&store.DiscoveryTask{}).Where("id = ?", task.ID).
			Update("status", store.TaskStatusIdle)
		return store.DiscoveryTaskRun{}, fmt.Errorf("创建执行记录失败: %w", err)
	}

	// 解密凭据（如配置）：解密失败即本次执行失败。
	var secret map[string]any
	var execErr error
	produced := 0
	output := ""
	if task.CredentialID != nil && *task.CredentialID != "" {
		secret, execErr = s.loadSecret(ctx, *task.CredentialID)
	}
	if execErr == nil {
		produced, output, execErr = s.execute(ctx, task, secret)
	}
	// 凭据使用计数与审计：仅当凭据成功解密并进入执行阶段时计一次使用。
	if task.CredentialID != nil && *task.CredentialID != "" && secret != nil {
		s.db.WithContext(ctx).Model(&store.Credential{}).Where("id = ?", *task.CredentialID).
			UpdateColumn("use_count", gorm.Expr("use_count + 1"))
		s.db.WithContext(ctx).Create(&store.CredentialAudit{
			CredentialID: *task.CredentialID,
			Action:       store.CredentialAuditUse,
			Operator:     "system",
			Source:       "task:" + task.Name,
		})
	}

	// 回写执行记录与任务状态。
	finished := time.Now()
	run.FinishedAt = &finished
	run.Success = execErr == nil
	run.Produced = produced
	run.Output = truncateTail(output, maxOutputLen)
	updates := map[string]any{
		"last_run_at": run.StartedAt,
		"run_count":   gorm.Expr("run_count + 1"),
	}
	if execErr != nil {
		run.ErrorSummary = truncateTail(execErr.Error(), maxErrorLen)
		updates["status"] = store.TaskStatusError
		updates["last_error"] = run.ErrorSummary
		updates["fail_count"] = gorm.Expr("fail_count + 1")
	} else {
		updates["status"] = store.TaskStatusIdle
		updates["last_success_at"] = finished
		updates["last_error"] = ""
	}
	if err := s.db.WithContext(ctx).Model(&store.DiscoveryTaskRun{}).Where("id = ?", run.ID).
		Updates(map[string]any{
			"finished_at":   run.FinishedAt,
			"success":       run.Success,
			"produced":      run.Produced,
			"error_summary": run.ErrorSummary,
			"output":        run.Output,
		}).Error; err != nil {
		return run, fmt.Errorf("回写执行记录失败: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&store.DiscoveryTask{}).Where("id = ?", task.ID).
		Updates(updates).Error; err != nil {
		return run, fmt.Errorf("回写任务状态失败: %w", err)
	}
	return run, execErr
}

// loadSecret 加载并解密凭据 secret JSON（不会把明文写入任何持久化字段）。
func (s *Scheduler) loadSecret(ctx context.Context, credentialID string) (map[string]any, error) {
	if s.cipher == nil {
		return nil, fmt.Errorf("服务端未配置凭据密钥，无法解密凭据")
	}
	var cred store.Credential
	if err := s.db.WithContext(ctx).First(&cred, "id = ?", credentialID).Error; err != nil {
		return nil, fmt.Errorf("凭据 %s 不存在", credentialID)
	}
	plain, err := s.cipher.Decrypt(cred.SecretCiphertext)
	if err != nil {
		return nil, fmt.Errorf("解密凭据失败: %v", err)
	}
	secret := map[string]any{}
	if err := json.Unmarshal(plain, &secret); err != nil {
		return nil, fmt.Errorf("凭据 secret 不是合法 JSON 对象: %v", err)
	}
	return secret, nil
}

// execute 按 collector_type 分发到具体执行器。
func (s *Scheduler) execute(ctx context.Context, task store.DiscoveryTask, secret map[string]any) (produced int, output string, err error) {
	cfg := task.Config
	switch {
	case task.CollectorType == store.CollectorBuiltinN9E:
		return s.execBuiltinN9E(ctx, cfg, secret)
	case strings.HasPrefix(task.CollectorType, store.CollectorTypeExecPrefix):
		return s.execBinary(ctx, cfg, secret)
	default:
		return 0, "", fmt.Errorf("不支持的采集器类型 %q", task.CollectorType)
	}
}

// execBuiltinN9E 进程内执行 n9e 消费：api_url 取 config；token 优先取凭据
// secret 的 token 字段，其次 config.api_token。
func (s *Scheduler) execBuiltinN9E(ctx context.Context, cfg map[string]any, secret map[string]any) (int, string, error) {
	apiURL, _ := cfg["api_url"].(string)
	if apiURL == "" {
		return 0, "", fmt.Errorf("builtin:n9e-consumer 需要 config.api_url")
	}
	token, _ := cfg["api_token"].(string)
	if secret != nil {
		if v, ok := secret["token"].(string); ok && v != "" {
			token = v // 凭据优先于 config 内联 token
		}
	}
	consumer := n9e.NewConsumer(n9e.NewClient(apiURL, token), s.pipeline, 0)
	produced, err := consumer.RunOnce(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("n9e 消费失败: %w", err)
	}
	return produced, fmt.Sprintf("n9e 消费完成: 摄入接受 %d 条", produced), nil
}

// execBinary 执行白名单目录内的采集器二进制：
//   - binary 必须解析到白名单目录内（防路径穿越）；
//   - 凭据 secret 的每个键以大写键名注入环境变量（如 {"token":"x"} → TOKEN=x）；
//   - config.env 显式环境变量在注入后覆盖同名项；
//   - 超时（config.timeout_seconds 或默认 5 分钟）即 kill；
//   - stdout/stderr 合并输出由调用方截断 4KB 入执行记录；
//   - stdout 中形如 CMDB_PRODUCED=<n> 的行可声明产出条数（缺省 0）。
func (s *Scheduler) execBinary(ctx context.Context, cfg map[string]any, secret map[string]any) (int, string, error) {
	if s.execAllowDir == "" {
		return 0, "", fmt.Errorf("未配置 exec 白名单目录（CMDB_EXEC_ALLOWED_DIR），拒绝执行外部二进制")
	}
	binary, _ := cfg["binary"].(string)
	if binary == "" {
		return 0, "", fmt.Errorf("exec 执行器需要 config.binary")
	}
	resolved, err := s.resolveBinary(binary)
	if err != nil {
		return 0, "", err
	}

	var args []string
	if raw, ok := cfg["args"].([]any); ok {
		for _, a := range raw {
			args = append(args, fmt.Sprint(a))
		}
	}
	timeout := s.execTimeout
	if v, ok := cfg["timeout_seconds"]; ok {
		if n, err := strconv.Atoi(fmt.Sprint(v)); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(execCtx, resolved, args...)
	cmd.Env = os.Environ()
	// 凭据注入：键名大写作为环境变量名。
	for k, v := range secret {
		cmd.Env = append(cmd.Env, strings.ToUpper(k)+"="+fmt.Sprint(v))
	}
	// config.env 显式覆盖同名环境变量。
	if rawEnv, ok := cfg["env"].(map[string]any); ok {
		for k, v := range rawEnv {
			cmd.Env = append(cmd.Env, k+"="+fmt.Sprint(v))
		}
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	out := buf.String()
	produced := parseProduced(out)

	if execCtx.Err() == context.DeadlineExceeded {
		return produced, out, fmt.Errorf("执行超时（%s），进程已终止", timeout)
	}
	if runErr != nil {
		return produced, out, fmt.Errorf("进程退出失败: %v", runErr)
	}
	return produced, out, nil
}

// resolveBinary 解析二进制路径并断言其位于白名单目录内。
func (s *Scheduler) resolveBinary(binary string) (string, error) {
	candidate := binary
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.execAllowDir, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("解析二进制路径失败: %v", err)
	}
	// 防路径穿越：必须在白名单目录之内。
	rel, err := filepath.Rel(s.execAllowDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("二进制 %q 不在白名单目录 %s 内，拒绝执行", binary, s.execAllowDir)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("二进制 %q 不存在: %v", abs, err)
	}
	return abs, nil
}

// parseProduced 从输出中解析 CMDB_PRODUCED=<n> 产出声明（取最后一处）。
func parseProduced(out string) int {
	produced := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "CMDB_PRODUCED="); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
				produced = n
			}
		}
	}
	return produced
}

// truncateTail 保留字符串尾部至多 max 字节（保留最近的日志更有意义）。
func truncateTail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
