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
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(400<<(attempt-1)) * time.Millisecond
			jitter := time.Duration(rand.Intn(250)) * time.Millisecond //nolint:gosec
			select {
			case <-time.After(backoff + jitter):
			case <-ctx.Done():
				return provider.Result{Error: "已取消"}
			}
		}
		events, cancel := adapter.Translate(ctx, req)
		var r provider.Result
		var delivered bool
		for ev := range events {
			switch ev.Kind {
			case provider.EventDelta:
				if onDelta != nil {
					onDelta(ev.Text)
				}
			case provider.EventDone:
				r = provider.Result{OK: true, Text: ev.Text}
				delivered = true
			case provider.EventError:
				r = provider.Result{Error: ev.Error}
				delivered = true
			}
		}
		cancel()
		_ = delivered
		throttle := strings.Contains(r.Error, "API 限流")
		hardError := strings.Contains(r.Error, "API 错误")
		retryable := !r.OK && (throttle || !hardError) && attempt < maxAttempts-1
		if r.OK || !retryable {
			return r
		}
	}
	return provider.Result{Error: "未知错误"} // unreachable
}
