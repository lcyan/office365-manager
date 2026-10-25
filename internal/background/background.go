// Package background 在独立 goroutine 中执行带重试的后台任务，
// 并把每次尝试与最终结果写入结构化日志（不再静默放弃）。
package background

import (
	"fmt"
	"log/slog"
	"time"
)

// Runner 管理后台任务。
type Runner struct {
	log *slog.Logger
}

// New 构造 Runner。
func New(log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{log: log}
}

// Retry 启动一个后台重试任务：最多 attempts 次、每次间隔 interval。
// fn 返回 nil 表示成功并停止重试。最终失败记录为 WARN 级别日志
// （任务名与尝试次数），成功记录 INFO。
func (r *Runner) Retry(task string, attempts int, interval time.Duration, fn func() error) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				r.log.Error("后台任务 panic", "task", task, "panic", fmt.Sprint(rec))
			}
		}()
		var lastErr error
		for attempt := 1; attempt <= attempts; attempt++ {
			lastErr = fn()
			if lastErr == nil {
				r.log.Info("后台任务成功", "task", task, "attempt", attempt)
				return
			}
			r.log.Warn("后台任务尝试失败", "task", task, "attempt", attempt, "err", lastErr)
			if attempt < attempts {
				time.Sleep(interval)
			}
		}
		r.log.Warn("后台任务最终失败", "task", task, "attempts", attempts, "err", lastErr)
	}()
}
