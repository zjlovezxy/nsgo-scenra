package scenraview

import (
	"context"
	"github.com/oneo-me/nsgo-scenra/flowfile"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"github.com/nsgo-dev/nsgo/ui"
	agio "github.com/oneo-me/nsgo-agio"
	agentio "github.com/oneo-me/nsgo-agio/agent"
	"github.com/oneo-me/nsgo-agio/chatview"
	"github.com/oneo-me/nsgo-agio/host"
	"github.com/oneo-me/nsgo-agio/mcp"
)

// step is what a scripted agent does: write text to file, or, with view,
// ask the server what the user sees.
type step struct {
	file, text string
	view       bool
}

// reply is what a scripted agent does for a prompt: its steps, then the
// text it answers once hold, when set, closes.
type reply struct {
	steps []step
	hold  chan struct{}
	text  string
}

// scripted is a fake Claude Code answering each prompt with the next
// reply, keeping the sessions' options, the prompts and what the server
// told it.
type scripted struct {
	mu      sync.Mutex
	replies []reply
	started []agentio.StartOptions
	prompts []string
	views   []string
}

// script queues replies for the prompts to come.
func (d *scripted) script(replies ...reply) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.replies = append(d.replies, replies...)
}

func (d *scripted) get() (started []agentio.StartOptions, prompts, views []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.started), slices.Clone(d.prompts), slices.Clone(d.views)
}

func (d *scripted) Tool() agentio.Tool { return agentio.Claude }

func (d *scripted) Catalog(context.Context, string) (agentio.Catalog, error) {
	return agentio.Catalog{Model: "opus", Models: []agentio.Model{{Tool: agentio.Claude, ID: "opus", Name: "Opus"},
		{Tool: agentio.Claude, ID: "sonnet", Name: "Sonnet", Efforts: []string{"low", "high"}, DefaultEffort: "low"}}}, nil
}

func (d *scripted) Start(o agentio.StartOptions, onEvent func(agentio.Event), onExit func(error)) (agentio.Session, agentio.Info, error) {
	d.mu.Lock()
	d.started = append(d.started, o)
	cursor := o.Cursor
	if cursor == "" {
		cursor = "session-" + strconv.Itoa(len(d.started))
	}
	d.mu.Unlock()
	return &scriptedSession{d: d, o: o, emit: onEvent}, agentio.Info{Cursor: cursor, Settings: o.Settings}, nil
}

func (d *scripted) History(string, string) ([]agentio.Event, error) { return nil, nil }

type scriptedSession struct {
	d    *scripted
	o    agentio.StartOptions
	emit func(agentio.Event)
}

func (s *scriptedSession) Prompt(_ context.Context, p agentio.Prompt) error {
	d := s.d
	d.mu.Lock()
	d.prompts = append(d.prompts, p.Text)
	var r reply
	if len(d.replies) > 0 {
		r, d.replies = d.replies[0], d.replies[1:]
	}
	d.mu.Unlock()
	s.emit(agentio.Event{Type: agentio.Started})
	// The steps wait for the UI, so they run once Prompt returns.
	go func() {
		var server agentio.MCPServer
		for _, m := range s.o.MCPServers {
			if m.Name == ServerName {
				server = m
			}
		}
		for _, st := range r.steps {
			if !st.view {
				os.WriteFile(st.file, []byte(st.text), 0o644)
				continue
			}
			out, _, err := mcp.CallTool(context.Background(), server, ToolView, map[string]any{"tab": promptTab(s.o.SystemPrompt)})
			if err != nil {
				out = "error: " + err.Error()
			}
			d.mu.Lock()
			d.views = append(d.views, out)
			d.mu.Unlock()
		}
		if r.hold != nil {
			<-r.hold
		}
		s.emit(agentio.Event{Type: agentio.TextDone, Segment: "text", Text: r.text})
		s.emit(agentio.Event{Type: agentio.Settled})
	}()
	return nil
}

// promptTab is the tab a system prompt names, as an agent reads it.
func promptTab(prompt string) string {
	_, rest, _ := strings.Cut(prompt, `tab "`)
	tab, _, _ := strings.Cut(rest, `"`)
	return tab
}

func (s *scriptedSession) Abort(context.Context) error                       { return nil }
func (s *scriptedSession) Configure(context.Context, agentio.Settings) error { return nil }
func (s *scriptedSession) Close(time.Duration)                               {}

// askedSettings counts the times the test app's assistant opened the
// settings.
var askedSettings int

// startAssistant starts an app of the Window whose assistant's agent is a
// scripted one, which asks a Server what the user sees.
func startAssistant(t *testing.T, opts ...nsgotest.Option) (*nsgotest.App, *Editor, *scripted) {
	d := &scripted{}
	srv, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	h := host.New(host.Options{Env: []string{}, Tools: []agentio.Tool{agentio.Claude}, Drivers: map[agentio.Tool]agentio.Driver{agentio.Claude: d},
		Home: t.TempDir(), MCPServers: []agentio.MCPServer{srv.Agent()}})
	t.Cleanup(func() { h.Close() })
	var e *Editor
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		e = New(cx, Options{Agents: chatview.Bind(cx, h), OpenSettings: func() { askedSettings++ }, Remember: "editor"})
		srv.Attach(e)
		return core.View{Style: ui.FillRest, Children: []core.Node{Window{Editor: e}, ui.Toasts{Toaster: e.Toaster()}}}
	}, append([]nsgotest.Option{nsgotest.Size(1280, 800)}, opts...)...)
	app.WaitUntil(5*time.Second, func() bool { return h.State().Ready() })
	return app, e, d
}

// waitComposer waits for the assistant's composer.
func waitComposer(app *nsgotest.App) {
	app.WaitUntil(5*time.Second, func() bool { return len(app.Query("id:composer")) > 0 })
}

// send types text in the assistant's composer and sends it.
func send(app *nsgotest.App, text string) {
	waitComposer(app)
	app.Type("id:composer", text)
	app.Press("role:button Send")
}

func TestAssistantDrawsInTheFolder(t *testing.T) {
	app, e, d := startAssistant(t)
	app.Find("id:chat-open-folder")
	asked := askedSettings
	app.Press("id:open-settings")
	if askedSettings != asked+1 {
		t.Fatal("the assistant's settings button did nothing")
	}
	// A conversation is about a folder.
	dir := t.TempDir()
	openPath(t, app, e, dir)
	app.WaitFor("text:Let's design an app together", 5*time.Second)
	path := filepath.Join(dir, "orders.scenra")
	hold := make(chan struct{})
	d.script(reply{steps: []step{
		{view: true},
		{file: path, text: "flow\nnode order \"Order placed\" kind=start\n"},
		{view: true},
		{file: path, text: "flow\nnode order \"Order placed\" kind=start\nnode ship \"Ship order\"\norder -> ship\n"},
		{view: true},
	}, hold: hold, text: "I drew **two steps**."})
	send(app, "Chart an order flow")
	// A file the agent makes opens, and what it writes shows as it
	// writes it.
	app.WaitFor("flow >> label:Ship order", 5*time.Second)
	app.WaitUntil(5*time.Second, func() bool { _, _, views := d.get(); return len(views) == 3 })
	if _, _, views := d.get(); !strings.Contains(views[0], "no Scenra files yet") || !strings.Contains(views[2], "flow orders.scenra open") {
		t.Fatalf("views %q", views)
	}
	app.Find("id:files >> label:orders")
	// The user cannot change the files while the agent works.
	app.WaitUntil(5*time.Second, func() bool { return core.Untracked(e.cx, e.Working) })
	app.Post(e.Undo)
	app.Settle()
	app.Find("flow >> label:Ship order")
	if core.Untracked(e.cx, e.CanUndo) {
		t.Fatal("undo is offered while the agent works")
	}
	close(hold)
	app.WaitFor("text:two steps", 5*time.Second)
	// The agent works in the folder, told of it.
	started, _, _ := d.get()
	if len(started) != 1 || started[0].Dir != dir || !strings.Contains(started[0].SystemPrompt, dir) || len(started[0].MCPServers) != 1 {
		t.Fatalf("started %+v", started)
	}

	// What the turn wrote after the file opened undoes together.
	app.WaitUntil(5*time.Second, func() bool { return core.Untracked(e.cx, e.CanUndo) })
	app.Post(e.Undo)
	app.WaitForGone("flow >> label:Ship order", time.Second)

	// A new chat is another conversation, and the first stays a choice of
	// the header's menu.
	app.Press("id:new-chat")
	app.WaitFor("text:Let's design an app together", 5*time.Second)
	app.WaitUntil(5*time.Second, func() bool { return app.Find("id:chat-title").Text == "New Chat" })
	app.Press("id:chats")
	app.Press("text:Chart an order flow")
	app.WaitFor("text:two steps", 5*time.Second)
}

func TestConversationsResumeWithTheFolder(t *testing.T) {
	app, e, d := startAssistant(t)
	dir := t.TempDir()
	openPath(t, app, e, dir)
	d.script(reply{text: "Sure."}, reply{text: "Done."})
	send(app, "Sketch a sign-in screen")
	app.WaitFor("text:Sure.", 5*time.Second)
	app.Press("id:new-chat")
	app.WaitUntil(5*time.Second, func() bool { return app.Find("id:chat-title").Text == "New Chat" })
	send(app, "Sketch a menu")
	app.WaitFor("text:Done.", 5*time.Second)
	var stored string
	app.WaitUntil(5*time.Second, func() bool {
		stored, _ = app.Storage().GetItem("editor.chats")
		return strings.Contains(stored, "session-1") && strings.Contains(stored, "session-2")
	})

	// Another start resumes both, the latest shown.
	again, e2, d2 := startAssistant(t, nsgotest.Storage(map[string]string{keyFolder: dir, "editor.chats": stored}))
	again.WaitUntil(5*time.Second, func() bool {
		started, _, _ := d2.get()
		return len(again.Query("id:chat-title")) > 0 && again.Find("id:chat-title").Text == "Sketch a menu" && len(started) == 0
	})
	again.Press("id:chats")
	again.Find("text:Sketch a sign-in screen")
	// Deleting one forgets it and shows the other.
	again.Press("id:remove-chat-" + e2.a.agent.GetUntracked())
	again.WaitUntil(5*time.Second, func() bool {
		stored, _ := again.Storage().GetItem("editor.chats")
		return again.Find("id:chat-title").Text == "Sketch a sign-in screen" && !strings.Contains(stored, "session-2")
	})
}

func TestNewConversationsStartWithTheChoiceMadeLastInTheirFolder(t *testing.T) {
	app, e, d := startAssistant(t)
	dir := t.TempDir()
	openPath(t, app, e, dir)
	waitComposer(app)
	shown := func(a *assistant) *agio.Conversation {
		return a.m.Backend().State().Conversation(a.agent.GetUntracked())
	}
	model, effort := agentio.Key(agentio.Claude, "sonnet"), "high"
	app.Post(func() {
		id := e.a.agent.GetUntracked()
		e.a.m.Do(func(b agio.Backend) error { return b.Choose(id, agio.Choice{Model: &model, Effort: &effort}) }, nil)
	})
	d.script(reply{text: "Sure."})
	send(app, "Sketch a sign-in screen")
	app.WaitFor("text:Sure.", 5*time.Second)
	app.Press("id:new-chat")
	app.WaitUntil(5*time.Second, func() bool { c := shown(e.a); return c != nil && len(c.Turns) == 0 })
	if c := shown(e.a); c.Model != model || c.Effort != effort {
		t.Fatalf("the new conversation chose %q %q", c.Model, c.Effort)
	}
	// The choice is the folder's own.
	key := "editor.choice." + folderID(dir)
	stored, _ := app.Storage().GetItem(key)
	if stored == "" {
		t.Fatalf("the folder kept no choice under %s", key)
	}
	if other, _ := app.Storage().GetItem("editor.choice"); other != "" {
		t.Fatalf("the choice leaked to every folder: %s", other)
	}

	// Another start remembers it while it opens that folder, and another
	// folder starts with the model it had.
	again, e2, _ := startAssistant(t, nsgotest.Storage(map[string]string{key: stored}))
	openPath(t, again, e2, dir)
	again.WaitUntil(5*time.Second, func() bool { c := shown(e2.a); return c != nil && c.Model == model && c.Effort == effort })
	other := t.TempDir()
	openPath(t, again, e2, other)
	again.WaitUntil(5*time.Second, func() bool {
		c := shown(e2.a)
		return c != nil && c.Dir == other && c.Model != model
	})
}

func TestAScreenShowsOverTheComponentsMadeWithIt(t *testing.T) {
	app, e, d := startAssistant(t)
	dir := t.TempDir()
	openPath(t, app, e, dir)
	app.WaitFor("text:Let's design an app together", 5*time.Second)
	os.MkdirAll(filepath.Join(dir, "components"), 0o755)
	d.script(reply{steps: []step{
		{file: filepath.Join(dir, "components", "tab-bar.scenra"), text: "component\ntext \"Home tab\"\n"},
		{file: filepath.Join(dir, "about.scenra"), text: "design\nuse tab-bar\n"},
		{view: true},
	}, text: "Drew it."})
	send(app, "Design an about screen")
	app.WaitFor("text:Drew it.", 5*time.Second)
	app.WaitFor("design >> text:Home tab", 5*time.Second)
	if got := shownName(app); got != "about" {
		t.Fatalf("the header names %q", got)
	}
}

func TestSendingSavesWhatTheUserChanged(t *testing.T) {
	path := writeFlow(t, "shop.scenra", "design\nbox class=\"flex-1 gap-4 p-6 bg-nope\"\n  text \"Welcome\"\nend\n")
	app, e, d := startAssistant(t)
	openPath(t, app, e, path)
	app.WaitFor("design >> text:Welcome", time.Second)
	app.Press("id:element-0/0.0")
	app.WaitFor("id:comment-card", time.Second)
	app.Type("id:comment-input", "Friendlier")
	app.Press("id:comment-save")
	app.WaitFor("id:comment-pin-0/0.0", time.Second)
	if data, _ := os.ReadFile(path); strings.Contains(string(data), "Friendlier") {
		t.Fatal("the comment was saved before a message was sent")
	}
	d.script(reply{steps: []step{{view: true}}, text: "Seen."})
	send(app, "What do you see?")
	app.WaitFor("text:Seen.", 5*time.Second)
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `comment="Friendlier"`) {
		t.Fatalf("sending did not save the comment:\n%s", data)
	}
	if core.Untracked(e.cx, e.Edited) {
		t.Fatal("the document still has unsaved changes")
	}
	// The agent hears of the selection, the comment and what draws nothing.
	_, _, views := d.get()
	for _, want := range []string{"design shop.scenra open", "selected element 0.0", `shop.scenra, element 0.0: "Friendlier"`, "shop.scenra: bg-nope"} {
		if !strings.Contains(views[0], want) {
			t.Fatalf("the view tells nothing of %q:\n%s", want, views[0])
		}
	}
}

func TestAgentsHearOfFilesThatDoNotParse(t *testing.T) {
	path := writeFlow(t, "plan.scenra", "flow\nnode a \"Research\"\n")
	other := filepath.Join(filepath.Dir(path), "other.scenra")
	app, e, d := startAssistant(t)
	openPath(t, app, e, path)
	app.WaitFor("flow >> label:Research", time.Second)
	d.script(reply{steps: []step{{file: path, text: "flow\nnode a \"Research\nscreen\n"}, {file: other, text: "box\n"}, {view: true}}, text: "Oops."})
	send(app, "Break it")
	app.WaitFor("text:Oops.", 5*time.Second)
	if _, _, views := d.get(); !strings.Contains(views[0], "do not parse") || !strings.Contains(views[0], "plan.scenra: line 2") || !strings.Contains(views[0], "other.scenra: line 1") {
		t.Fatalf("views %q", views)
	}
	// The editor keeps the last version that parsed.
	app.Find("flow >> label:Research")
}

func TestAFileThatDoesNotParseShowsWhyAndTheAssistantFixesIt(t *testing.T) {
	path := writeFlow(t, "home.scenra", "design\nbox\n")
	app, e, d := startAssistant(t)
	openPath(t, app, e, path)
	app.WaitFor("id:file-broken", time.Second)
	if got := app.Find("id:file-problem").Text; !strings.Contains(got, "line 2") {
		t.Fatalf("the problem shown is %q", got)
	}
	if got := shownName(app); got != "home" {
		t.Fatalf("the header names %q", got)
	}
	// Saving leaves the file as it is.
	app.Post(e.Save)
	app.Settle()
	if data, _ := os.ReadFile(path); string(data) != "design\nbox\n" {
		t.Fatalf("saving wrote the file:\n%s", data)
	}

	d.script(reply{steps: []step{{view: true}, {file: path, text: "design\nbox\n  text \"Fixed\"\nend\n"}}, text: "Fixed it."})
	waitComposer(app)
	app.Press("id:fix-file")
	app.WaitFor("text:Fixed it.", 5*time.Second)
	if _, prompts, _ := d.get(); len(prompts) != 1 || !strings.HasPrefix(prompts[0], "Fix home.scenra, which Scenra cannot open: line 2") {
		t.Fatalf("prompts %q", prompts)
	}
	if _, _, views := d.get(); !strings.Contains(views[0], "home.scenra open, which has never parsed") {
		t.Fatalf("views %q", views)
	}
	// The fixed file shows, with nothing to undo back to.
	app.WaitFor("design >> text:Fixed", 5*time.Second)
	app.WaitForGone("id:file-broken", time.Second)
	if core.Untracked(e.cx, e.CanUndo) {
		t.Fatal("undo is offered back to the broken file")
	}
}

func TestCommentsAskTheAssistant(t *testing.T) {
	path := writeFlow(t, "shop.scenra", "design\nbox class=\"flex-1 gap-4 p-6\"\n  text \"Welcome\"\n  text \"Mail\" comment=\"Smaller\"\nend\n")
	app, e, d := startAssistant(t)
	openPath(t, app, e, path)
	app.WaitFor("design >> text:Welcome", time.Second)
	app.Find("id:comment-pin-0/0.1")
	app.Find("id:resolve-comments")
	if app.Exists("id:comment-card") {
		t.Fatal("a comment card shows with nothing selected")
	}

	// Commenting on the selected element saves the comment in the file.
	app.Press("id:element-0/0.0")
	app.WaitFor("id:comment-card", time.Second)
	app.Type("id:comment-input", "Friendlier")
	app.Press("id:comment-save")
	app.WaitFor("id:comment-pin-0/0.0", time.Second)
	if got := e.shown().doc.GetUntracked().Format(); !strings.Contains(got, `text "Welcome" comment="Friendlier"`) {
		t.Fatalf("the comment was not saved:\n%s", got)
	}
	app.Press("id:comment-remove")
	app.WaitForGone("id:comment-pin-0/0.0", time.Second)
	app.Type("id:comment-input", "Friendlier")

	// Asking the assistant sends it the comment, which it resolves.
	d.script(reply{steps: []step{{file: path,
		text: "design\nbox class=\"flex-1 gap-4 p-6\"\n  text \"Hello there\" class=\"font-bold\"\n  text \"Mail\" comment=\"Smaller\"\nend\n"}},
		text: "Made it friendlier."})
	waitComposer(app)
	app.Press("id:comment-ask")
	app.WaitFor("text:Made it friendlier.", 5*time.Second)
	if _, prompts, _ := d.get(); len(prompts) != 1 || prompts[0] != "Resolve my comment on element 0.0: Friendlier" {
		t.Fatalf("prompts %q", prompts)
	}
	app.WaitFor("design >> text:Hello there", time.Second)
	app.WaitForGone("id:comment-pin-0/0.0", time.Second)
	app.Find("id:comment-pin-0/0.1")
}

func TestServerTellsOfTheFolder(t *testing.T) {
	srv, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	var tab string
	ask := func() (string, bool) {
		out, failed, err := mcp.CallTool(context.Background(), srv.Agent(), ToolView, map[string]any{"tab": tab})
		if err != nil {
			t.Fatal(err)
		}
		return out, failed
	}
	if out, failed := ask(); !failed || !strings.Contains(out, "no Scenra editor") {
		t.Fatalf("a call without an editor: %q", out)
	}
	dir := folderOfFiles(t, map[string]string{"plan.scenra": "design\ntext \"x\" class=\"bg-nope\"\nuse card\nuse gone\n" +
		"image \"Cup\" src=\"assets/cup.png\"\nimage \"Lost\" src=\"assets/lost.jpg\"\nimage \"Stray\" src=\"a/stray.png\"\n",
		"components/card.scenra": "component\nuse card\n", "stray.scenra": "component\n", "a/nested.scenra": "design\n", "deep.scenra": "design name=\"A/B/C\"\n",
		"loose-flow.scenra": "flow\n", "flows/screen.scenra": "design\n", "flows/pay.scenra": "flow name=\"A/B/Pay\"\n",
		"hosted.scenra": "design in=\"plan#content\"\n", "views/lonely.scenra": "design\n", "views/deep.scenra": "design\n",
		"assets/cup.png": "cup", "a/stray.png": "stray"})
	var e *Editor
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		e = New(cx, Options{})
		srv.Attach(e)
		return core.View{}
	})
	// The app answers while the test drives it.
	answer := func() (out string, failed bool) {
		done := make(chan struct{})
		go func() { out, failed = ask(); close(done) }()
		app.WaitUntil(5*time.Second, func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		})
		return out, failed
	}
	if out, failed := answer(); !failed || !strings.Contains(out, "give tab") {
		t.Fatalf("a call naming no tab: %q", out)
	}
	openPath(t, app, e, dir)
	tab = "0000"
	if out, failed := answer(); !failed || !strings.Contains(out, "no Scenra tab 0000") {
		t.Fatalf("a call naming another tab: %q", out)
	}
	tab = e.ID()
	out, failed := answer()
	for _, want := range []string{"no file open", "plan.scenra: bg-nope", "draw with use <name>: card.",
		`plan.scenra: use "card": components use each other`, `plan.scenra: use "gone": there is no components/gone.scenra`,
		`components/card.scenra: use "card": components use each other`, "stray.scenra: a component outside components/",
		"which the editor does not show", "- a/nested.scenra\n", `deep.scenra: name="A/B/C"`,
		`plan.scenra: src "assets/lost.jpg": there is no assets/lost.jpg`,
		`plan.scenra: src "a/stray.png": put the picture in assets/`, "which the editor lists with the wrong kind",
		"- loose-flow.scenra\n", "- flows/screen.scenra\n",
		"so the top lists only those:\n- hosted.scenra\n- views/deep.scenra\n- views/lonely.scenra\n",
		"which taps and in= never reach, as they name the one at the top; rename one:\n- views/deep.scenra\n"} {
		if failed || !strings.Contains(out, want) {
			t.Fatalf("the view tells nothing of %q: %s", want, out)
		}
	}
	// A flow's name has no groups to nest.
	if strings.Contains(out, "flows/pay.scenra") {
		t.Fatalf("the view tells of a flow in its place: %s", out)
	}
}

func TestTheViewNamesTheDesignsOfAFile(t *testing.T) {
	f, err := flowfile.Parse("design sign-in\ntext \"In\" tap=\"open #reset\" comment=\"Warmer\"\n\ndesign sign-up\ntext \"Up\" tap=\"open home#top\" note=\"Up there\"\nuse tag\n")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := flowfile.Parse("design top\ntext \"Home\" tap=\"open auth#sign-up\" note=\"Down here\"\n")
	tag, _ := flowfile.Parse("component\ntext \"Tag\" note=\"Tagged\"\n")
	pill, _ := flowfile.Parse("component\ntext \"Pill\" note=\"Unused\"\n")
	flows, _ := flowfile.Parse("flow pay\nnode a\n\nflow refund\nnode b\n")
	out := view("/p", "/p", []fileView{{path: "auth.scenra", file: f, shown: &location{element: "1/0"}}, {path: "home.scenra", file: home},
		{path: "flows/pay.scenra", file: flows}}, nil, nil, NotesAll)
	for _, want := range []string{"design auth.scenra open, which holds the designs sign-in, sign-up.", "selected element 0 of design sign-up:",
		`auth.scenra, design sign-in, element 0: "Warmer"`, `auth.scenra: tap "open #reset": auth has no design reset`} {
		if !strings.Contains(out, want) {
			t.Errorf("the view tells nothing of %q:\n%s", want, out)
		}
	}
	// Notes may be left out, problems never.
	files := []fileView{{path: "auth.scenra", file: f, shown: &location{}}, {path: "home.scenra", file: home},
		{path: "components/tag.scenra", file: tag}, {path: "components/pill.scenra", file: pill}}
	parts := components{"tag": {UI: tag.Screens[0].UI}, "pill": {UI: pill.Screens[0].UI}}
	for notes, want := range map[string][]string{NotesAll: {"Up there", "Down here", "Tagged", "Unused"}, NotesOpen: {"Up there", "Tagged"}, NotesNone: nil} {
		out := view("/p", "/p", files, parts, nil, notes)
		for _, note := range []string{"Up there", "Down here", "Tagged", "Unused"} {
			if strings.Contains(out, note) != slices.Contains(want, note) {
				t.Errorf("notes=%s tells of %q wrongly:\n%s", notes, note, out)
			}
		}
		if !strings.Contains(out, "has no design reset") {
			t.Errorf("notes=%s drops a problem:\n%s", notes, out)
		}
	}
	if _, taps, _ := strings.Cut(out, "Taps that lead nowhere:"); strings.Contains(taps, "home#top") || strings.Contains(taps, "auth#sign-up") {
		t.Errorf("taps that lead somewhere are reported:\n%s", out)
	}
	out = view("/p", "/p", []fileView{{path: "flows/pay.scenra", file: flows, shown: &location{node: "1/b"}}}, nil, nil, NotesAll)
	if !strings.Contains(out, `selected node "b" of flow refund`) || !strings.Contains(out, "which holds the flows pay, refund") {
		t.Errorf("the view of flows:\n%s", out)
	}
}

func TestTheServerTellsEachTabOfItsOwnFolder(t *testing.T) {
	srv, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	dirs := []string{t.TempDir(), t.TempDir()}
	var editors []*Editor
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		for _, dir := range dirs {
			e := New(cx, Options{Folder: dir})
			srv.Attach(e)
			editors = append(editors, e)
		}
		return core.View{}
	})
	for i, e := range editors {
		var out string
		done := make(chan struct{})
		go func() {
			out, _, _ = mcp.CallTool(context.Background(), srv.Agent(), ToolView, map[string]any{"tab": e.ID()})
			close(done)
		}()
		app.WaitUntil(5*time.Second, func() bool {
			select {
			case <-done:
				return true
			default:
				return false
			}
		})
		if want := "The open folder is " + dirs[i] + ";"; !strings.Contains(out, want) {
			t.Fatalf("tab %d tells of another folder: %s", i, out)
		}
	}
}
