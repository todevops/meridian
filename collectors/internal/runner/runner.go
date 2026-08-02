// Package runner 编排采集器运行与发现记录上报。
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"

	"collectors/internal/record"
)

// Collector 是一个数据源采集器：拉取源端清单并映射为标准发现记录。
type Collector interface {
	Name() string
	Collect(ctx context.Context) ([]record.Record, error)
}

// Run 顺序运行采集器并上报记录。单个采集器失败不阻断其余采集器，
// 所有失败以聚合错误返回（供进程退出码使用）。
// 运行结束后向 out 打印一行 CMDB_PRODUCED=<成功上报总条数>（2A 任务调度器据此统计产出，
// dry-run 同样打印便于联调）；调用方传 os.Stdout 即满足"stdout 末行"约定。
func Run(ctx context.Context, cols []Collector, sink record.Sink, logf func(format string, args ...any), out io.Writer) error {
	var errs []error
	produced := 0
	for _, c := range cols {
		recs, err := c.Collect(ctx)
		if err != nil {
			logf("采集器 %s 采集失败: %v", c.Name(), err)
			errs = append(errs, fmt.Errorf("采集器 %s: %w", c.Name(), err))
			continue
		}
		logf("采集器 %s 产出 %d 条发现记录", c.Name(), len(recs))
		if len(recs) == 0 {
			continue
		}
		if err := sink.Submit(ctx, recs); err != nil {
			logf("采集器 %s 上报失败: %v", c.Name(), err)
			errs = append(errs, fmt.Errorf("采集器 %s 上报: %w", c.Name(), err))
			continue
		}
		produced += len(recs)
	}
	fmt.Fprintf(out, "CMDB_PRODUCED=%d\n", produced)
	return errors.Join(errs...)
}
