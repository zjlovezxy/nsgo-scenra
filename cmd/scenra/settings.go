package main

import (
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"

	"github.com/oneo-me/nsgo-agio/chatview"
	"github.com/oneo-me/nsgo-scenra/cmd/scenra/locales"
)

// statsSwitch turns the usage statistics on or off.
func statsSwitch(cx *core.Scope) core.Node {
	on := core.NewSignal(cx, statsOn(cx))
	return ui.Switch{Checked: on.Get, OnChange: func(v bool) { on.Set(v); setStatsOn(cx, v) },
		Label: locales.For(cx).StatsSend()}
}

// settingsPage, a page of the home page, chooses the language, turns the
// agents and their models on or off, says which machine runs them, sets
// the model titling conversations, and turns the usage statistics on or
// off.
func settingsPage(cx *core.Scope, m *chatview.Model, n *namer, s *session) core.Node {
	tr := locales.For(cx)
	colors := ui.ColorsOf(cx)
	return ui.ScrollView{Style: ui.FillRest, ContentStyle: core.Style{AlignItems: core.AlignCenter}, Children: []core.Node{
		core.View{TestID: "settings", Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(640),
			Padding: core.Symmetric(32, 32), Gap: 24}, Children: []core.Node{
			ui.Text(tr.SettingsTitle(), 20, ui.Semibold, colors.Text),
			languageSection(cx),
			chatview.Agents{Model: m},
			remoteSection(cx, s),
			namingSection(cx, n),
			core.View{Style: core.Style{Gap: 4}, Children: []core.Node{
				ui.Text(tr.StatsSection(), 15, ui.Semibold, colors.Text),
				core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12}, Children: []core.Node{
					core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, Gap: 2}, Children: []core.Node{
						ui.Text(tr.StatsSend(), 13, ui.Medium, colors.Text),
						core.Text{Content: tr.StatsDetail(), Style: ui.TypeCaption, StyleFunc: ui.Colored(colors.Muted)},
					}},
					statsSwitch(cx),
				}},
			}},
		}},
	}}
}
