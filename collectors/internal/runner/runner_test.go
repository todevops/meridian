package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"collectors/internal/record"
)

// fakeCollector 是测试用采集器。
type fakeCollector struct {
	name string
	recs []record.Record
	err  error
}

func (f fakeCollector) Name() string { return f.name }
func (f fakeCollector) Collect(context.Context) ([]record.Record, error) {
	return f.recs, f.err
}

// fakeSink 记录收到的批次。
type fakeSink struct {
	batches [][]record.Record
	err     error
}

func (s *fakeSink) Submit(_ context.Context, recs []record.Record) error {
	if s.err != nil {
		return s.err
	}
	s.batches = append(s.batches, recs)
	return nil
}

func sampleRecord() record.Record {
	return record.Record{
		Source:         "test",
		Collector:      "test",
		ModelCandidate: "host",
		Attributes:     map[string]any{"ip": "10.0.0.1"},
	}
}

func TestRunSuccess(t *testing.T) {
	sink := &fakeSink{}
	cols := []Collector{
		fakeCollector{name: "a", recs: []record.Record{sampleRecord()}},
		fakeCollector{name: "b", recs: nil}, // 空批次不上报
	}
	logs := []string{}
	var out strings.Builder
	err := Run(context.Background(), cols, sink, func(f string, args ...any) {
		logs = append(logs, f)
	}, &out)
	if err != nil {
		t.Fatalf("Run 应成功: %v", err)
	}
	if len(sink.batches) != 1 {
		t.Fatalf("应只上报 1 批（空批次跳过）: %d", len(sink.batches))
	}
	if out.String() != "CMDB_PRODUCED=1\n" {
		t.Fatalf("应打印 CMDB_PRODUCED=1: %q", out.String())
	}
}

func TestRunCollectErrorDoesNotBlockOthers(t *testing.T) {
	sink := &fakeSink{}
	cols := []Collector{
		fakeCollector{name: "bad", err: errors.New("数据源不可用")},
		fakeCollector{name: "good", recs: []record.Record{sampleRecord()}},
	}
	var out strings.Builder
	err := Run(context.Background(), cols, sink, func(string, ...any) {}, &out)
	if err == nil {
		t.Fatal("存在失败采集器应返回聚合错误")
	}
	if !strings.Contains(err.Error(), "bad") || !strings.Contains(err.Error(), "数据源不可用") {
		t.Fatalf("聚合错误应包含失败采集器信息: %v", err)
	}
	if len(sink.batches) != 1 {
		t.Fatal("失败采集器不应阻断后续采集器上报")
	}
	// 失败采集器不计入产出，但产出声明照常打印（末行约定）
	if out.String() != "CMDB_PRODUCED=1\n" {
		t.Fatalf("应打印 CMDB_PRODUCED=1: %q", out.String())
	}
}

func TestRunSinkError(t *testing.T) {
	sink := &fakeSink{err: errors.New("CMDB 不可达")}
	cols := []Collector{fakeCollector{name: "a", recs: []record.Record{sampleRecord()}}}
	var out strings.Builder
	err := Run(context.Background(), cols, sink, func(string, ...any) {}, &out)
	if err == nil || !strings.Contains(err.Error(), "CMDB 不可达") {
		t.Fatalf("上报失败应聚合返回: %v", err)
	}
	if out.String() != "CMDB_PRODUCED=0\n" {
		t.Fatalf("上报失败应声明产出 0: %q", out.String())
	}
}
