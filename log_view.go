package main

import (
	"ciwang/translate/internal/pipeline"

	"github.com/egoist/mygo/ui"
)

// logView is the log page: a scrollable view of the latest translation
// run's detailed trace (routing, group requests/replies, per-line
// verdicts, rescue retries), reached from 设置 → 查看日志.
func (a *app) logView(c *ui.Context) {
	t := c.Theme()
	text := a.log.Render()

	// Header: title + back.
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.Button(c, "← 返回翻译").Clicked() {
			a.tab = 0
			c.Invalidate()
		}
		ui.Text(c, "翻译日志").Bold()
		ui.Text(c, "").Grow(1)
	})

	ui.Scroll(c).TrackScroll(&a.logScroll).Grow(1).Children(func() {
		ui.Column(c).Fill().Children(func() {
			if text == "" {
				ui.Text(c, "暂无日志 — 翻译一次后这里会显示完整流程").FontSize(12).TextColor(t.TextMuted).LineHeight(1.5)
			} else {
				ui.Text(c, text).FontSize(12).LineHeight(1.5)
			}
		})
	})

	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, "复制日志").Clicked() && text != "" {
			c.WriteClipboard(text)
		}
		if ui.Button(c, "清空").Clicked() {
			a.log = pipeline.NewLogger()
			c.Invalidate()
		}
		ui.Text(c, "").Grow(1)
		ui.Text(c, "每次翻译生成一份新日志").FontSize(11).TextColor(t.TextMuted)
	})
}

var _ = ui.Text
