// pipeline 模式：五类实体（sites/racks/devices/vlans/virtual-machines）翻译为
// 标准 DiscoveryRecord，经分批 + 令牌桶限速 + 429/5xx 指数退避重试，
// 批量 POST /api/v1/discovery-records 上报摄入管道。
// IPAM（prefixes/ip-addresses）无模型管道，仍走 direct（见 migrate.go RunWithOptions 注释）。
package migrate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"migrator/internal/cmdb"
)

// CollectorID 为管道模式上报记录的采集器标识。
const CollectorID = "netbox-migrator"

// entityFetch 拉取一类 NetBox 实体并翻译为标准发现记录。
type entityFetch struct {
	key   string                         // 实体类别键（报告用）
	label string                         // 中文说明
	fetch func(ctx context.Context) ([]cmdb.DiscoveryRecord, []fetchFailure, error)
}

// fetchFailure 为翻译阶段被跳过的记录（坏数据，不参与上报）。
type fetchFailure struct {
	netboxID string
	name     string
	err      error
}

// toRecord 包装属性为标准发现记录。
func toRecord(modelCandidate string, attrs map[string]any, occurredAt time.Time) cmdb.DiscoveryRecord {
	return cmdb.DiscoveryRecord{
		Source:         cmdb.MigrationSource,
		Collector:      CollectorID,
		ModelCandidate: modelCandidate,
		Attributes:     attrs,
		OccurredAt:     occurredAt,
	}
}

// runPipeline 执行管道模式实体迁移：逐类拉取翻译 → 分批限速上报。
// 某类实体拉取失败记入报告后继续后续实体；批次最终失败按整批失败计数后继续下一批。
func (m *Migrator) runPipeline(ctx context.Context, report *Report, opts Options) {
	report.Pipeline = &PipelineReport{}
	uploader := &batchUploader{
		client:  m.cmdb,
		limiter: newTokenBucket(opts.Rate),
		backoff: newBackoffPolicy(opts.MaxRetry),
		stats:   report.Pipeline,
	}

	fetches := []entityFetch{
		{"sites", "站点→机房", m.fetchSiteRecords},
		{"racks", "机架→机柜", m.fetchRackRecords},
		{"devices", "设备→网络设备", m.fetchDeviceRecords},
		{"vlans", "VLAN→VLAN CI", m.fetchVLANRecords},
		{"virtual_machines", "虚拟机→VM CI", m.fetchVMRecords},
	}
	for _, f := range fetches {
		if ctx.Err() != nil {
			return // 取消传播：不再拉取后续实体
		}
		ent := report.entity(f.key, f.label)
		records, failures, err := f.fetch(ctx)
		if err != nil {
			ent.recordFetchError(err)
			continue
		}
		ent.Fetched = len(records) + len(failures)
		for _, fl := range failures {
			ent.recordFailure(fl.netboxID, fl.name, fl.err)
		}
		uploader.upload(ctx, ent, records, opts.BatchSize)
	}
}

// fetchSiteRecords 拉取站点并翻译为 room 记录。
func (m *Migrator) fetchSiteRecords(ctx context.Context) ([]cmdb.DiscoveryRecord, []fetchFailure, error) {
	sites, err := m.nb.ListSites(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	records := make([]cmdb.DiscoveryRecord, 0, len(sites))
	for _, s := range sites {
		_, attrs := siteAttrs(s)
		records = append(records, toRecord("room", attrs, now))
	}
	return records, nil, nil
}

// fetchRackRecords 拉取机架并翻译为 rack 记录。
func (m *Migrator) fetchRackRecords(ctx context.Context) ([]cmdb.DiscoveryRecord, []fetchFailure, error) {
	racks, err := m.nb.ListRacks(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	records := make([]cmdb.DiscoveryRecord, 0, len(racks))
	for _, r := range racks {
		_, attrs := rackAttrs(r)
		records = append(records, toRecord("rack", attrs, now))
	}
	return records, nil, nil
}

// fetchDeviceRecords 拉取设备并翻译为 network_device 记录。
func (m *Migrator) fetchDeviceRecords(ctx context.Context) ([]cmdb.DiscoveryRecord, []fetchFailure, error) {
	devices, err := m.nb.ListDevices(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	records := make([]cmdb.DiscoveryRecord, 0, len(devices))
	for _, d := range devices {
		_, attrs := deviceAttrs(d)
		records = append(records, toRecord("network_device", attrs, now))
	}
	return records, nil, nil
}

// fetchVLANRecords 拉取 VLAN 并翻译为 vlan 记录。
func (m *Migrator) fetchVLANRecords(ctx context.Context) ([]cmdb.DiscoveryRecord, []fetchFailure, error) {
	vlans, err := m.nb.ListVLANs(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	records := make([]cmdb.DiscoveryRecord, 0, len(vlans))
	for _, v := range vlans {
		_, attrs := vlanAttrs(v)
		records = append(records, toRecord("vlan", attrs, now))
	}
	return records, nil, nil
}

// fetchVMRecords 拉取虚拟机并翻译为 virtual_machine 记录。
func (m *Migrator) fetchVMRecords(ctx context.Context) ([]cmdb.DiscoveryRecord, []fetchFailure, error) {
	vms, err := m.nb.ListVirtualMachines(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	records := make([]cmdb.DiscoveryRecord, 0, len(vms))
	for _, v := range vms {
		_, attrs := vmAttrs(v)
		records = append(records, toRecord("virtual_machine", attrs, now))
	}
	return records, nil, nil
}

// traceID 取记录的 netbox_id 留痕（失败回查用），缺失时为 "-"。
func traceID(rec cmdb.DiscoveryRecord) string {
	if id, ok := rec.Attributes["netbox_id"].(string); ok && id != "" {
		return id
	}
	return "-"
}

// recordLabel 取记录的展示名（name 属性优先），缺失时为模型候选名。
func recordLabel(rec cmdb.DiscoveryRecord) string {
	if name, ok := rec.Attributes["name"].(string); ok && name != "" {
		return name
	}
	return rec.ModelCandidate
}

// batchUploader 负责分批、限速与重试上报，并累计管道统计。
type batchUploader struct {
	client  *cmdb.Client
	limiter *tokenBucket
	backoff *backoffPolicy
	stats   *PipelineReport
}

// upload 把 records 按 batchSize 分批上报；限速按条计费，429/5xx 指数退避重试。
// 成功/失败计数写入 ent（accepted→成功，rejected 与最终失败批次→失败）。
func (u *batchUploader) upload(ctx context.Context, ent *EntityReport, records []cmdb.DiscoveryRecord, batchSize int) {
	for start := 0; start < len(records); start += batchSize {
		end := start + batchSize
		if end > len(records) {
			end = len(records)
		}
		batch := records[start:end]
		u.stats.Batches++
		u.stats.Records += len(batch)

		if err := u.limiter.Wait(ctx, len(batch)); err != nil {
			// 取消传播：本批及后续记录按未上报失败计数（不重试）。
			u.stats.Rejected += len(batch)
			for _, rec := range batch {
				ent.recordFailure(traceID(rec), recordLabel(rec), err)
			}
			return
		}

		result, err := u.sendWithRetry(ctx, batch)
		if err != nil {
			if ctx.Err() != nil {
				return // 取消：剩余批次不再上报
			}
			// 重试耗尽仍失败：整批逐条记失败，继续下一批。
			u.stats.Rejected += len(batch)
			for _, rec := range batch {
				ent.recordFailure(traceID(rec), recordLabel(rec), err)
			}
			continue
		}
		u.stats.Accepted += result.Accepted
		u.stats.Rejected += result.Rejected
		for i := 0; i < result.Accepted; i++ {
			ent.recordSuccess()
		}
		for _, re := range result.Errors {
			nbID, name := "-", fmt.Sprintf("记录 #%d", re.Index)
			if re.Index >= 0 && re.Index < len(batch) {
				nbID = traceID(batch[re.Index])
				name = recordLabel(batch[re.Index])
			}
			ent.recordFailure(nbID, name, errors.New(re.Message))
		}
	}
}

// sendWithRetry 上报一批记录；429/5xx 按指数退避重试，其余错误直接返回。
func (u *batchUploader) sendWithRetry(ctx context.Context, batch []cmdb.DiscoveryRecord) (cmdb.IngestResult, error) {
	var lastErr error
	for attempt := 0; attempt <= u.backoff.maxRetry; attempt++ {
		if attempt > 0 {
			u.stats.Backoffs++
			u.stats.RetriedRecords += len(batch)
			if err := u.backoff.sleep(ctx, u.backoff.delay(attempt)); err != nil {
				return cmdb.IngestResult{}, err
			}
		}
		result, err := u.client.IngestRecords(ctx, batch)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return cmdb.IngestResult{}, err
		}
	}
	return cmdb.IngestResult{}, fmt.Errorf("重试 %d 次后仍失败: %w", u.backoff.maxRetry, lastErr)
}

// isRetryable 判定错误是否可重试（HTTP 429 或 5xx）。
func isRetryable(err error) bool {
	if cmdb.IsStatus(err, http.StatusTooManyRequests) {
		return true
	}
	for status := http.StatusInternalServerError; status < 600; status++ {
		if cmdb.IsStatus(err, status) {
			return true
		}
	}
	return false
}
