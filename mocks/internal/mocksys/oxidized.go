package mocksys

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// oxidizedNode 是 Oxidized 风格的节点条目（/nodes 响应元素）。
type oxidizedNode struct {
	Name   string `json:"name"`
	IP     string `json:"ip"`
	Model  string `json:"model"`
	Group  string `json:"group"`
	Status string `json:"status"` // success / never / fail
	Time   int64  `json:"time"`   // 最近一次备份时间（Unix 秒）
}

// oxidizedState 是 Oxidized mock 的内存态：节点清单可被一次性上报流程刷新。
type oxidizedState struct {
	mu    sync.RWMutex
	nodes []oxidizedNode
}

// oxidizedDevice 对应 CMDB GET /api/v1/integrations/oxidized/devices 返回的设备条目。
type oxidizedDevice struct {
	Name  string `json:"name"`
	IP    string `json:"ip"`
	Model string `json:"model"`
	Group string `json:"group"`
}

// oxidizedEvent 是上报给 CMDB webhook（POST /api/v1/integrations/oxidized/events）的事件体。
type oxidizedEvent struct {
	Node    string `json:"node"`           // 设备名（与设备源清单一致）
	IP      string `json:"ip"`             // 管理 IP
	Model   string `json:"model"`          // 设备型号
	Group   string `json:"group"`          // 分组
	Event   string `json:"event"`          // backup / change
	Status  string `json:"status"`         // success / fail
	Time    string `json:"time"`           // RFC3339
	User    string `json:"user,omitempty"` // 变更操作者（change 事件可带）
	Message string `json:"message"`        // 人类可读摘要
}

// newOxidized 构建 Oxidized mock（:19008），只读端点（无需鉴权，供 curl 直测）：
//   - GET /nodes              节点清单（Oxidized /nodes.json 风格）
//   - GET /node/fetch/{name}  该节点最新配置文本（text/plain）
//
// 节点清单默认来自 fixtures/oxidized-nodes.json；一次性上报流程（OXIDIZED_ONCE=true，默认开）
// 成功从 CMDB 拉到设备清单后会以真实清单替换，使 /nodes 与上报动作互相印证。
func newOxidized() (http.Handler, error) {
	raw, err := readFixture("oxidized-nodes.json")
	if err != nil {
		return nil, fmt.Errorf("读取 oxidized-nodes.json 失败: %w", err)
	}
	var nodes []oxidizedNode
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return nil, fmt.Errorf("解析 oxidized-nodes.json 失败: %w", err)
	}
	st := &oxidizedState{nodes: nodes}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes", func(w http.ResponseWriter, r *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()
		writeJSON(w, http.StatusOK, st.nodes)
	})
	mux.HandleFunc("GET /node/fetch/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		st.mu.RLock()
		var node *oxidizedNode
		for i := range st.nodes {
			if st.nodes[i].Name == name {
				node = &st.nodes[i]
				break
			}
		}
		st.mu.RUnlock()
		if node == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("节点 %s 不存在", name)})
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "! 配置由 Oxidized mock（%s）生成\n! 节点: %s (%s)\n! 型号: %s  分组: %s\n!\nhostname %s\n!\ninterface Vlanif1\n ip address %s 255.255.255.0\n!\nntp server 10.30.0.1\n",
			r.Host, node.Name, node.IP, node.Model, node.Group, node.Name, node.IP)
	})
	return oxidizedWithStart(mux, st), nil
}

// oxidizedWithStart 把一次性上报流程挂到 handler 上（System.Start 钩子，见 mocksys.go）。
type oxidizedHandler struct {
	http.Handler
	st *oxidizedState
}

func oxidizedWithStart(h http.Handler, st *oxidizedState) *oxidizedHandler {
	return &oxidizedHandler{Handler: h, st: st}
}

// Start 在 mockd 启动全部系统后被调用：OXIDIZED_ONCE=false 时不做任何事，
// 否则后台执行一次性流程——拉设备清单 → 逐台上报 backup 事件 → 首台补报 change 事件。
func (h *oxidizedHandler) Start(ctx context.Context) {
	if strings.EqualFold(os.Getenv("OXIDIZED_ONCE"), "false") {
		log.Println("[oxidized] OXIDIZED_ONCE=false，跳过一次性上报流程（仅保留只读端点）")
		return
	}
	go h.runOnce(ctx)
}

// runOnce 执行一次性上报流程，每步结果均打日志；CMDB 未就绪时按 3 秒间隔最多重试 20 次。
func (h *oxidizedHandler) runOnce(ctx context.Context) {
	apiURL := strings.TrimRight(envOr("CMDB_API_URL", "http://127.0.0.1:8080"), "/")
	hookToken := envOr("OXIDIZED_TOKEN", "dev-oxidized-token")
	client := &http.Client{Timeout: 10 * time.Second}

	token, err := meridianToken(ctx, client, apiURL)
	if err != nil {
		log.Printf("[oxidized] 获取 CMDB 访问令牌失败，一次性流程中止: %v", err)
		return
	}

	var devices []oxidizedDevice
	for attempt := 1; attempt <= 20; attempt++ {
		devices, err = fetchOxidizedDevices(ctx, client, apiURL, token)
		if err == nil {
			break
		}
		log.Printf("[oxidized] 拉取设备清单失败（第 %d/20 次，3 秒后重试）: %v", attempt, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
	if err != nil {
		log.Printf("[oxidized] 拉取设备清单最终失败，一次性流程中止: %v", err)
		return
	}
	log.Printf("[oxidized] 从 CMDB 拉到 %d 台网络设备", len(devices))
	if len(devices) == 0 {
		log.Println("[oxidized] 设备清单为空（network_device 模型或 CI 未就绪），一次性流程结束")
		return
	}

	// 以真实清单刷新 /nodes，使只读端点与上报动作互相印证。
	h.st.mu.Lock()
	h.st.nodes = make([]oxidizedNode, 0, len(devices))
	for _, d := range devices {
		h.st.nodes = append(h.st.nodes, oxidizedNode{Name: d.Name, IP: d.IP, Model: d.Model, Group: d.Group, Status: "never"})
	}
	h.st.mu.Unlock()

	// 逐台上报 backup 事件，时间错开（越靠前的设备备份时间越早，间隔 5 分钟）。
	base := time.Now().Add(-time.Duration(len(devices)) * 5 * time.Minute)
	for i, d := range devices {
		evt := oxidizedEvent{
			Node: d.Name, IP: d.IP, Model: d.Model, Group: d.Group,
			Event: "backup", Status: "success",
			Time:    base.Add(time.Duration(i) * 5 * time.Minute).UTC().Format(time.RFC3339),
			Message: "配置备份成功（Oxidized mock）",
		}
		if err := postOxidizedEvent(ctx, client, apiURL, hookToken, evt); err != nil {
			log.Printf("[oxidized] 上报 backup 事件失败 node=%s: %v", d.Name, err)
			continue
		}
		log.Printf("[oxidized] 已上报 backup 事件 node=%s time=%s", d.Name, evt.Time)
		h.st.mu.Lock()
		for j := range h.st.nodes {
			if h.st.nodes[j].Name == d.Name {
				h.st.nodes[j].Status = "success"
				if ts, err := time.Parse(time.RFC3339, evt.Time); err == nil {
					h.st.nodes[j].Time = ts.Unix()
				}
			}
		}
		h.st.mu.Unlock()
	}

	// 首台设备补报一条 change 事件（演示配置变更回写）。
	first := devices[0]
	evt := oxidizedEvent{
		Node: first.Name, IP: first.IP, Model: first.Model, Group: first.Group,
		Event: "change", Status: "success",
		Time:    time.Now().UTC().Format(time.RFC3339),
		User:    "oxidized-mock",
		Message: "检测到配置变更（Oxidized mock）：ntp server 行更新",
	}
	if err := postOxidizedEvent(ctx, client, apiURL, hookToken, evt); err != nil {
		log.Printf("[oxidized] 上报 change 事件失败 node=%s: %v", first.Name, err)
	} else {
		log.Printf("[oxidized] 已上报 change 事件 node=%s", first.Name)
	}
	log.Println("[oxidized] 一次性上报流程结束")
}

// meridianToken 解析 CMDB 访问令牌：优先 MERIDIAN_TOKEN，
// 否则用 MERIDIAN_USERNAME/MERIDIAN_PASSWORD（默认 admin/admin123）登录换取。
func meridianToken(ctx context.Context, client *http.Client, apiURL string) (string, error) {
	if t := os.Getenv("MERIDIAN_TOKEN"); t != "" {
		return t, nil
	}
	username := envOr("MERIDIAN_USERNAME", "admin")
	password := envOr("MERIDIAN_PASSWORD", "admin123")
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("登录请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("登录返回状态码 %d: %s", resp.StatusCode, truncate(raw, 200))
	}
	var parsed struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Token == "" {
		return "", fmt.Errorf("登录响应缺少 token: %s", truncate(raw, 200))
	}
	return parsed.Token, nil
}

// fetchOxidizedDevices 从 CMDB 拉取 Oxidized 设备源清单。
func fetchOxidizedDevices(ctx context.Context, client *http.Client, apiURL, token string) ([]oxidizedDevice, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/api/v1/integrations/oxidized/devices", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("设备清单返回状态码 %d: %s", resp.StatusCode, truncate(raw, 200))
	}
	var devices []oxidizedDevice
	if err := json.Unmarshal(raw, &devices); err != nil {
		return nil, fmt.Errorf("解析设备清单失败: %w", err)
	}
	return devices, nil
}

// postOxidizedEvent 向 CMDB webhook 上报一条 Oxidized 事件（X-Oxidized-Token 头鉴权）。
func postOxidizedEvent(ctx context.Context, client *http.Client, apiURL, hookToken string, evt oxidizedEvent) error {
	body, _ := json.Marshal(evt)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/api/v1/integrations/oxidized/events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Oxidized-Token", hookToken)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook 返回状态码 %d: %s", resp.StatusCode, truncate(raw, 200))
	}
	return nil
}

// envOr 读取环境变量，为空时返回默认值。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// truncate 截断字节串用于日志输出。
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
