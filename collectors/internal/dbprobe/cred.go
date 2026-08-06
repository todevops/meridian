// 凭据文件加载：DBPROBE_CRED_FILE 指向的本地 JSON（0600 权限校验）。
package dbprobe

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strings"
)

// credEntry 是一条实例直连凭据（生产口径为凭据库 db 类型只读账号，
// 本期经本地文件下发；密码仅驻留进程内存，严禁打印日志）。
type credEntry struct {
	InstanceAddr string `json:"instance_addr"`
	Type         string `json:"type"` // mysql|redis；为空时回退 CI 的 component_type
	Username     string `json:"username"`
	Password     string `json:"password"`
}

// loadCreds 读取并校验凭据文件：JSON 数组，元素须含 instance_addr 与 username。
func loadCreds(path string) ([]credEntry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("DBPROBE_CRED_FILE 未配置（实例直连补采需要本地凭据文件）")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("读取凭据文件失败: %w", err)
	}
	if err := checkPerm(runtime.GOOS, info.Mode().Perm()); err != nil {
		return nil, fmt.Errorf("凭据文件 %s: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取凭据文件失败: %w", err)
	}
	var entries []credEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("解析凭据文件 JSON 失败: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("凭据文件为空（至少需要一条实例凭据）")
	}
	for i, e := range entries {
		if e.InstanceAddr == "" || e.Username == "" {
			return nil, fmt.Errorf("凭据文件第 %d 条缺少 instance_addr 或 username", i)
		}
	}
	return entries, nil
}

// checkPerm 校验凭据文件权限位：非 Windows 平台不允许任何组/他人权限（须 0600）。
// Windows 无 POSIX 权限位语义（os.Stat 恒报 0666/0444），跳过位校验，由 NTFS ACL 管控。
func checkPerm(goos string, perm fs.FileMode) error {
	if goos == "windows" {
		return nil
	}
	if perm&0o077 != 0 {
		return fmt.Errorf("权限 %04o 过宽：凭据文件须为 0600（chmod 600）", perm)
	}
	return nil
}

// findCred 按实例地址匹配凭据。
func findCred(creds []credEntry, addr string) (credEntry, bool) {
	for _, e := range creds {
		if e.InstanceAddr == addr {
			return e, true
		}
	}
	return credEntry{}, false
}
