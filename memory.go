package main

import (
	"log"
	"runtime"
	"runtime/debug"

	"ciwang/translate/internal/pipeline"
)

// releaseMemory drops what is reproducible and hands the freed heap back to
// the OS. Called when the window is minimized (and could be re-triggered on
// long idle): the translation LRU cache is the largest discretionary holder
// (40+ entries of source+translation text), and a full GC + scavenge
// returns the freed spans so the resident footprint actually shrinks —
// a plain GC alone keeps them for reuse. Restoring needs no rebuild: the
// UI state (source, output, settings) is untouched.
func (a *app) releaseMemory() {
	pipeline.ClearCache()
	runtime.GC()
	debug.FreeOSMemory()
	log.Printf("窗口已最小化：已清理翻译缓存并归还内存")
}
