package scenraview

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
	agio "github.com/oneo-me/nsgo-agio"
	"github.com/oneo-me/nsgo-agio/chatview"
	"github.com/oneo-me/nsgo-agio/transcript"

	"github.com/oneo-me/nsgo-scenra/scenraview/locales"
)

// assistant gives the open folder conversations of Options.Agents about
// it, one shown at a time, each starting its agent with its first message.
// The agent edits the folder's files itself, as the app's skill teaches,
// and asks the app's Server what the user sees. The conversations whose
// agents started a session are kept by folder, so opening the folder again
// resumes them.
type assistant struct {
	cx *core.Scope
	e  *Editor
	m  *chatview.Model
	// agent is the ID of the conversation shown, "" until there is one;
	// creating says one is being made.
	agent    *core.Signal[string]
	creating bool
	// dir is the folder whose conversations the backend holds, and
	// restoring how many of its saved ones are still being resumed.
	dir       string
	restoring int
	// saved are the conversations kept by folder, newest first, which key,
	// when set, keeps in the storage; resumed are the cursors the
	// conversations resumed this run were saved with, by ID.
	saved   map[string][]savedChat
	key     string
	resumed map[string]string
	// gone are the conversations being removed, which are no longer kept.
	gone map[string]bool
	// last is the agent, model and its settings chosen last in the open
	// folder, which its new conversations start with and choicePrefix,
	// when set, keeps in the storage by folder.
	last         choice
	choicePrefix string
}

// choice is a conversation's model, as its agent.Key naming its agent,
// reasoning effort and service tier.
type choice struct {
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
	Tier   string `json:"tier,omitempty"`
}

// savedChat is what resumes a conversation: its agent's session and
// choices.
type savedChat struct {
	Title   string    `json:"title,omitempty"`
	Model   string    `json:"model"`
	Effort  string    `json:"effort,omitempty"`
	Tier    string    `json:"tier,omitempty"`
	Cursor  string    `json:"cursor"`
	Created time.Time `json:"created,omitzero"`
	Updated time.Time `json:"updated,omitzero"`
}

func newAssistant(cx *core.Scope, e *Editor) *assistant {
	a := &assistant{cx: cx, e: e, m: e.opts.Agents, agent: core.NewSignal(cx, ""),
		saved: map[string][]savedChat{}, resumed: map[string]string{}, gone: map[string]bool{}}
	if a.m == nil {
		return a
	}
	if e.opts.Remember != "" {
		a.key = e.opts.Remember + ".chats"
		a.choicePrefix = e.opts.Remember + ".choice"
		a.reload()
	}
	// The open folder gets its conversations, whose agents stop as the
	// editor goes.
	cx.Effect(func() {
		dir := e.folder.Get()
		cx.Untrack(func() { a.open(dir) })
	})
	cx.Cleanup(func() {
		for _, c := range a.chats() {
			id := c.ID
			a.m.Do(func(b agio.Backend) error { return b.Remove(id) }, nil)
		}
	})
	// What resumes them follows the backend.
	cx.Effect(func() {
		s := a.m.State()
		cx.Untrack(func() { a.note(s) })
	})
	// What the user chooses in the conversation shown is what new ones
	// start with; what it shows first is only what it was made or
	// resumed with, or known once the agents' models load.
	var seen string
	var was choice
	cx.Effect(func() {
		id := a.agent.Get()
		c := a.m.Conversation(id)
		now := choice{Model: c.Model, Effort: c.Effort, Tier: c.Tier}
		cx.Untrack(func() {
			if id != "" && id == seen && was.Model != "" && now.Model != "" && now != was {
				a.choose(now)
			}
			seen, was = id, now
		})
	})
	return a
}

// choiceKey is the storage key of the choice the folder dir's new
// conversations start with, which other editors of the folder change too,
// so each folder keeps a model of its own; "" without a key to keep it.
func (a *assistant) choiceKey(dir string) string {
	if a.choicePrefix == "" || dir == "" {
		return ""
	}
	return a.choicePrefix + "." + folderID(dir)
}

// reload reads again the conversations kept and the choice the open
// folder's conversations start with, which other editors under the same
// keys change too, falling back on the choice kept for every folder
// before each kept one.
func (a *assistant) reload() {
	st := a.cx.Storage()
	if a.key != "" {
		saved := map[string][]savedChat{}
		if data, ok := st.GetItem(a.key); ok {
			json.Unmarshal([]byte(data), &saved)
		}
		a.saved = saved
	}
	if a.choicePrefix == "" {
		return
	}
	a.last = choice{}
	for _, key := range []string{a.choiceKey(a.dir), a.choicePrefix} {
		if key == "" {
			continue
		}
		if data, ok := st.GetItem(key); ok {
			json.Unmarshal([]byte(data), &a.last)
			return
		}
	}
}

// choose keeps c as the choice the open folder's new conversations start
// with.
func (a *assistant) choose(c choice) {
	if c == a.last {
		return
	}
	a.last = c
	key := a.choiceKey(a.dir)
	if key == "" {
		return
	}
	if data, err := json.Marshal(c); err == nil {
		a.cx.Storage().SetItem(key, string(data))
	}
}

// prompt is the system prompt of a conversation about the folder dir,
// which names the editor of the folder to the server's tools; without
// view, the agent runs on another machine and cannot ask them, so it reads
// the files itself instead.
func (a *assistant) prompt(dir string, view bool) string {
	tell := "ask the scenra MCP server's " + ToolView + ` tool what the user sees and has selected before you change them, as the scenra-app skill says. Give each of its tools tab "` + folderID(dir) + `", as other tabs show other folders.`
	if !view {
		tell = "read the files themselves, as you cannot see the user's editor, which runs on another machine with the folder's files, and tell the user what you changed."
	}
	return "You are an app designer working in Scenra, a design and flow editor. The user has the folder " + dir + ` open in it, in the tab ` + folderID(dir) + `, beside this conversation, which is about the folder's Scenra files, kept in its design folder when it has none of its own: each holds one design of a screen or one flow chart. Edit and make them as the scenra skill says, and ` + tell + `

Discuss the app with the user: ask about its screens, what each shows, the steps, decisions, actors and edge cases of what happens on them when they are unclear, suggest improvements, and draw as you agree. Reply in the user's language.`
}

// open shows the conversations about the folder dir in place of the
// previous folder's, whose agents stop: its saved ones resumed, the
// newest shown, or a new one.
func (a *assistant) open(dir string) {
	if dir == a.dir {
		return
	}
	for _, c := range a.chats() {
		id := c.ID
		a.m.Do(func(b agio.Backend) error { return b.Remove(id) }, nil)
	}
	a.dir, a.restoring = dir, 0
	a.agent.Set("")
	if dir != "" {
		// The folder's conversations start with the model it last had.
		a.reload()
	}
	if dir == "" {
		return
	}
	saved := a.saved[dir]
	if len(saved) == 0 {
		a.create()
		return
	}
	a.restoring = len(saved)
	for _, s := range slices.Backward(saved) {
		c := agio.Create{Dir: dir, Title: s.Title, SystemPrompt: a.prompt(dir, !a.e.opts.NoView), Model: s.Model, Effort: s.Effort, Tier: s.Tier,
			Cursor: s.Cursor, Created: s.Created, Updated: s.Updated}
		var id string
		a.m.Do(func(b agio.Backend) (err error) {
			id, err = b.Create(c)
			return err
		}, func(err error) {
			if a.dir != dir {
				if err == nil {
					a.m.Do(func(b agio.Backend) error { return b.Remove(id) }, nil)
				}
				return
			}
			if err == nil {
				a.resumed[id] = s.Cursor
			}
			if a.restoring--; a.restoring == 0 {
				a.showLatest()
			}
		})
	}
}

// showLatest shows the folder's latest conversation, or a new one.
func (a *assistant) showLatest() {
	if chats := a.chats(); len(chats) > 0 {
		a.agent.Set(chats[0].ID)
		return
	}
	a.create()
}

// chats are the conversations about the open folder, the latest first,
// as the backend has them now.
func (a *assistant) chats() []*agio.Conversation {
	if a.m == nil || a.dir == "" {
		return nil
	}
	return folderChats(a.m.Backend().State(), a.dir)
}

// folderChats are s's conversations about the folder dir, the latest
// first.
func folderChats(s agio.State, dir string) []*agio.Conversation {
	var chats []*agio.Conversation
	for _, c := range s.Conversations {
		if c.Dir == dir {
			chats = append(chats, c)
		}
	}
	slices.SortStableFunc(chats, func(x, y *agio.Conversation) int { return lastActive(y).Compare(lastActive(x)) })
	return chats
}

// lastActive is when c last changed.
func lastActive(c *agio.Conversation) time.Time {
	if c.Updated.After(c.Created) {
		return c.Updated
	}
	return c.Created
}

// chatTitle names c: its title, else its first message's first line, ""
// before it has either.
func chatTitle(c *agio.Conversation) string {
	if c.Title != "" {
		return c.Title
	}
	if len(c.Turns) == 0 {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(c.Turns[0].Prompt), "\n")
	if r := []rune(line); len(r) > 60 {
		line = string(r[:60]) + "…"
	}
	return line
}

// note keeps what resumes the open folder's conversations whose agents
// started a session, and those saved that could not be resumed, such as
// while the agent is turned off.
func (a *assistant) note(s agio.State) {
	if a.dir == "" {
		return
	}
	var kept []savedChat
	live := map[string]bool{}
	for _, c := range folderChats(s, a.dir) {
		if a.gone[c.ID] {
			continue
		}
		live[c.Cursor], live[a.resumed[c.ID]] = true, true
		if c.Cursor != "" && c.Model != "" {
			kept = append(kept, savedChat{Title: chatTitle(c), Model: c.Model, Effort: c.Effort, Tier: c.Tier,
				Cursor: c.Cursor, Created: c.Created, Updated: c.Updated})
		}
	}
	for _, c := range a.saved[a.dir] {
		if !live[c.Cursor] {
			kept = append(kept, c)
		}
	}
	slices.SortStableFunc(kept, func(x, y savedChat) int { return y.Updated.Compare(x.Updated) })
	a.keep(kept)
}

// keep saves chats as the open folder's, among the other folders' kept
// since.
func (a *assistant) keep(chats []savedChat) {
	if slices.Equal(chats, a.saved[a.dir]) {
		return
	}
	a.reload()
	if len(chats) == 0 {
		delete(a.saved, a.dir)
	} else {
		a.saved[a.dir] = chats
	}
	if a.key == "" {
		return
	}
	if data, err := json.Marshal(a.saved); err == nil {
		a.cx.Storage().SetItem(a.key, string(data))
	}
}

// create starts a conversation about the open folder, working in it, with
// the choice made last, and shows it.
func (a *assistant) create() {
	dir := a.dir
	if a.m == nil || dir == "" || a.creating {
		return
	}
	a.creating = true
	a.reload()
	c := agio.Create{Dir: dir, SystemPrompt: a.prompt(dir, !a.e.opts.NoView), Model: a.last.Model, Effort: a.last.Effort, Tier: a.last.Tier}
	a.m.Create(c, func(id string) {
		a.creating = false
		if a.dir != dir {
			a.m.Do(func(b agio.Backend) error { return b.Remove(id) }, nil)
			return
		}
		a.agent.Set(id)
	})
}

// newChat shows a new conversation, unless the one shown is still new.
func (a *assistant) newChat() {
	if a.m == nil || a.restoring > 0 {
		return
	}
	if c := a.m.Backend().State().Conversation(a.agent.GetUntracked()); c != nil && !c.Bound && len(c.Turns) == 0 {
		return
	}
	a.create()
}

// show shows the conversation id.
func (a *assistant) show(id string) { a.agent.Set(id) }

// remove forgets the conversation id and stops its agent, showing the
// latest of the others, or a new one.
func (a *assistant) remove(id string) {
	c := a.m.Backend().State().Conversation(id)
	if c == nil {
		return
	}
	a.keep(slices.DeleteFunc(slices.Clone(a.saved[a.dir]), func(s savedChat) bool {
		return s.Cursor != "" && (s.Cursor == c.Cursor || s.Cursor == a.resumed[id])
	}))
	delete(a.resumed, id)
	a.gone[id] = true
	a.m.Do(func(b agio.Backend) error { return b.Remove(id) }, func(error) {
		delete(a.gone, id)
		if a.agent.GetUntracked() == id {
			a.showLatest()
		}
	})
}

// ask sends prompt to the conversation, saving the documents first; nil
// without agents.
func (a *assistant) ask() func(prompt string) {
	if a.m == nil {
		return nil
	}
	return func(prompt string) {
		id := a.agent.GetUntracked()
		if id == "" || a.busy() {
			return
		}
		a.sent()
		a.m.Do(func(b agio.Backend) error { return b.Send(id, agio.Message{Text: prompt}) }, nil)
	}
}

// sent saves the documents, so what the agent reads is what the user
// sees, and keeps the conversation's choice, as a message is sent.
func (a *assistant) sent() {
	a.e.saveQuietly()
	if c := a.m.Backend().State().Conversation(a.agent.GetUntracked()); c != nil && c.Model != "" {
		a.choose(choice{Model: c.Model, Effort: c.Effort, Tier: c.Tier})
	}
	if a.e.opts.OnSend != nil {
		a.e.opts.OnSend()
	}
}

// working says the agent is at work, while which the user cannot edit
// the files; a caller reading it follows.
func (a *assistant) working() bool {
	if a.m == nil {
		return false
	}
	id := a.agent.Get()
	return id != "" && a.m.Conversation(id).Running
}

// busy is working as the backend has it now, which the model follows a
// little later, for actions to check.
func (a *assistant) busy() bool {
	if a.m == nil {
		return false
	}
	c := a.m.Backend().State().Conversation(a.agent.GetUntracked())
	return c != nil && c.Running
}

// group is the undo group of a change the agent made now: its running
// turn, whose changes undo together, else none.
func (a *assistant) group() string {
	if a.m == nil {
		return ""
	}
	// The backend's state, as the model's follows it a little later.
	c := a.m.Backend().State().Conversation(a.agent.GetUntracked())
	if c == nil || !c.Running || len(c.Turns) == 0 {
		return ""
	}
	t := c.Turns[len(c.Turns)-1]
	if t.Status != transcript.Running {
		return ""
	}
	return c.ID + "/" + t.ID
}

// assistantPane is the conversation about the open folder under a header
// that is the window's title bar with titleBar; or, while no folder is
// open, a way to open one.
func assistantPane(cx *core.Scope, a *assistant, titleBar bool) core.Node {
	tr := locales.For(cx)
	colors := ui.ColorsOf(cx)
	e := a.e
	_, drag := chrome(cx, titleBar)
	actions := []core.Node{ui.Spacer{},
		ui.IconButton{Icon: lucide.MessageSquarePlus, Label: tr.NewChat(), OnPress: a.newChat, TestID: "new-chat"}}
	if open := e.opts.OpenSettings; open != nil {
		actions = append(actions, ui.IconButton{Icon: lucide.Settings, Label: tr.Settings(), OnPress: open, TestID: "open-settings"})
	}
	header := core.View{Desktop: core.DesktopViewProps{WindowDrag: drag},
		Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, FlexShrink: 0, Borders: core.Edges{Bottom: 1}},
		StyleFunc: func() core.Style {
			bar, _ := chrome(cx, titleBar)
			return core.Style{Height: core.Pt(max(HeaderHeight, bar.Top)), Padding: core.Edges{Left: 12, Right: 8 + bar.Right},
				Background: ui.Chrome(cx), BorderColors: core.EdgeColors{Bottom: colors.Line()}}
		},
		Children: append([]core.Node{
			ui.Icon(lucide.Sparkles, 16, colors.Muted),
			chatPicker(cx, a),
		}, actions...)}
	placeholder := func(detail string, actions ...core.Node) core.Node {
		return core.View{Style: core.Style{FlexGrow: 1, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter, Padding: core.All(32), Gap: 10},
			Children: []core.Node{
				ui.Icon(lucide.Workflow, 32, colors.Muted),
				ui.Text(tr.EmptyTitle(), 15, ui.Semibold, colors.Text),
				core.Text{Content: detail, Style: core.Style{FontSize: 13, LineHeight: 19, TextAlign: core.TextAlignCenter, MaxWidth: core.Pt(320)},
					StyleFunc: ui.Colored(colors.Muted)},
				core.View{Style: core.Style{Direction: core.Row, Gap: 8, Margin: core.Edges{Top: 4}}, Children: actions},
			}}
	}
	// The conversation is built again for another one, as its draft and
	// scrolling are its own.
	body := core.Dynamic(func(cx *core.Scope) core.Node {
		id := a.agent.Get()
		switch {
		case e.folder.Get() == "":
			return placeholder(tr.NoFolder(),
				ui.Button{Text: tr.OpenFolder(), Icon: lucide.FolderOpen, Variant: ui.ButtonPrimary, OnPress: e.OpenFolder, TestID: "chat-open-folder"})
		case a.m == nil || id == "":
			return placeholder(tr.EmptyDetail())
		}
		empty := func() bool {
			c := a.m.Conversation(id)
			return len(c.Turns) == 0 && !c.Loading
		}
		// The placeholder leaves the composer free.
		return core.View{Style: ui.FillRest, Children: []core.Node{
			// What the user changed is saved for the agent to read.
			chatview.Conversation{Model: a.m, ID: id, TestID: "conversation", OnSend: a.sent},
			core.Show{When: empty, Then: func(*core.Scope) core.Node {
				return core.View{NoFocus: true, Style: core.Style{Position: core.Absolute, Left: core.Pt(0), Top: core.Pt(0), Right: core.Pt(0), Bottom: core.Pt(180)},
					Children: []core.Node{placeholder(tr.EmptyDetail())}}
			}},
		}}
	})
	return core.View{Style: ui.FillRest, StyleFunc: ui.Filled(colors.Background), Children: []core.Node{header, body}}
}

// chatPicker names the conversation shown and, from a menu, shows or
// deletes another about the folder; the assistant's name while there are
// none.
func chatPicker(cx *core.Scope, a *assistant) core.Node {
	tr := locales.For(cx)
	colors := ui.ColorsOf(cx)
	label := func(c *agio.Conversation) string {
		if t := chatTitle(c); t != "" {
			return t
		}
		return tr.NewChat()
	}
	// The items are built again only as the list or its names change.
	key := core.NewMemo(cx, func() string {
		if a.m == nil || a.e.folder.Get() == "" {
			return ""
		}
		var b strings.Builder
		for _, c := range folderChats(a.m.State(), a.e.folder.Get()) {
			b.WriteString(c.ID + "\x00" + label(c) + "\x00")
		}
		return b.String()
	})
	return core.Dynamic(func(cx *core.Scope) core.Node {
		if key.Get() == "" || a.agent.Get() == "" {
			return ui.Text(tr.Assistant(), 13, ui.Semibold, colors.Text)
		}
		var items []core.Node
		for _, c := range core.Untracked(cx, func() []*agio.Conversation { return folderChats(a.m.State(), a.e.folder.GetUntracked()) }) {
			id := c.ID
			items = append(items, ui.MenuItem{Text: label(c), TestID: "chat-" + id,
				Checked: func() bool { return a.agent.Get() == id }, OnSelect: func() { a.show(id) },
				Trailing: ui.IconButton{Icon: lucide.Trash, Label: tr.RemoveChat(), Size: 22, Tint: colors.Muted,
					OnPress: func() { a.remove(id) }, TestID: "remove-chat-" + id}})
		}
		items = append(items, ui.MenuSeparator{},
			ui.MenuItem{Text: tr.NewChat(), Icon: lucide.MessageSquarePlus, OnSelect: a.newChat, TestID: "chats-new"})
		name := ui.Textf(func() string { return label(a.m.Conversation(a.agent.Get())) }, 13, ui.Semibold, colors.Text)
		name.MaxLines = 1
		name.TestID = "chat-title"
		name.Style = name.Style.Merge(core.Style{FlexShrink: 1, MinWidth: core.Pt(0)})
		face := core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 4, MinWidth: core.Pt(0), FlexShrink: 1,
			Padding: core.Symmetric(3, 6), CornerRadius: ui.RadiusControl},
			StyleFunc: func() core.Style { return core.Style{Hover: &core.Style{Background: ui.Alpha(colors.Text(), .08)}} },
			Children:  []core.Node{name, ui.Icon(lucide.ChevronDown, 14, colors.Muted)}}
		return ui.Menu{Face: face, Label: tr.Chats(), Placement: core.PlaceBottom, Align: core.AlignStart, MaxHeight: 360,
			Children: items, TestID: "chats"}
	})
}
