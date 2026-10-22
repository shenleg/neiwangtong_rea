// 本文件按间隔执行只读扫描，保留每次评估与相邻比较，不执行客户端状态切换。
package windowprobe

import (
	"context"
	"fmt"
	"time"
)

// Observe 在 ctx 有效期间采集 count 次快照，interval 是上次完成到下次开始的等待时间。
// label 由操作者声明当前状态，不是自动检测的客户端状态；count 范围 1–1000，多次采集间隔至少 1ms。
// 参数错误、取消或 Scan 失败时返回已完成的记录及错误；中途出现歧义不重试、不丢弃失败样本。
func Observe(ctx context.Context, count int, interval time.Duration, label string) (Series, error) {
	return observe(ctx, count, interval, label, Scan)
}

func observe(ctx context.Context, count int, interval time.Duration, label string, scan func() (Report, error)) (Series, error) {
	s := Series{Label: label, Scans: []Report{}, Comparisons: []Comparison{}, Stability: "insufficient_samples"}
	if count < 1 || count > 1000 || interval < 0 || (count > 1 && interval < time.Millisecond) {
		return s, fmt.Errorf("count 必须为 1–1000；interval 非负，多次扫描至少 1ms")
	}
	for i := 0; i < count; i++ {
		if i > 0 {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return s, ctx.Err()
			case <-timer.C:
			}
		}
		if err := ctx.Err(); err != nil {
			return s, err
		}
		r, err := scan()
		if err != nil {
			return s, fmt.Errorf("扫描 %d: %w", i+1, err)
		}
		r.Label = label
		s.Scans = append(s.Scans, r)
		if i > 0 {
			c := Compare(s.Scans[i-1], r)
			s.Comparisons = append(s.Comparisons, c)
			if s.Stability == "insufficient_samples" {
				s.Stability = "stable_unique_unverified"
			}
			if !c.Stable {
				s.Stability = "not_stable"
			}
		}
	}
	return s, nil
}
