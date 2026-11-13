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

	// quota exhausted: once the provider reports the free-tier allowance is
	// used up, auto-translate stops calling the model and a modal explains;
	// the user re-arms via the modal's 重试 button.
	quotaExhausted bool
	showQuotaAlert bool

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
	// Quota gate: once the provider reported the free allowance as used up,
	// stop calling the model and surface the modal again.
	if a.quotaExhausted {
		a.status = "免费额度已用完，未调用模型"
		a.showQuotaAlert = true
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
				// Free-tier exhaustion: trip the gate so subsequent
				// auto-translations stop calling the model.
				if provider.IsQuotaExhaustedError(res.Error) {
					a.quotaExhausted = true
					a.showQuotaAlert = true
				}
			}
			win.Invalidate()
		})
	}()
}

// quotaAlertView shows the free-quota-exhausted dialog with a re-arm
// button: 重试 clears the gate (the next translation calls the model
// again — the allowance may have reset, or the user switched models).
func (a *app) quotaAlertView(c *ui.Context) {
	if !a.showQuotaAlert {
		return
	}
	if ui.Modal(c, &a.showQuotaAlert, func() {
		t := c.Theme()
		ui.Column(c).Gap(10).Padding(20).Width(380).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "⚠").FontSize(18).TextColor(t.Warning)
				ui.Text(c, "免费额度已用完").Bold().FontSize(15)
			})
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, a.status).FontSize(12)
				ui.Text(c, "已停止调用模型翻译。免费额度通常按天重置；也可以在设置中切换其他模型。").FontSize(12).TextColor(t.TextMuted)
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.PrimaryButton(c, "重试").Clicked() {
					a.quotaExhausted = false
					a.showQuotaAlert = false
					c.Invalidate()
				}
			})
		})
	}).Dismissed() {
		// Click outside/Escape closes the dialog; the gate stays armed so
		// auto-translate keeps skipping model calls.
	}
}

const maxFloat32 = float32(3.4028235e38)

func (a *app) view(c *ui.Context) {
	t := c.Theme()

	// Root: padded column filling the window.
	ui.Column(c).Fill().Padding(16).Gap(10).Children(func() {
		if a.tab == 1 {
			a.logView(c)
		} else {
			a.mainContent(c, t)
		}
	})

	a.settingsView(c)
	a.quotaAlertView(c)
}

func (a *app) mainContent(c *ui.Context, t *ui.Theme) {

	// Debounce: schedule a frame for when auto-translate should fire, then
	// start the translation on that frame. Arms on source edits; a new edit
	// before the delay re-arms.
	if ui.TextArea(c, &a.source).Placeholder("输入要翻译的文本…").Height(110).
		FontSize(13).LineHeight(1.5).Changed() {
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

	// Status line: spinner while translating, colored dot on finish/error.
	ui.Row(c).Gap(6).AlignItems(ui.Center).Height(18).Children(func() {
		if a.phase == 1 {
			ui.Spinner(c).Label("翻译中")
			ui.Text(c, "翻译中…").FontSize(11).TextColor(t.TextMuted)
		} else {
			statusText := a.status
			if statusText == "" {
				statusText = "输入后自动翻译"
			}
			switch {
			case strings.HasPrefix(statusText, "完成"):
				ui.Text(c, "●").FontSize(11).TextColor(t.Success)
			case statusText == "已复制" || statusText == "输入后自动翻译":
				ui.Text(c, "●").FontSize(11).TextColor(t.TextMuted)
			default:
				ui.Text(c, "●").FontSize(11).TextColor(t.Danger)
			}
			ui.Text(c, statusText).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Text(c, "").Grow(1)
	})

	// Result card (grows, streams, follows the bottom). Comfortable reading:
	// 14px with 1.6 line height, close to native macOS reading text.
	ui.Scroll(c).TrackScroll(&a.resultScroll).Grow(1).Children(func() {
		ui.Column(c).Fill().Children(func() {
			out := a.output
			if out == "" && a.phase == 0 {
				ui.Text(c, "译文会显示在这里").FontSize(13).TextColor(t.TextMuted).LineHeight(1.6)
			} else {
				ui.Text(c, out).FontSize(14).LineHeight(1.6)
			}
		})
	})

	// Bottom bar: target language + actions.
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		langs := make([]string, 0, provider.TargetCount()+1)
		langs = append(langs, autoTargetName)
		for i := 0; i < provider.TargetCount(); i++ {
			langs = append(langs, provider.TargetDisplayName(i))
		}
		choice := targetDisplayName(a.targetIdx)
		ui.Text(c, "目标语言").FontSize(12).TextColor(t.TextMuted)
		sel := ui.Select(c, &choice, langs).Width(160)
		if sel.Changed() {
			a.targetIdx = targetIndexForName(choice)
			a.saveTarget()
			// Retranslate with the new language.
			if a.phase == 0 && strings.TrimSpace(a.source) != "" {
				a.translateFromView()
			}
		}
		ui.Text(c, "").Grow(1)
		if ui.Button(c, "设置").Clicked() {
			a.openSettings()
		}
		if a.output != "" {
			if ui.PrimaryButton(c, "复制结果").Clicked() {
				c.WriteClipboard(a.output)
				a.status = "已复制"
				c.Invalidate()
			}
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
		ui.Column(c).Gap(14).Padding(20).Width(440).Children(func() {
			ui.Text(c, "设置").Bold().FontSize(15)

			// Model section.
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "模型").FontSize(12).TextColor(c.Theme().TextMuted)
				provs := make([]string, provider.ProviderCount())
				for i := range provs {
					provs[i] = provider.ProviderDisplayName(i)
				}
				pchoice := provider.ProviderDisplayName(a.panelActiveProv)
				psel := ui.Select(c, &pchoice, provs).FillWidth()
				if psel.Changed() {
					for i, name := range provs {
						if name == pchoice {
							a.panelActiveProv = i
							break
						}
					}
				}

				// Per-provider key field: the value lives in panelKeys for
				// the active provider.
				key := a.panelKeys[a.panelActiveProv]
				ui.TextInput(c, &key).Password().
					Placeholder("API Key（当前模型）").FillWidth()
				a.panelKeys[a.panelActiveProv] = key
			})

			// Glossary section.
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "术语词典（每行一条：术语 = 翻译）").FontSize(12).TextColor(c.Theme().TextMuted)
				ui.TextArea(c, &a.glossaryText).Height(120)
			})

			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "查看日志").Clicked() {
					a.showSettings = false
					a.tab = 1
				}
				ui.Text(c, "").Grow(1)
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
const autoTargetName = "中英互译"// autoTargetIndex is the stored index for the auto entry (must match
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
