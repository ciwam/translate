package main

import (
	"ciwang/translate/internal/pipeline"

	"github.com/egoist/mygo/ui"
)

// logView is the log page: a scrollable view of the latest translation
// run's detailed trace (routing, group requests/replies, per-line
// verdicts, rescue retries), reached from 设置 → 查看日志.
func (a *app) logView(c *ui.Context) {
	text := a.log.Render()
	ui.Scroll(c).TrackScroll(&a.logScroll).Grow(1).Children(func() {
		ui.Text(c, text)
	})
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, "返回翻译").Clicked() {
			a.tab = 0
			c.Invalidate()
		}
		if ui.Button(c, "复制日志").Clicked() && text != "" {
			c.WriteClipboard(text)
		}
		if ui.Button(c, "清空").Clicked() {
			a.log = pipeline.NewLogger()
			c.Invalidate()
		}
		ui.Text(c, "每次翻译生成一份新日志").FontSize(11).TextColor(c.Theme().TextMuted)
	})
}

var _ = ui.Text
