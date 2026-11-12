package pipeline

import (
	"context"
	"math/rand"
	"strings"
	"time"

	"ciwang/translate/internal/provider"
)

// translateWithRetry runs the adapter with up to 3 total attempts. Deltas
// are forwarded from every attempt (the UI overwrites with the running
// result, so a retry just restarts the overwrite). "API 限流" errors
// (429 / rate-limit wording) are transient and retried with backoff; auth
// and other provider faults ("API 错误") are not.
//
// Port of translate_with_retry (translator.cpp:662).
func translateWithRetry(ctx context.Context, adapter provider.Translator, req provider.Request, onDelta func(string)) provider.Result {
	return translateWithRetryLog(ctx, adapter, req, onDelta, nil)
}

func translateWithRetryLog(ctx context.Context, adapter provider.Translator, req provider.Request, onDelta func(string), log *Logger) provider.Result {
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(400<<(attempt-1)) * time.Millisecond
			jitter := time.Duration(rand.Intn(250)) * time.Millisecond //nolint:gosec
			if log != nil {
				log.Warnf("第 %d 次尝试失败，%v 后重试", attempt, backoff+jitter)
			}
			select {
			case <-time.After(backoff + jitter):
			case <-ctx.Done():
				if log != nil {
					log.Errorf("已取消")
				}
				return provider.Result{Error: "已取消"}
			}
		}
		events, cancel := adapter.Translate(ctx, req)
		var r provider.Result
		for ev := range events {
			switch ev.Kind {
			case provider.EventDelta:
				if onDelta != nil {
					onDelta(ev.Text)
				}
			case provider.EventDone:
				r = provider.Result{OK: true, Text: ev.Text}
			case provider.EventError:
				r = provider.Result{Error: ev.Error}
			}
		}
		cancel()
		throttle := strings.Contains(r.Error, "API 限流")
		hardError := strings.Contains(r.Error, "API 错误")
		retryable := !r.OK && (throttle || !hardError) && attempt < maxAttempts-1
		if log != nil {
			if r.OK {
				log.Debugf("请求成功（第 %d 次尝试）", attempt+1)
			} else if retryable {
				log.Warnf("请求失败（第 %d 次尝试，可重试）: %s", attempt+1, r.Error)
			} else {
				log.Errorf("请求失败（第 %d 次尝试，不可重试）: %s", attempt+1, r.Error)
			}
		}
		if r.OK || !retryable {
			return r
		}
	}
	return provider.Result{Error: "未知错误"} // unreachable
}
