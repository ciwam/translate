package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"ciwang/translate/internal/pipeline"
	"ciwang/translate/internal/provider"
	"ciwang/translate/internal/settings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	autoTranslateDelay = 800 * time.Millisecond
	windowWidth        = 800
	windowHeight       = 540
)

// app is the state the window shows.
type app struct {
	mu sync.Mutex

	// settings
	store        *settings.Store
	providerIdx  int
	targetIdx    int
	glossaryText string

	// translation state
	source      string
	output      string
	status      string
	phase       int // 0 idle, 1 translating
	generation  uint64
	translating uint64 // generation of the in-flight request

	// debounce bookkeeping (view-driven; see view)
	lastEditAt time.Time
	editArmed  bool

	// settings dialog
	showSettings bool

	// panel edit-in-progress keys, keyed by provider index. Switching the
	// model moves the field's text to the provider it belongs to.
	panelKeys       map[int]string
	panelActiveProv int

	// result scroll follows the stream only while the user is at the bottom.
	resultScroll ui.ScrollState
	logScroll    ui.ScrollState

	// tab: 0 = 翻译, 1 = 日志
	tab int
	// log holds the latest run's trace.
	log *pipeline.Logger

	win *mygo.Window
}

func newApp(store *settings.Store) *app {
	d := store.Load()
	return &app{
		store:        store,
		providerIdx:  d.ProviderIndex,
		targetIdx:    d.TargetIndex,
		glossaryText: d.Glossary,
	}
}

// translateFromView starts a translation (called on the main thread). It
// snapshots the current settings, bumps the generation, and runs the kernel
// on a goroutine.
func (a *app) translateFromView() {
	src := strings.TrimSpace(a.source)
	if src == "" {
		a.output = ""
		a.status = ""
		return
	}
	a.generation++
	gen := a.generation
	a.translating = gen
	a.phase = 1
	a.status = "翻译中…"
	a.output = ""
	a.log = pipeline.NewLogger()

	pIdx := a.providerIdx
	tIdx := a.targetIdx
	key := a.store.APIKey(pIdx)
	glossary := a.glossaryText
	win := a.win

	go func() {
		adapter := provider.New(provider.Provider(pIdx))
		var route string
		res := pipeline.Translate(context.Background(), pipeline.Options{
			Provider: pIdx,
			Target:   tIdx,
			APIKey:   key,
			Glossary: glossary,
			Log:      a.log,
		}, src, adapter, func(delta string) {
			mygo.RunOnMain(func() {
				if a.generation != gen {
					return // stale deltas from a superseded request
				}
				a.output = delta
				// Follow the stream only while the user is already at the
				// bottom; don't yank the view back down on every delta.
				if a.resultScroll.Y >= a.resultScroll.MaxY {
					a.resultScroll.Y = maxFloat32
				}
				win.Invalidate()
			})
		}, &route)
		mygo.RunOnMain(func() {
			if a.generation != gen {
				return
			}
			a.phase = 0
			if res.OK {
				a.output = res.Text
				a.status = "完成 · " + route
			} else {
				a.status = res.Error
			}
			win.Invalidate()
		})
	}()
}

const maxFloat32 = float32(3.4028235e38)

func (a *app) view(c *ui.Context) {
	t := c.Theme()

	// Root: padded column filling the window.
	ui.Column(c).Fill().Padding(12).Gap(8).Children(func() {
		// Tab bar: 翻译 / 日志.
		ui.Row(c).Gap(4).Children(func() {
			if ui.Button(c, "翻译").Clicked() {
				a.tab = 0
			}
			if ui.Button(c, "日志").Clicked() {
				a.tab = 1
			}
			ui.Text(c, "").Grow(1)
		})

		if a.tab == 1 {
			a.logView(c)
		} else {
			a.mainContent(c, t)
		}
	})

	a.settingsView(c)
}

func (a *app) mainContent(c *ui.Context, t *ui.Theme) {

	// Debounce: schedule a frame for when auto-translate should fire, then
	// start the translation on that frame. Arms on source edits; a new edit
	// before the delay re-arms.
	if ui.TextArea(c, &a.source).Placeholder("输入要翻译的文本…").Height(100).Changed() {
		a.lastEditAt = time.Now()
		a.editArmed = true
		c.After(autoTranslateDelay)
	}
	if a.editArmed && time.Since(a.lastEditAt) >= autoTranslateDelay {
		a.editArmed = false
		if a.phase == 0 {
			a.translateFromView()
		}
	}

	// Status line.
	statusText := a.status
	if statusText != "" {
		ui.Text(c, statusText).FontSize(11).TextColor(t.TextMuted)
	} else {
		ui.Text(c, "").FontSize(11)
	}

	// Result area (grows, streams, follows the bottom).
	ui.Scroll(c).TrackScroll(&a.resultScroll).Grow(1).Children(func() {
		ui.Text(c, a.output)
	})

	// Bottom bar: target language, settings, copy, quit. The language list
	// leads with 中英互译 (auto direction by input language, stored as
	// target index -1), followed by the fixed targets.
	ui.Row(c).Gap(8).Children(func() {
		langs := make([]string, 0, provider.TargetCount()+1)
		langs = append(langs, autoTargetName)
		for i := 0; i < provider.TargetCount(); i++ {
			langs = append(langs, provider.TargetDisplayName(i))
		}
		choice := targetDisplayName(a.targetIdx)
		ui.Text(c, "目标语言").FontSize(12)
		sel := ui.Select(c, &choice, langs)
		if sel.Changed() {
			a.targetIdx = targetIndexForName(choice)
			a.saveTarget()
			// Retranslate with the new language.
			if a.phase == 0 && strings.TrimSpace(a.source) != "" {
				a.translateFromView()
			}
		}
		if ui.Button(c, "设置").Clicked() {
			a.openSettings()
		}
		if ui.Button(c, "复制结果").Clicked() && a.output != "" {
			c.WriteClipboard(a.output)
			a.status = "已复制"
			c.Invalidate()
		}
		if ui.Button(c, "退出").Clicked() {
			mygo.App.Quit()
		}
	})
}

func (a *app) saveTarget() {
	d := a.store.Load()
	d.TargetIndex = a.targetIdx
	d.ProviderIndex = a.providerIdx
	d.Glossary = a.glossaryText
	_ = a.store.Save(d)
}

func (a *app) openSettings() {
	a.showSettings = true
	a.panelKeys = map[int]string{}
	for i := 0; i < provider.ProviderCount(); i++ {
		if k := a.store.APIKey(i); k != "" {
			a.panelKeys[i] = k
		}
	}
	a.panelActiveProv = a.providerIdx
}

// settingsView builds the modal settings panel.
func (a *app) settingsView(c *ui.Context) {
	if !a.showSettings {
		return
	}
	if ui.Modal(c, &a.showSettings, func() {
		ui.Column(c).Gap(10).Padding(16).Width(420).Children(func() {
			ui.Text(c, "设置").Bold()

			// Model picker: switching files the edited key under the
			// provider it was typed for, then swaps in the other provider's.
			provs := make([]string, provider.ProviderCount())
			for i := range provs {
				provs[i] = provider.ProviderDisplayName(i)
			}
			pchoice := provider.ProviderDisplayName(a.panelActiveProv)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "模型").Width(80)
				psel := ui.Select(c, &pchoice, provs)
				if psel.Changed() {
					for i, name := range provs {
						if name == pchoice {
							a.panelActiveProv = i
							break
						}
					}
				}
			})

			// Per-provider key field: the value lives in panelKeys for the
			// active provider.
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "API Key").Width(80)
				key := a.panelKeys[a.panelActiveProv]
				kf := ui.TextInput(c, &key).Password().Placeholder("在此填写当前模型的 API Key").Grow(1)
				a.panelKeys[a.panelActiveProv] = key
				_ = kf
			})

			ui.Text(c, "术语词典（每行一条：术语 = 翻译）").FontSize(11)
			ui.TextArea(c, &a.glossaryText).Height(120)
			_ = c.Theme()

			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					a.showSettings = false
				}
				if ui.PrimaryButton(c, "确定").Clicked() {
					a.confirmSettings()
				}
			})
		})
	}).Dismissed() {
		// Escape / click outside = cancel; nothing to revert (edits commit
		// only on 确定).
	}
}

func (a *app) confirmSettings() {
	a.providerIdx = a.panelActiveProv
	for i := 0; i < provider.ProviderCount(); i++ {
		k := a.panelKeys[i]
		old := a.store.APIKey(i)
		if (old == "") != (k == "") || (k != "" && old != k) {
			_ = a.store.SetAPIKey(i, k)
		}
	}
	d := a.store.Load()
	d.TargetIndex = a.targetIdx
	d.ProviderIndex = a.providerIdx
	d.Glossary = a.glossaryText
	_ = a.store.Save(d)
	a.showSettings = false
}

// autoTargetName is the 中英互译 entry in the target-language picker.
const autoTargetName = "中英互译"

// autoTargetIndex is the stored index for the auto entry (must match
// provider.AutoBidirectional).
const autoTargetIndex = -1

// targetDisplayName renders a stored target index for the picker.
func targetDisplayName(idx int) string {
	if idx == autoTargetIndex {
		return autoTargetName
	}
	return provider.TargetDisplayName(idx)
}

// targetIndexForName maps a picker choice back to the stored index.
func targetIndexForName(name string) int {
	if name == autoTargetName {
		return autoTargetIndex
	}
	for i := 0; i < provider.TargetCount(); i++ {
		if provider.TargetDisplayName(i) == name {
			return i
		}
	}
	return autoTargetIndex
}
