// Translate — a mygo Native UI rewrite of the macOS translation app.
// The window mirrors the AppKit original: source input with 0.8s
// auto-translate debounce, streaming result, target-language picker,
// settings modal (per-provider keys in the Keychain, glossary), copy.
package main

import (
	"log"

	"ciwang/translate/internal/pipeline"
	"ciwang/translate/internal/settings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	if err := pipeline.DefaultInit(); err != nil {
		log.Printf("繁简转换初始化失败：%v", err)
	}
	store, err := settings.Load()
	if err != nil {
		log.Fatalf("设置目录创建失败：%v", err)
	}
	app := newApp(store)

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     "Translate",
			Width:     windowWidth,
			Height:    windowHeight,
			MinWidth:  560,
			MinHeight: 380,
			StateKey:  "main",
			Content:   ui.View(app.view),
		})
		app.win = win
		// First launch with no key: open the settings panel, as the AppKit
		// app does.
		if store.APIKey(app.providerIdx) == "" {
			app.openSettings()
			win.Invalidate()
		}

		// Memory hygiene: when the window is minimized, the UI state is idle
		// and the caches are stale — drop what we can and hand the memory
		// back to the OS (debug.FreeOSMemory runs a full GC and returns
		// scavengeable spans). Restoring just redraws; no state rebuild is
		// needed because nothing durable is dropped (the LRU result cache
		// is reproducible by re-translation).
		win.OnMinimize(func() {
			app.releaseMemory()
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
