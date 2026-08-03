// K8s 集成（/api/v1/k8s，F-024）处理器：Pod 实况直查 apiserver 代理（不落库）。
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"meridian/server/internal/store"
)

// k8sPodView 是 Pod 精简视图（契约形状）。
type k8sPodView struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Phase        string `json:"phase"`
	Node         string `json:"node"`
	RestartCount int    `json:"restart_count"`
	AgeSeconds   int64  `json:"age_seconds"`
}

// k8sPodList 对应 apiserver /api/v1/pods 响应（仅取所需字段）。
type k8sPodList struct {
	Items []struct {
		Metadata struct {
			Name              string    `json:"name"`
			Namespace         string    `json:"namespace"`
			CreationTimestamp time.Time `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			NodeName string `json:"nodeName"`
		} `json:"spec"`
		Status struct {
			Phase             string `json:"phase"`
			ContainerStatuses []struct {
				RestartCount int `json:"restartCount"`
			} `json:"containerStatuses"`
		} `json:"status"`
	} `json:"items"`
}

// k8sEndpoint 是 apiserver 连接信息。
type k8sEndpoint struct {
	APIURL string
	Token  string
}

// resolveK8SEndpoint 解析 apiserver 凭据：优先 credentials 表 type=kubeconfig
// （最近更新的一条，secret 取 api_url/server 与 token 字段），缺省回退环境变量
// K8S_API_URL（默认 http://localhost:19009）与 K8S_TOKEN（默认 dev-k8s-token）。
func (s *Server) resolveK8SEndpoint() k8sEndpoint {
	ep := k8sEndpoint{
		APIURL: normalizeK8SURL(defaultStringEnv("K8S_API_URL", "http://localhost:19009")),
		Token:  defaultStringEnv("K8S_TOKEN", "dev-k8s-token"),
	}
	var cred store.Credential
	if err := s.db.Where("type = ?", store.CredentialTypeKubeconfig).
		Order("updated_at DESC").Limit(1).First(&cred).Error; err != nil {
		return ep // 无 kubeconfig 凭据：用环境变量默认
	}
	raw, err := s.credCipher.Decrypt(cred.SecretCiphertext)
	if err != nil {
		return ep // 解密失败不泄露细节，回退环境变量
	}
	var secret map[string]any
	if err := json.Unmarshal(raw, &secret); err != nil {
		return ep
	}
	if v, ok := secret["api_url"].(string); ok && v != "" {
		ep.APIURL = normalizeK8SURL(v)
	} else if v, ok := secret["server"].(string); ok && v != "" {
		ep.APIURL = normalizeK8SURL(v)
	}
	if v, ok := secret["token"].(string); ok && v != "" {
		ep.Token = v
	}
	return ep
}

// normalizeK8SURL 把 ":19009"、"localhost:19009" 等简写规范化为完整 URL。
func normalizeK8SURL(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, ":") {
		return "http://localhost" + v
	}
	if v != "" && !strings.Contains(v, "://") {
		return "http://" + v
	}
	return v
}

// listK8SPods 直查 apiserver Pod 列表（10s 超时，不落库）。
func (s *Server) listK8SPods(ctx context.Context, ep k8sEndpoint, namespace, selector string) ([]k8sPodView, error) {
	path := ep.APIURL + "/api/v1/pods"
	if namespace != "" {
		path = ep.APIURL + "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods"
	}
	if selector != "" {
		path += "?labelSelector=" + url.QueryEscape(selector)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if ep.Token != "" {
		req.Header.Set("Authorization", "Bearer "+ep.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 apiserver 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("apiserver 返回状态 %d", resp.StatusCode)
	}
	var list k8sPodList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("解析 apiserver 响应失败: %w", err)
	}
	now := time.Now()
	pods := make([]k8sPodView, 0, len(list.Items))
	for _, item := range list.Items {
		p := k8sPodView{
			Name:      item.Metadata.Name,
			Namespace: item.Metadata.Namespace,
			Phase:     item.Status.Phase,
			Node:      item.Spec.NodeName,
		}
		for _, cs := range item.Status.ContainerStatuses {
			p.RestartCount += cs.RestartCount
		}
		if !item.Metadata.CreationTimestamp.IsZero() {
			p.AgeSeconds = int64(now.Sub(item.Metadata.CreationTimestamp).Seconds())
			if p.AgeSeconds < 0 {
				p.AgeSeconds = 0
			}
		}
		pods = append(pods, p)
	}
	return pods, nil
}

// handleK8SPods 处理 GET /api/v1/k8s/pods?cluster=&namespace=&selector=：
// 代理 K8s apiserver Pod 实况（精简字段，不落库）。
// 当前仅支持单集群：cluster 参数与配置集群名（K8S_CLUSTER_NAME，默认 volc-prod-k8s）
// 不符时返回空列表。
func (s *Server) handleK8SPods(c *gin.Context) {
	clusterName := defaultStringEnv("K8S_CLUSTER_NAME", "volc-prod-k8s")
	if cluster := c.Query("cluster"); cluster != "" && cluster != clusterName {
		c.JSON(http.StatusOK, []k8sPodView{})
		return
	}
	ep := s.resolveK8SEndpoint()
	pods, err := s.listK8SPods(c.Request.Context(), ep, c.Query("namespace"), c.Query("selector"))
	if err != nil {
		respondError(c, http.StatusBadGateway, CodeUpstream, "查询 K8s apiserver 失败: "+err.Error(), nil)
		return
	}
	c.JSON(http.StatusOK, pods)
}
