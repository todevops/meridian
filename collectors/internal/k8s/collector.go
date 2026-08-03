// k8s 元数据采集器：list 集群/命名空间/工作负载/Service/Ingress 与节点，
// 映射为标准发现记录。Node 映射为 host 记录（host_type=k8s_node），
// 与既有主机 CI 经调和键（ident/内网 IP）合并，不产生重复建档。
// 本采集器为周期任务型：单次运行即一轮全量 list 上报（增量/对账由调度侧周期保证）。
package k8s

import (
	"context"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"collectors/internal/record"
)

const (
	// Source 是发现来源系统标识。
	Source = "kubernetes"
	// CollectorName 是采集器标识。
	CollectorName = "k8s-metadata-collector"
)

// nodeLabelWhitelist 是节点标签白名单（环境/业务/owner）。
var nodeLabelWhitelist = []string{"env", "environment", "biz", "business", "owner"}

// workloadLabelWhitelist 是工作负载标签白名单（app/env）。
var workloadLabelWhitelist = []string{"app", "env", "environment"}

// Collector 是 K8s 元数据采集器。
type Collector struct {
	client      *Client
	clusterName string
	now         func() time.Time
}

// New 创建采集器。apiURL 支持 ":19009" 简写；kubeconfig 非空时优先于 url+token。
func New(apiURL, token, clusterName, kubeconfig string, insecure bool) (*Collector, error) {
	cli, err := NewClient(apiURL, token, kubeconfig, insecure)
	if err != nil {
		return nil, err
	}
	if clusterName == "" {
		clusterName = "volc-prod-k8s"
	}
	return &Collector{client: cli, clusterName: clusterName, now: time.Now}, nil
}

// Name 返回采集器名。
func (c *Collector) Name() string { return "k8s" }

// Collect 全量 list 一轮并映射为发现记录。
func (c *Collector) Collect(ctx context.Context) ([]record.Record, error) {
	nodes, err := c.client.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	info, err := c.client.Version(ctx)
	if err != nil {
		return nil, err
	}
	namespaces, err := c.client.ListNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	deploys, err := c.client.ListDeployments(ctx)
	if err != nil {
		return nil, err
	}
	stss, err := c.client.ListStatefulSets(ctx)
	if err != nil {
		return nil, err
	}
	dss, err := c.client.ListDaemonSets(ctx)
	if err != nil {
		return nil, err
	}
	svcs, err := c.client.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	ings, err := c.client.ListIngresses(ctx)
	if err != nil {
		return nil, err
	}

	now := c.now()
	recs := make([]record.Record, 0, 1+len(nodes.Items)+len(namespaces.Items))
	recs = append(recs, mapCluster(c.clusterName, info.GitVersion, len(nodes.Items), now))
	for i := range nodes.Items {
		recs = append(recs, mapNode(c.clusterName, &nodes.Items[i], now))
	}
	for i := range namespaces.Items {
		recs = append(recs, mapNamespace(c.clusterName, namespaces.Items[i].Name, now))
	}
	for i := range deploys.Items {
		recs = append(recs, mapWorkload(c.clusterName, "Deployment", deploys.Items[i].Namespace,
			deploys.Items[i].Name, replicasOrOne(deploys.Items[i].Spec.Replicas),
			firstImage(deploys.Items[i].Spec.Template.Spec.Containers), deploys.Items[i].Labels, now))
	}
	for i := range stss.Items {
		recs = append(recs, mapWorkload(c.clusterName, "StatefulSet", stss.Items[i].Namespace,
			stss.Items[i].Name, replicasOrOne(stss.Items[i].Spec.Replicas),
			firstImage(stss.Items[i].Spec.Template.Spec.Containers), stss.Items[i].Labels, now))
	}
	for i := range dss.Items {
		recs = append(recs, mapWorkload(c.clusterName, "DaemonSet", dss.Items[i].Namespace,
			dss.Items[i].Name, dss.Items[i].Status.DesiredNumberScheduled,
			firstImage(dss.Items[i].Spec.Template.Spec.Containers), dss.Items[i].Labels, now))
	}
	for i := range svcs.Items {
		recs = append(recs, mapService(c.clusterName, &svcs.Items[i], now))
	}
	for i := range ings.Items {
		recs = append(recs, mapIngress(c.clusterName, &ings.Items[i], now))
	}
	return recs, nil
}

// newRecord 构造统一发现记录。
func newRecord(modelCandidate string, attrs map[string]any, now time.Time) record.Record {
	return record.Record{
		Source:         Source,
		Collector:      CollectorName,
		ModelCandidate: modelCandidate,
		Attributes:     attrs,
		OccurredAt:     now,
	}
}

// mapCluster 映射 k8s_cluster 记录（name=集群名、version、node_count）。
func mapCluster(clusterName, gitVersion string, nodeCount int, now time.Time) record.Record {
	return newRecord("k8s_cluster", map[string]any{
		"name":       clusterName,
		"version":    gitVersion,
		"node_count": nodeCount,
	}, now)
}

// mapNode 映射节点为 host 记录（host_type=k8s_node、ident=node 名、ip=InternalIP）。
// 关电/NotReady 节点容错：仍建档并以 ready=false 标注，不中断采集。
func mapNode(clusterName string, n *corev1.Node, now time.Time) record.Record {
	ip := ""
	for _, addr := range n.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP && addr.Address != "" {
			ip = addr.Address
			break
		}
	}
	if ip == "" {
		for _, addr := range n.Status.Addresses {
			if addr.Type == corev1.NodeExternalIP && addr.Address != "" {
				ip = addr.Address
				break
			}
		}
	}
	ready := false
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			ready = cond.Status == corev1.ConditionTrue
			break
		}
	}
	return newRecord("host", map[string]any{
		"ident":     n.Name,
		"ip":        ip,
		"host_type": "k8s_node",
		"cluster":   clusterName,
		"ready":     ready,
		"labels":    pickLabels(n.Labels, nodeLabelWhitelist),
		"source":    Source,
	}, now)
}

// mapNamespace 映射 k8s_namespace 记录。
func mapNamespace(clusterName, name string, now time.Time) record.Record {
	return newRecord("k8s_namespace", map[string]any{
		"cluster": clusterName,
		"name":    name,
		// uid 为调和主键：cluster/namespace/name 任一单字段都不具标识性（cluster 全集群共享），组合方可唯一
		"uid": clusterName + "/" + name,
	}, now)
}

// mapWorkload 映射 k8s_workload 记录（Deployment/StatefulSet/DaemonSet）。
func mapWorkload(clusterName, kind, namespace, name string, replicas int32, image string, labels map[string]string, now time.Time) record.Record {
	return newRecord("k8s_workload", map[string]any{
		"cluster":   clusterName,
		"namespace": namespace,
		"kind":      kind,
		"name":      name,
		"replicas":  replicas,
		"image":     image,
		"labels":    pickLabels(labels, workloadLabelWhitelist),
		// uid 为调和主键：cluster+namespace+kind+name 四元组合
		"uid": clusterName + "/" + namespace + "/" + kind + "/" + name,
	}, now)
}

// mapService 映射 Service 为 k8s_service 记录（selector）。
func mapService(clusterName string, svc *corev1.Service, now time.Time) record.Record {
	return newRecord("k8s_service", map[string]any{
		"cluster":   clusterName,
		"namespace": svc.Namespace,
		"kind":      "service",
		"uid":       clusterName + "/" + svc.Namespace + "/service/" + svc.Name,
		"name":      svc.Name,
		"selector":  record.FormatTags(svc.Spec.Selector),
	}, now)
}

// mapIngress 映射 Ingress 为 k8s_service 记录（host 为规则域名逗号拼接）。
func mapIngress(clusterName string, ing *networkingv1.Ingress, now time.Time) record.Record {
	hosts := make([]string, 0, len(ing.Spec.Rules))
	for _, rule := range ing.Spec.Rules {
		if rule.Host != "" {
			hosts = append(hosts, rule.Host)
		}
	}
	return newRecord("k8s_service", map[string]any{
		"cluster":   clusterName,
		"namespace": ing.Namespace,
		"kind":      "ingress",
		"uid":       clusterName + "/" + ing.Namespace + "/ingress/" + ing.Name,
		"name":      ing.Name,
		"host":      strings.Join(hosts, ","),
	}, now)
}

// replicasOrOne 处理 spec.replicas 缺省（K8s 语义缺省为 1）。
func replicasOrOne(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

// firstImage 取 Pod 模板首个容器镜像。
func firstImage(containers []corev1.Container) string {
	if len(containers) == 0 {
		return ""
	}
	return containers[0].Image
}

// pickLabels 按白名单键筛选标签。
func pickLabels(labels map[string]string, whitelist []string) map[string]string {
	out := map[string]string{}
	for _, k := range whitelist {
		if v, ok := labels[k]; ok && v != "" {
			out[k] = v
		}
	}
	return out
}
