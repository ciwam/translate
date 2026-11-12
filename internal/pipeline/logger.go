package pipeline

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Logger collects a structured trace of one translation run: routing
// decisions, group requests/replies, per-line validation verdicts, rescue
// retries and the final assembly. It is safe for concurrent use (splice
// group workers log in parallel). The UI shows the log of the latest run in
// a dedicated tab; the debug CLI prints it to stderr with --verbose.
type Logger struct {
	mu      sync.Mutex
	entries []Entry
	start   time.Time
}

// Entry is one log line.
type Entry struct {
	Time  time.Time
	Level string // info / warn / error / debug
	Msg   string
}

// NewLogger starts a run log, timestamps relative to now.
func NewLogger() *Logger {
	return &Logger{start: time.Now()}
}

func (l *Logger) logf(level, format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, Entry{
		Time:  time.Now(),
		Level: level,
		Msg:   strings.TrimRight(fmt.Sprintf(format, args...), "\n"),
	})
}

// Infof appends an informational line.
func (l *Logger) Infof(format string, args ...any) { l.logf("info", format, args...) }

// Warnf appends a warning line.
func (l *Logger) Warnf(format string, args ...any) { l.logf("warn", format, args...) }

// Errorf appends an error line.
func (l *Logger) Errorf(format string, args ...any) { l.logf("error", format, args...) }

// Debugf appends a detail line (request/reply dumps).
func (l *Logger) Debugf(format string, args ...any) { l.logf("debug", format, args...) }

// Entries returns a snapshot of the log.
func (l *Logger) Entries() []Entry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, len(l.entries))
	copy(out, l.entries)
	return out
}

// Render formats the whole log as human-readable text, timestamps relative
// to the run start ([0.000s] style).
func (l *Logger) Render() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	for _, e := range l.entries {
		ts := e.Time.Sub(l.start).Seconds()
		switch e.Level {
		case "warn":
			b.WriteString(fmt.Sprintf("[%6.3fs] ⚠ %s\n", ts, e.Msg))
		case "error":
			b.WriteString(fmt.Sprintf("[%6.3fs] ✗ %s\n", ts, e.Msg))
		case "debug":
			b.WriteString(fmt.Sprintf("[%6.3fs]   %s\n", ts, e.Msg))
		default:
			b.WriteString(fmt.Sprintf("[%6.3fs] · %s\n", ts, e.Msg))
		}
	}
	return b.String()
}
