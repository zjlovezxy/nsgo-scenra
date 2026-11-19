// Command scenra is a desktop app designer with an AI assistant: a folder
// of Scenra files, each a design or a flow chart, as a tree on the left,
// the file chosen beside it, and on the right a conversation with a coding
// agent, such as Claude Code or Codex, run through Agio, which edits the
// folder's files as the scenra skill among the app's resources teaches.
// Each folder opens in a tab of its own, scenraview's Window, beside a
// home page of the folders opened lately and the settings, with the app's
// menu and usage statistics.
package main

import (
	"os"
	"path/filepath"

	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/desktop"
	"github.com/nsgo-dev/nsgo/i18n"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
	"github.com/nsgo-dev/nsgo/updater"
	"github.com/oneo-me/nsgo-agio/agent"
	"github.com/oneo-me/nsgo-agio/chatview"
	"github.com/oneo-me/nsgo-agio/host"

	"github.com/oneo-me/nsgo-scenra/cmd/scenra/locales"
	"github.com/oneo-me/nsgo-scenra/scenraview"
	words "github.com/oneo-me/nsgo-scenra/scenraview/locales"
)

func main() {
	agent.Client = agent.ClientInfo{Name: "scenra", Title: "Scenra", Version: "1"}
	// `scenra serve` runs the agents of a folder for a window elsewhere,
	// without a window of its own.
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		os.Exit(serve(os.Args[2:]))
	}
	nsgo.Run(Root, append([]nsgo.Option{nsgo.WindowSize(1440, 900), nsgo.WindowConstraints(core.WindowConstraints{MinWidth: 1000, MinHeight: 480})},
		demoOptions()...)...)
}

// shown are the tabs of the window shown last, which tests reach.
var shown *tabs

// Root is the app's window: its tabs in its title bar, the home page
// first, over the one shown, and the name of its file in the title.
// Connecting to another machine's agents and back builds it again, for the
// tabs the storage kept open.
func Root(cx *core.Scope) core.Node {
	home, _ := os.UserHomeDir()
	sess := newSession(cx)
	cx.Cleanup(sess.close)
	return core.Dynamic(func(cx *core.Scope) core.Node {
		sess.rev.Get()
		return root(cx, host.Options{Dir: home}, sess)
	})
}

// root is Root with the agents agents runs and the machine sess says they
// start on.
func root(cx *core.Scope, agents host.Options, sess *session) core.Node {
	rememberLanguage(cx)
	toasts := ui.NewToaster(cx)
	// What connecting to another machine's agents failed with shows, once
	// per message.
	failed := ""
	cx.Effect(func() {
		p := sess.problem.Get()
		if p == "" || p == failed {
			return
		}
		failed = p
		cx.Untrack(func() {
			toasts.Show(ui.ToastContent{Type: ui.ToastError, Title: locales.For(cx).RemoteFailed(p)})
		})
	})
	page := core.NewSignal(cx, pageRecent)
	stats := newUsage(cx)
	var m *chatview.Model
	var srv *scenraview.Server
	if demoBuild {
		m = demoAgents(cx)
	} else {
		m, srv = startAgents(cx, agents, sess)
	}
	namer := newNamer(cx, m)
	// Agents elsewhere cannot reach this window's server, so their
	// instructions don't tell them to ask it what the user sees.
	remote := sess.backend() != nil
	if remote {
		srv = nil
	}
	var t *tabs
	// The menu's Settings shows the home page's.
	settings := func() {
		cx.Batch(func() {
			t.shown.Set(nil)
			page.Set(pageSettings)
		})
	}
	// Each tab's editor works in its own folder, which its conversations
	// name to the server.
	t = newTabs(cx, toasts, newRecents(cx), func(cx *core.Scope, dir string) *scenraview.Editor {
		e := scenraview.New(cx, scenraview.Options{
			Agents:   m,
			Toaster:  toasts,
			Remember: "editor",
			OnSend:   stats.sent,
			Folder:   dir,
			NoView:   remote,
		})
		if srv != nil {
			srv.Attach(e)
		}
		stats.watch(cx, e)
		return e
	})
	shown = t
	stats.start()
	demoOpen(t)
	// Scenra files or folders opened from the Finder, or named on the
	// command line.
	desktop.OnOpenFiles(cx, func(paths []string) {
		for _, path := range paths {
			if err := t.openPath(path); err != nil {
				toasts.Show(ui.ToastContent{Type: ui.ToastError, Title: words.For(cx).OpenFailed(err.Error())})
			}
		}
	})
	// Closing the window saves every tab's changes, and stays open when
	// saving fails.
	if w := desktop.CurrentWindow(cx); w != nil {
		w.OnClosing(cx, func(c *desktop.WindowClosing) {
			if !t.saveAll() {
				c.Cancel()
			}
		})
	}

	titleBar := desktop.IsAvailable(cx)
	if titleBar {
		desktop.SetTitleBar(cx, desktop.TitleBarHidden, scenraview.HeaderHeight)
		cx.Effect(func() {
			e := t.editorShown()
			title := locales.For(cx).AppName()
			if e != nil {
				title = e.Title()
			}
			switch {
			case e == nil, e.Showcase():
			case e.Empty():
				title = filepath.Base(e.Folder())
			case e.Edited():
				title += " — " + words.For(cx).Edited()
			}
			desktop.SetTitle(cx, title)
		})
		cx.Effect(func() { desktop.SetApplicationMenu(cx, menu(cx, t, settings)) })
	} else {
		do := func(f func(*scenraview.Editor)) func() {
			return func() {
				if e := core.Untracked(cx, t.editorShown); e != nil {
					f(e)
				}
			}
		}
		cx.OnKey("mod+n", do((*scenraview.Editor).NewChat))
		cx.OnKey("mod+o", t.pick)
		cx.OnKey("mod+s", do((*scenraview.Editor).Save))
		cx.OnKey("mod+b", do((*scenraview.Editor).TogglePanels))
		cx.OnKey("mod+1", do((*scenraview.Editor).ActualSize))
		cx.OnKey("mod+2", do((*scenraview.Editor).Fit))
		cx.OnKey("mod+=", do((*scenraview.Editor).ZoomIn))
		cx.OnKey("mod+-", do((*scenraview.Editor).ZoomOut))
		cx.OnKey("mod+w", t.closeShown)
	}

	// A new language builds the window's views again in it; the tabs'
	// editors, made in scopes of their own, keep their documents and
	// conversations.
	return core.Dynamic(func(cx *core.Scope) core.Node {
		i18n.Language(cx)
		// What it builds mounts untracked, outside this function.
		return core.Func(func(cx *core.Scope) core.Node {
			colors := ui.ColorsOf(cx)
			return core.View{Style: ui.FillRest, StyleFunc: ui.Filled(colors.Background), Children: []core.Node{
				tabBar(cx, t, titleBar),
				core.View{Style: ui.FillRest, Children: []core.Node{
					layer(func() bool { return t.shown.Get() == nil }, homePage(cx, t, page, m, namer, sess)),
					core.Each(t.open.Get, func(tb *tab) *tab { return tb }, func(cx *core.Scope, item func() *tab, _ func() int) core.Node {
						tb := item()
						return layer(func() bool { return t.shown.Get() == tb }, scenraview.Window{Editor: tb.e, NoHeader: true})
					}),
				}},
				ui.Toasts{Toaster: toasts, Placement: ui.ToastBottomStart},
			}}
		})
	})
}

// layer is a page of the window, over the others while shown says so;
// hidden ones stay laid out, keeping their state and their canvases'
// views.
func layer(shown func() bool, page core.Node) core.Node {
	return core.View{StyleFunc: func() core.Style {
		st := core.Style{Position: core.Absolute, Left: core.Pt(0), Top: core.Pt(0), Right: core.Pt(0), Bottom: core.Pt(0)}
		if !shown() {
			st.Visibility = core.VisibilityHidden
		}
		return st
	}, Children: []core.Node{page}}
}

// tabBar is the bar of the window's tabs: the home page's, then one per
// open folder, marked while its file has unsaved changes, which closes
// from its button. As the window's title bar,
// with titleBar, it drags the window and keeps clear of its controls.
func tabBar(cx *core.Scope, t *tabs, titleBar bool) core.Node {
	tr := locales.For(cx)
	colors := ui.ColorsOf(cx)
	inset, drag := core.Edges{}, core.WindowDragInherit
	if titleBar {
		drag = core.WindowDragOn
	}
	bar := func() core.Edges {
		if titleBar {
			return desktop.TitleBar(cx)
		}
		return inset
	}
	look := func(shown func() bool) func() core.Style {
		return func() core.Style {
			st := core.Style{Hover: &core.Style{Background: ui.Alpha(colors.Text(), .06)}}
			if shown() {
				st.Background = colors.Background()
			}
			return st
		}
	}
	face := core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 6, Height: core.Pt(28), CornerRadius: ui.RadiusControl}
	// A tab takes the pointer from the bar, which drags the window.
	own := func(n core.Node) core.Node {
		return core.View{Desktop: core.DesktopViewProps{WindowDrag: core.WindowDragOff},
			Style: core.Style{MinWidth: core.Pt(0), FlexShrink: 1}, Children: []core.Node{n}}
	}
	home := func() bool { return t.shown.Get() == nil }
	homeTab := own(ui.Press{OnPress: func() { t.shown.Set(nil) }, Label: tr.Home(), TestID: "tab-home", Selected: home, Scale: 1,
		Style: face.Merge(core.Style{Width: core.Pt(36), JustifyContent: core.JustifyCenter}), StyleFunc: look(home),
		Children: []core.Node{ui.Icon(lucide.House, 15, colors.Text)}})
	folders := core.Each(t.open.Get, func(tb *tab) *tab { return tb }, func(cx *core.Scope, item func() *tab, _ func() int) core.Node {
		tb := item()
		shown := func() bool { return t.shown.Get() == tb }
		name := core.Text{ContentFunc: func() string { return filepath.Base(tb.e.Folder()) }, MaxLines: 1,
			Style: ui.TypeBody.Merge(core.Style{FlexShrink: 1, MinWidth: core.Pt(0)}),
			StyleFunc: func() core.Style {
				if shown() {
					return core.Style{Color: colors.Text()}
				}
				return core.Style{Color: colors.Muted()}
			}}
		return own(ui.Press{OnPress: func() { t.shown.Set(tb) }, LabelFunc: func() string { return filepath.Base(tb.e.Folder()) },
			TestID: "tab", Selected: shown, Scale: 1,
			Style: face.Merge(core.Style{MaxWidth: core.Pt(220), MinWidth: core.Pt(0), Padding: core.Edges{Left: 10, Right: 4}}), StyleFunc: look(shown),
			Children: []core.Node{
				ui.Icon(lucide.Folder, 14, colors.Muted),
				name,
				core.Show{When: tb.e.Edited, Then: func(*core.Scope) core.Node {
					return core.View{Style: core.Style{Width: core.Pt(7), Height: core.Pt(7), CornerRadius: 3.5, FlexShrink: 0},
						StyleFunc: ui.Filled(colors.Muted), Label: words.For(cx).Edited(), TestID: "tab-edited"}
				}},
				ui.Spacer{},
				ui.IconButton{Icon: lucide.X, Label: tr.CloseTab(), Size: 20, Tint: colors.Muted, OnPress: func() { t.close(tb) }, TestID: "close-tab"},
			}})
	})
	return core.View{TestID: "tabs", Label: tr.Tabs(), Desktop: core.DesktopViewProps{WindowDrag: drag},
		StyleFunc: func() core.Style {
			b := bar()
			return core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 4, FlexShrink: 0,
				Height: core.Pt(max(scenraview.HeaderHeight, b.Top)), Padding: core.Edges{Left: b.Left + 8, Right: b.Right + 8},
				Background: ui.Chrome(cx), Borders: core.Edges{Bottom: 1}, BorderColors: core.EdgeColors{Bottom: colors.Line()}}
		},
		Children: []core.Node{homeTab, folders}}
}

// menu is the application menu, whose document's items act on the tab
// shown.
func menu(cx *core.Scope, t *tabs, settings func()) []desktop.MenuItem {
	tr, w := locales.For(cx), words.For(cx)
	e := t.editorShown()
	// is says what f says of the tab shown, false on the home page; do
	// does f there.
	is := func(f func(*scenraview.Editor) bool) bool { return e != nil && f(e) }
	do := func(f func(*scenraview.Editor)) func() {
		return func() {
			if e != nil {
				f(e)
			}
		}
	}
	home := e == nil
	return []desktop.MenuItem{
		{Label: tr.AppName(), Items: []desktop.MenuItem{
			{Role: desktop.MenuAbout},
			{Label: tr.CheckForUpdates(), OnSelect: func() { updater.Check(cx) }}, {Separator: true},
			{Label: w.Settings(), Shortcut: "mod+,", OnSelect: settings}, {Separator: true},
			{Role: desktop.MenuHide}, {Role: desktop.MenuHideOthers}, {Role: desktop.MenuShowAll}, {Separator: true},
			{Role: desktop.MenuQuit},
		}},
		{Label: tr.FileMenu(), Items: []desktop.MenuItem{
			{Label: w.NewChat(), Shortcut: "mod+n", Disabled: !is(func(e *scenraview.Editor) bool { return e.Folder() != "" }), OnSelect: do((*scenraview.Editor).NewChat)},
			{Label: w.OpenFolder(), Shortcut: "mod+o", OnSelect: t.pick},
			{Separator: true},
			{Label: w.Save(), Shortcut: "mod+s", Disabled: home, OnSelect: do((*scenraview.Editor).Save)},
			{Separator: true},
			{Label: tr.CloseTab(), Shortcut: "mod+w", OnSelect: t.closeShown},
		}},
		{Label: tr.EditMenu(), Items: []desktop.MenuItem{
			{Role: desktop.MenuUndo}, {Role: desktop.MenuRedo}, {Separator: true},
			{Role: desktop.MenuCut}, {Role: desktop.MenuCopy}, {Role: desktop.MenuPaste}, {Role: desktop.MenuSelectAll},
			{Separator: true},
			{Label: tr.UndoChange(), Shortcut: "mod+alt+z", Disabled: !is((*scenraview.Editor).CanUndo), OnSelect: do((*scenraview.Editor).Undo)},
			{Label: tr.RedoChange(), Shortcut: "mod+alt+shift+z", Disabled: !is((*scenraview.Editor).CanRedo), OnSelect: do((*scenraview.Editor).Redo)},
		}},
		{Label: tr.ViewMenu(), Items: []desktop.MenuItem{
			{Label: w.ActualSize(), Shortcut: "mod+1", Disabled: home, OnSelect: do((*scenraview.Editor).ActualSize)},
			{Label: w.Fit(), Shortcut: "mod+2", Disabled: home, OnSelect: do((*scenraview.Editor).Fit)},
			{Label: w.ZoomIn(), Shortcut: "mod+=", Disabled: home, OnSelect: do((*scenraview.Editor).ZoomIn)},
			{Label: w.ZoomOut(), Shortcut: "mod+-", Disabled: home, OnSelect: do((*scenraview.Editor).ZoomOut)},
			{Separator: true},
			{Label: w.HidePanels(), Shortcut: "mod+b", Checkable: true, Checked: is((*scenraview.Editor).PanelsHidden), Disabled: home, OnSelect: do((*scenraview.Editor).TogglePanels)},
			{Label: w.Components(), Shortcut: "mod+shift+k", Disabled: !is((*scenraview.Editor).HasComponents), OnSelect: do((*scenraview.Editor).ShowComponents)},
			{Label: w.HideOutlines(), Checkable: true, Checked: is((*scenraview.Editor).OutlinesHidden), Disabled: home, OnSelect: do((*scenraview.Editor).ToggleOutlines)},
			{Separator: true},
			{Label: w.LeftToRight(), Checkable: true, Checked: is((*scenraview.Editor).LeftToRight), Disabled: !is((*scenraview.Editor).FlowOpen), OnSelect: do((*scenraview.Editor).ToggleDirection)},
		}},
	}
}
