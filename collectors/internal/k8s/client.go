// Package k8s 实现 K8s 元数据采集器的 apiserver 客户端：
// 基于 client-go rest.RESTClient 直接 list 资源（自建轻量发现），
// 不引入 informer/discovery 整树依赖。连接支持两种凭据形态：
//   - kubeconfig 文件（K8S_KUBECONFIG）
//   - url+token 环境变量（K8S_API_URL/K8S_TOKEN/K8S_INSECURE，默认值指向 mock 平台 :19009）
package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// scheme 注册采集用到的三个 API 组，供 rest.RESTClient 解码 list 响应（官方 list 壳）。
var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(networkingv1.AddToScheme(scheme))
}

// Client 封装对三个 API 组的 list 调用。
type Client struct {
	core       *rest.RESTClient // /api/v1
	apps       *rest.RESTClient // /apis/apps/v1
	networking *rest.RESTClient // /apis/networking.k8s.io/v1
	base       *rest.RESTClient // /version 等无组路径
}

// NewClient 创建客户端。kubeconfig 非空时优先从 kubeconfig 构建；
// 否则用 apiURL+token（apiURL 支持 ":19009" 简写），insecure 控制 TLS 证书校验。
func NewClient(apiURL, token, kubeconfig string, insecure bool) (*Client, error) {
	var cfg *rest.Config
	var err error
	if kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("加载 kubeconfig %s 失败: %w", kubeconfig, err)
		}
	} else {
		cfg = &rest.Config{
			Host:            normalizeHost(apiURL),
			BearerToken:     token,
			TLSClientConfig: rest.TLSClientConfig{Insecure: insecure},
		}
	}
	cfg.Timeout = 30 * time.Second
	core, err := restClientFor(cfg, "/api", corev1.SchemeGroupVersion)
	if err != nil {
		return nil, err
	}
	apps, err := restClientFor(cfg, "/apis", appsv1.SchemeGroupVersion)
	if err != nil {
		return nil, err
	}
	networking, err := restClientFor(cfg, "/apis", networkingv1.SchemeGroupVersion)
	if err != nil {
		return nil, err
	}
	base, err := restClientFor(cfg, "", corev1.SchemeGroupVersion)
	if err != nil {
		return nil, err
	}
	return &Client{core: core, apps: apps, networking: networking, base: base}, nil
}

// restClientFor 按 API 路径与组版本创建 RESTClient。
func restClientFor(cfg *rest.Config, apiPath string, gv schema.GroupVersion) (*rest.RESTClient, error) {
	c := rest.CopyConfig(cfg)
	c.APIPath = apiPath
	c.GroupVersion = &gv
	c.NegotiatedSerializer = serializer.WithoutConversionCodecFactory{CodecFactory: serializer.NewCodecFactory(scheme)}
	rc, err := rest.RESTClientFor(c)
	if err != nil {
		return nil, fmt.Errorf("创建 %s RESTClient 失败: %w", gv.String(), err)
	}
	return rc, nil
}

// normalizeHost 把 ":19009"、"localhost:19009" 等简写规范化为完整 URL。
func normalizeHost(apiURL string) string {
	u := apiURL
	if len(u) > 0 && u[0] == ':' {
		u = "http://localhost" + u
	}
	return u
}

// Version 拉取 /version（普通 JSON，非 runtime.Object，走 DoRaw 解码）。
func (c *Client) Version(ctx context.Context) (version.Info, error) {
	var info version.Info
	data, err := c.base.Get().AbsPath("/version").DoRaw(ctx)
	if err != nil {
		return info, fmt.Errorf("拉取 /version 失败: %w", err)
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, fmt.Errorf("解析 /version 响应失败: %w", err)
	}
	return info, nil
}

// list 泛型 list 辅助：空 namespace 即集群范围全量列出。
func list[T runtime.Object](ctx context.Context, rc *rest.RESTClient, resource string, out T) error {
	if err := rc.Get().Resource(resource).Do(ctx).Into(out); err != nil {
		return fmt.Errorf("list %s 失败: %w", resource, err)
	}
	return nil
}

func (c *Client) ListNodes(ctx context.Context) (*corev1.NodeList, error) {
	out := &corev1.NodeList{}
	return out, list(ctx, c.core, "nodes", out)
}

func (c *Client) ListNamespaces(ctx context.Context) (*corev1.NamespaceList, error) {
	out := &corev1.NamespaceList{}
	return out, list(ctx, c.core, "namespaces", out)
}

func (c *Client) ListServices(ctx context.Context) (*corev1.ServiceList, error) {
	out := &corev1.ServiceList{}
	return out, list(ctx, c.core, "services", out)
}

func (c *Client) ListDeployments(ctx context.Context) (*appsv1.DeploymentList, error) {
	out := &appsv1.DeploymentList{}
	return out, list(ctx, c.apps, "deployments", out)
}

func (c *Client) ListStatefulSets(ctx context.Context) (*appsv1.StatefulSetList, error) {
	out := &appsv1.StatefulSetList{}
	return out, list(ctx, c.apps, "statefulsets", out)
}

func (c *Client) ListDaemonSets(ctx context.Context) (*appsv1.DaemonSetList, error) {
	out := &appsv1.DaemonSetList{}
	return out, list(ctx, c.apps, "daemonsets", out)
}

func (c *Client) ListIngresses(ctx context.Context) (*networkingv1.IngressList, error) {
	out := &networkingv1.IngressList{}
	return out, list(ctx, c.networking, "ingresses", out)
}
