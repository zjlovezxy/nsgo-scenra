package scenraview

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/nsgo-dev/nsgo/base/camera"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
	uiflow "github.com/nsgo-dev/nsgo/ui/flow"
	"github.com/oneo-me/nsgo-agio/chatview"

	"github.com/oneo-me/nsgo-scenra/flowfile"
	"github.com/oneo-me/nsgo-scenra/scenraview/locales"
)

// historyLimit is how many changes undo keeps.
const historyLimit = 200

// Options configure an Editor.
type Options struct {
	// Agents, when set, runs the assistant: a conversation about the open
	// folder, working in it, whose agent edits its files as the app's
	// skill teaches and asks the app's Server what the user sees. Without
	// it the editor has no assistant.
	Agents *chatview.Model
	// OpenSettings, when set, shows where the app sets up the agents,
	// which the assistant's header offers.
	OpenSettings func()
	// Toaster shows what failed to open or save, and what was saved; one
	// of the editor's own when nil, which Toaster returns for the app to
	// show.
	Toaster *ui.Toaster
	// Remember, when set, prefixes the storage keys under which the open
	// folder and the file shown open again at the next start, and those
	// keeping the folders' conversations and how components draw, which
	// editors given the same prefix share.
	Remember string
	// Folder, when set, is the folder the editor opens, in place of the
	// one Remember kept, as an app showing an editor per folder, such as
	// in tabs, gives each its own and keeps them itself.
	Folder string
	// OnSend, when set, hears of each message sent to the assistant.
	OnSend func()
	// NoView, when set, says the agents run elsewhere and cannot ask the
	// Server what the user sees, so their instructions don't tell them to.
	NoView bool
}

// document is the Scenra file shown: its content and path, its history
// and what the user selected in it.
type document struct {
	e   *Editor
	doc *core.Signal[flowfile.File]
	// path is the file the document came from, which saving writes
	// again; dirty says it changed since.
	path  string
	dirty *core.Signal[bool]
	// selected is the node of a flow the user or the assistant selected,
	// and element the path of a design's selected element.
	selected, element *core.Signal[string]
	// undos and redos are documents before and after changes.
	undos, redos *core.Signal[[]flowfile.File]
	// group is the key of the last change, whose undo later changes with
	// the same key join.
	group string
	// stamp is the version of the file the document was last read from
	// or written to, and broken why the file's latest version does not
	// parse, "" when it does. A document whose file never parsed holds
	// no file, which the workspace shows as broken.
	stamp  stamp
	broken *core.Signal[string]
}

// opened is a design a tap opened or opened from: its file's path and
// its ID, "" for the file's first.
type opened struct{ path, id string }

// focusing asks the canvas to show the design with id of the file shown,
// the whole file for ""; each request counts anew.
type focusing struct {
	id string
	n  int
}

// Editor is a folder of Scenra files and the one shown for editing, with
// an assistant that works in the folder. Showing another file saves the
// one shown. The views show an editor; an app showing one file opens just
// it and leaves out the Header.
type Editor struct {
	cx   *core.Scope
	opts Options
	// folder is the open folder, "" before one is, which the assistant
	// works in, and root the project in it, the folder or its design
	// folder; files are the Scenra files found in root, parts the
	// components among them, designs the files of designs, by name, and
	// assets the pictures of its assets folder; changes are what Git says
	// changed in its files.
	folder  *core.Signal[string]
	root    *core.Signal[string]
	files   *core.Signal[[]entry]
	parts   *core.Signal[components]
	designs *core.Signal[designs]
	assets  *core.Signal[assets]
	changes *core.Signal[changes]
	active  *core.Signal[*document]
	// blank stands for the document shown while none is open.
	blank *document
	// showcase says the folder's components show together in place of
	// the document, which stays open beneath them.
	showcase *core.Signal[bool]
	// camera shows the flows of the file shown, and charts their
	// diagrams' states by index.
	camera *camera.State
	charts []*uiflow.State
	toasts *ui.Toaster
	// flowCanvas, designCanvas and showcaseCanvas are the cameras of the
	// panes mounted, which zooming moves on the one on top.
	flowCanvas, designCanvas, showcaseCanvas *paneCamera
	// trail is the designs whose taps opened the designs shown since,
	// which closing one returns through, and focus the design of the file
	// shown a tap opened, which the canvas moves to.
	trail []opened
	focus *core.Signal[focusing]
	// reloads counts the times the file shown was read again as it
	// changed, which shows its taps again.
	reloads *core.Signal[int]
	// panelsHidden hides the Window's sidebar and assistant, leaving the
	// workspace the window's width.
	panelsHidden *core.Signal[bool]
	// outlinesHidden hides the dashed line marking a component's bounds,
	// kept under the Remember prefix.
	outlinesHidden *core.Signal[bool]
	a              *assistant
	// probe is what the unseen layer of the Workspace lays out, probes
	// those waiting for it, and probing counts the layers mounted.
	probe   *core.Signal[*probe]
	probes  []*probe
	probing int
	// scanning is the folder the scan reads, shared with its goroutine,
	// and rescan asks it to read the folder now.
	mu       sync.Mutex
	scanning string
	rescan   chan struct{}
}

// New is an editor with no folder open, or with the one opts.Remember
// kept.
func New(cx *core.Scope, opts Options) *Editor {
	toasts := opts.Toaster
	if toasts == nil {
		toasts = ui.NewToaster(cx)
	}
	e := &Editor{cx: cx, opts: opts, folder: core.NewSignal(cx, ""), root: core.NewSignal(cx, ""), files: core.NewSignalFunc(cx, []entry(nil), slices.Equal[[]entry]),
		parts: core.NewSignalFunc(cx, components(nil), sameComponents), designs: core.NewSignalFunc(cx, designs(nil), sameDesigns), assets: core.NewSignalFunc(cx, assets(nil), sameAssets),
		changes: core.NewSignalFunc(cx, changes(nil), sameChanges),
		active:  core.NewSignal[*document](cx, nil),
		camera:  &camera.State{}, toasts: toasts, reloads: core.NewSignal(cx, 0), panelsHidden: core.NewSignal(cx, false), outlinesHidden: core.NewSignal(cx, false), rescan: make(chan struct{}, 1),
		focus: core.NewSignal(cx, focusing{}), showcase: core.NewSignal(cx, false), probe: core.NewSignal[*probe](cx, nil)}
	e.blank = e.newDocument(flowfile.File{}, "")
	e.a = newAssistant(cx, e)
	e.watch()
	// Another flow shows fitted, once the batch that opened it is over.
	cx.Effect(func() {
		e.cur()
		cx.Post(e.camera.Fit)
	})
	if opts.Remember != "" {
		if opts.Folder == "" {
			e.remember(opts.Remember+".folder", opts.Remember+".file")
		}
		e.rememberOutlines(opts.Remember + ".outlines")
	}
	if opts.Folder != "" {
		e.setFolder(opts.Folder)
	}
	return e
}

// ID names the editor to agents, which the Server's tools take to tell
// editors apart: its folder's, so a conversation resumed later names it
// alike; "" while no folder is open. Any goroutine may call it.
func (e *Editor) ID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return folderID(e.scanning)
}

// folderID is the ID of an editor of the folder dir.
func folderID(dir string) string {
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(sum[:4])
}

// remember opens the folder and the file shown when the app last quit,
// under the storage keys folder and file, and keeps them there.
func (e *Editor) remember(folder, file string) {
	cx := e.cx
	e.restore(folder, file)
	cx.Effect(func() {
		dir, shown := e.folder.Get(), e.cur().path
		core.Untracked(cx, func() bool {
			st := cx.Storage()
			st.SetItem(folder, dir)
			st.SetItem(file, shown)
			return true
		})
	})
}

// rememberOutlines hides the components' outlines when the storage keeps
// "off" under key, and keeps it there while they are hidden.
func (e *Editor) rememberOutlines(key string) {
	cx := e.cx
	v, _ := cx.Storage().GetItem(key)
	e.outlinesHidden.Set(v == "off")
	cx.Effect(func() {
		hidden := e.outlinesHidden.Get()
		core.Untracked(cx, func() bool {
			if hidden {
				cx.Storage().SetItem(key, "off")
			} else {
				cx.Storage().RemoveItem(key)
			}
			return true
		})
	})
}

// Toaster shows the editor's messages.
func (e *Editor) Toaster() *ui.Toaster { return e.toasts }

// restore opens the folder the storage keeps under folderKey, and shows
// the file under fileKey unless it is gone.
func (e *Editor) restore(folderKey, fileKey string) {
	st := e.cx.Storage()
	dir, _ := st.GetItem(folderKey)
	if info, err := os.Stat(dir); dir == "" || err != nil || !info.IsDir() {
		return
	}
	e.setFolder(dir)
	path, _ := st.GetItem(fileKey)
	if path == "" || !e.inside(path) {
		return
	}
	if d, err := e.load(path); err == nil {
		e.active.Set(d)
	}
}

// readFile reads the Scenra file at path.
func readFile(path string) (flowfile.File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return flowfile.File{}, err
	}
	return flowfile.Parse(string(data))
}

// load is a document of the Scenra file at path, broken when the file
// does not parse, so the user can have it fixed; it fails only when the
// file cannot be read.
func (e *Editor) load(path string) (*document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := flowfile.Parse(string(data))
	d := e.newDocument(f, path)
	if err != nil {
		d.doc.Set(flowfile.File{})
		d.broken.Set(err.Error())
	}
	return d, nil
}

// newDocument is a document of f, from the file at path.
func (e *Editor) newDocument(f flowfile.File, path string) *document {
	cx := e.cx
	never := func(a, b flowfile.File) bool { return false }
	stacks := func(a, b []flowfile.File) bool { return false }
	d := &document{e: e, doc: core.NewSignalFunc(cx, f, never), path: path, dirty: core.NewSignal(cx, false),
		selected: core.NewSignal(cx, ""), element: core.NewSignal(cx, ""), broken: core.NewSignal(cx, ""),
		undos: core.NewSignalFunc(cx, []flowfile.File(nil), stacks), redos: core.NewSignalFunc(cx, []flowfile.File(nil), stacks)}
	d.mark()
	return d
}

// cur is the document shown, blank while none is open, which a caller
// reading it follows.
func (e *Editor) cur() *document {
	if d := e.active.Get(); d != nil {
		return d
	}
	return e.blank
}

// shown is the document shown, for actions.
func (e *Editor) shown() *document { return core.Untracked(e.cx, e.cur) }

// empty says no document is open.
func (e *Editor) empty() bool { return e.active.Get() == nil }

// show shows d in place of the document shown, whole.
func (e *Editor) show(d *document) {
	e.cx.Batch(func() {
		e.active.Set(d)
		e.showcase.Set(false)
		e.focusOn("")
	})
	e.cx.Post(e.camera.Fit)
}

// focusOn has the canvas show the design with id of the file shown, or
// the whole file for "".
func (e *Editor) focusOn(id string) {
	e.focus.Set(focusing{id, e.focus.GetUntracked().n + 1})
}

// inside says path is in the open project.
func (e *Editor) inside(path string) bool {
	dir := e.root.GetUntracked()
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// rel is path relative to the open project, with slashes.
func (e *Editor) rel(path string) string {
	rel, err := filepath.Rel(e.root.GetUntracked(), path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// setFolder opens the folder at dir, whose project's files the scan
// finds.
func (e *Editor) setFolder(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	e.mu.Lock()
	e.scanning = dir
	e.mu.Unlock()
	e.cx.Batch(func() {
		e.folder.Set(dir)
		e.root.Set(projectDir(dir))
		e.files.Set(nil)
		e.parts.Set(nil)
		e.designs.Set(nil)
		e.assets.Set(nil)
		e.changes.Set(nil)
	})
	e.scanNow()
}

// openFolder opens the folder at dir in place of the open one, once the
// changes of the document shown are saved; the open folder stays when
// saving fails.
func (e *Editor) openFolder(dir string) bool {
	if abs, err := filepath.Abs(dir); err == nil && abs == e.folder.GetUntracked() {
		return true
	}
	if !e.SaveChanges() {
		return false
	}
	e.trail = nil
	e.cx.Batch(func() {
		e.active.Set(nil)
		e.showcase.Set(false)
		e.setFolder(dir)
	})
	return true
}

// OpenFolder asks for a folder and opens it.
func (e *Editor) OpenFolder() {
	e.cx.PickDirectories(core.PickDirectoriesOptions{}, func(dirs []core.Directory, err error) {
		switch {
		case err != nil:
			e.toasts.Show(ui.ToastContent{Type: ui.ToastError, Title: locales.For(e.cx).OpenFailed(err.Error())})
		case len(dirs) > 0 && dirs[0].Path != "":
			e.openFolder(dirs[0].Path)
		}
	})
}

// ProjectOf is the folder an editor opens for the Scenra file at path,
// that of its project: the one holding it, or that folder's parent when
// it is the project's flows, views or components folder.
func ProjectOf(path string) string {
	dir := filepath.Dir(path)
	if slices.Contains([]string{flowsDir, viewsDir, flowfile.ComponentsDir}, filepath.Base(dir)) {
		return filepath.Dir(dir)
	}
	return dir
}

// OpenPath opens the folder at path, or shows the Scenra file at path,
// opening the project holding it first when it is not in the open one.
func (e *Editor) OpenPath(path string) error {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		e.openFolder(path)
		return nil
	}
	d, err := e.load(path)
	if err != nil {
		return err
	}
	if e.inside(path) || e.openFolder(ProjectOf(path)) {
		e.trail = nil
		e.openDoc(d)
	}
	return nil
}

// open shows the file at path, which the sidebar chose.
func (e *Editor) open(path string) {
	if e.shown().path == path {
		e.showcase.Set(false)
		return
	}
	e.trail = nil
	d, err := e.load(path)
	if err != nil {
		e.toasts.Show(ui.ToastContent{Type: ui.ToastError, Title: locales.For(e.cx).OpenFailed(err.Error())})
		return
	}
	e.openDoc(d)
}

// openDoc shows d in place of the document shown, once its changes are
// saved; the document shown stays when saving fails.
func (e *Editor) openDoc(d *document) {
	if e.shown().path == d.path || !e.SaveChanges() {
		return
	}
	e.show(d)
}

// designName is the name of the file of designs at path, which taps open
// its designs by: its file name without .scenra, at the top or in the
// views folder.
func (e *Editor) designName(path string) string {
	return nameOf(e.rel(path))
}

// designFile is the path of the file of designs named name, relative to
// the folder: the one at the top, or else the view of that name.
func (e *Editor) designFile(name string) string {
	files := e.files.GetUntracked()
	has := func(p string) bool { return slices.ContainsFunc(files, func(f entry) bool { return f.path == p }) }
	if top := flowfile.DesignPath(name); has(top) || !has(viewPath(name)) {
		return top
	}
	return viewPath(name)
}

// tapOpen shows the design t opens, as a tap in the design at index
// screen of the file shown opens it, which a close returns from: it
// moves to a design of the same file, and opens another's file.
func (e *Editor) tapOpen(t flowfile.Tap, screen int) {
	tr := locales.For(e.cx)
	shown := e.shown()
	f := shown.doc.GetUntracked()
	from := opened{path: shown.path}
	if screen < len(f.Screens) {
		from.id = f.Screens[screen].ID
	}
	name, id := t.Opens()
	if name == "" || name == e.designName(shown.path) {
		if _, ok := f.Screen(id); !ok {
			e.toasts.Show(ui.ToastContent{Type: ui.ToastError, Title: tr.NoDesign(t.Target)})
			return
		}
		e.trail = append(e.trail, from)
		e.focusOn(id)
		return
	}
	path := filepath.Join(e.root.GetUntracked(), filepath.FromSlash(e.designFile(name)))
	d, err := e.load(path)
	if err == nil && d.broken.GetUntracked() == "" {
		if _, ok := d.doc.GetUntracked().Screen(id); !ok {
			err = os.ErrNotExist
		}
	}
	switch {
	case err != nil && errors.Is(err, os.ErrNotExist):
		e.toasts.Show(ui.ToastContent{Type: ui.ToastError, Title: tr.NoDesign(t.Target)})
		return
	case err != nil:
		e.toasts.Show(ui.ToastContent{Type: ui.ToastError, Title: tr.OpenFailed(err.Error())})
		return
	case d.broken.GetUntracked() == "" && d.doc.GetUntracked().Kind == flowfile.Chart:
		e.toasts.Show(ui.ToastContent{Type: ui.ToastError, Title: tr.NoDesign(t.Target)})
		return
	}
	e.openDoc(d)
	if e.shown().path == path {
		e.trail = append(e.trail, from)
		e.focusOn(id)
	}
}

// tapClose shows the design whose tap opened the design shown, as a tap
// closing it does; it does nothing when the user opened the design shown.
func (e *Editor) tapClose() {
	n := len(e.trail)
	if n == 0 {
		return
	}
	back := e.trail[n-1]
	if back.path == e.shown().path {
		e.trail = e.trail[:n-1]
		e.focusOn(back.id)
		return
	}
	d, err := e.load(back.path)
	if err != nil {
		e.trail = nil
		e.toasts.Show(ui.ToastContent{Type: ui.ToastError, Title: locales.For(e.cx).OpenFailed(err.Error())})
		return
	}
	e.openDoc(d)
	if e.shown().path == back.path {
		e.trail = e.trail[:n-1]
		e.focusOn(back.id)
	}
}

// name is the document's file name.
func (d *document) name() string {
	if d.path == "" {
		return ""
	}
	return filepath.Base(d.path)
}

// unread says the document's file never parsed, so it holds nothing to
// show, edit or save; a caller reading it follows.
func (d *document) unread() bool { return d.broken.Get() != "" && d.doc.Get().Kind == "" }

// at is what the user selected.
func (d *document) at() location {
	return location{node: d.selected.GetUntracked(), element: d.element.GetUntracked()}
}

// show selects the node of l, and the element, dropping what is gone.
func (d *document) show(l location) {
	f := d.doc.GetUntracked()
	if _, ok := nodeOf(f, l.node); !ok {
		l.node = ""
	}
	if _, ok := elementOf(f, l.element); !ok {
		l.element = ""
	}
	d.e.cx.Batch(func() {
		d.selected.Set(l.node)
		d.element.Set(l.element)
	})
	if l.node != "" && d.e.shown() == d {
		d.e.cx.Post(func() { d.e.revealNode(l.node) })
	}
}

// change replaces the document with an edit of it, which undo reverts.
// Consecutive changes of one nonempty group, such as an assistant's
// turn, undo together.
func (d *document) change(f flowfile.File, group string) {
	before := d.doc.GetUntracked()
	joined := group != "" && group == d.group && len(d.undos.GetUntracked()) > 0
	d.group = group
	d.e.cx.Batch(func() {
		if !joined {
			undos := append(d.undos.GetUntracked(), before)
			if len(undos) > historyLimit {
				undos = undos[len(undos)-historyLimit:]
			}
			d.undos.Set(undos)
		}
		d.redos.Set(nil)
		d.doc.Set(f)
		d.dirty.Set(true)
		d.show(d.at())
	})
}

// setComment sets the comment on the element at key of the designs, or
// removes it when text is "", as a change undo reverts.
func (d *document) setComment(key, text string) { d.annotate(key, map[string]string{"comment": text}) }

// setNote sets the note on the element at key of the designs, or
// removes it when text is "", as a change undo reverts.
func (d *document) setNote(key, text string) { d.annotate(key, map[string]string{"note": text}) }

// annotate sets the attributes of the element at key of the designs to
// values, removing those set to "", as one change undo reverts; it
// changes nothing when they have those values.
func (d *document) annotate(key string, values map[string]string) {
	screen, path, ok := splitKey(key)
	f := d.doc.GetUntracked().Clone()
	if d.e.locked() || !ok || screen >= len(f.Screens) {
		return
	}
	els, changed := f.Screens[screen].UI, false
	for _, key := range slices.Sorted(maps.Keys(values)) {
		el, ok := flowfile.ElementAt(els, path)
		if !ok {
			return
		}
		if el.Attr(key) == values[key] {
			continue
		}
		var err error
		if els, err = flowfile.SetAttr(els, path, key, values[key]); err != nil {
			return
		}
		changed = true
	}
	if !changed {
		return
	}
	f.Screens[screen].UI = els
	d.change(f, "")
}

func (d *document) undo() { d.step(d.undos, d.redos) }
func (d *document) redo() { d.step(d.redos, d.undos) }

// step moves the document from one history stack to the other.
func (d *document) step(from, to *core.Signal[[]flowfile.File]) {
	list := from.GetUntracked()
	if len(list) == 0 || d.e.locked() {
		return
	}
	d.group = ""
	d.e.cx.Batch(func() {
		to.Set(append(to.GetUntracked(), d.doc.GetUntracked()))
		from.Set(list[: len(list)-1 : len(list)-1])
		d.doc.Set(list[len(list)-1])
		d.dirty.Set(true)
		d.show(d.at())
	})
}

// Undo and Redo step through the history of the document shown.
func (e *Editor) Undo() { e.shown().undo() }
func (e *Editor) Redo() { e.shown().redo() }

// SaveChanges saves the changes of the document shown, and says none
// are left unsaved.
func (e *Editor) SaveChanges() bool {
	d := e.shown()
	return !d.dirty.GetUntracked() || e.save(d)
}

// Save writes the document shown to its file.
func (e *Editor) Save() {
	if !e.empty() && !core.Untracked(e.cx, e.shown().unread) && e.save(e.shown()) {
		e.toasts.Show(ui.ToastContent{Type: ui.ToastSuccess, Title: locales.For(e.cx).Saved()})
	}
}

// save writes d to its file, and says it did. A document whose file
// never parsed leaves the file as it is.
func (e *Editor) save(d *document) bool {
	if core.Untracked(e.cx, d.unread) {
		return true
	}
	if err := writeFile(d.path, d.doc.GetUntracked().Format()); err != nil {
		e.toasts.Show(ui.ToastContent{Type: ui.ToastError, Title: locales.For(e.cx).SaveFailed(err.Error())})
		return false
	}
	d.mark()
	d.dirty.Set(false)
	return true
}

// writeFile replaces the file at path with text, leaving the old one
// whole if writing fails.
func writeFile(path, text string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".scenra-*")
	if err != nil {
		return err
	}
	_, err = tmp.WriteString(text)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// The editor's state, which a caller reading it follows, such as to name
// the window or turn menu items on and off.

// Folder is the open folder, "" before one is.
func (e *Editor) Folder() string { return e.folder.Get() }

// Empty says no file is open.
func (e *Editor) Empty() bool { return e.empty() }

// Name is the file name of the document shown, "" while none is, and
// Path its path.
func (e *Editor) Name() string { return e.cur().name() }
func (e *Editor) Path() string { return e.cur().path }

// Contains says path is in the open folder's project, where OpenPath
// shows a file without opening another folder.
func (e *Editor) Contains(path string) bool {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return e.root.Get() != "" && e.inside(path)
}

// Title is the name the document shown gives itself, or else its file
// name without the extension, "" while none is open; or the components'
// while they show.
func (e *Editor) Title() string {
	if e.showcase.Get() {
		return locales.For(e.cx).Components()
	}
	if name := e.cur().doc.Get().Title(); name != "" {
		return name
	}
	return keyName(e.cur().name())
}

// Edited says the document shown changed since it was last saved, which
// showing another file or folder, sending a message or Save saves.
func (e *Editor) Edited() bool { return e.cur().dirty.Get() }

// File is the document shown.
func (e *Editor) File() flowfile.File { return e.cur().doc.Get() }

// CanUndo and CanRedo say the document shown has a change to undo or to
// redo, which it does not while the agent works.
func (e *Editor) CanUndo() bool { return len(e.cur().undos.Get()) > 0 && !e.Working() }
func (e *Editor) CanRedo() bool { return len(e.cur().redos.Get()) > 0 && !e.Working() }

// FlowOpen says the document shown holds flows, and LeftToRight that the
// flow ToggleDirection turns first points right.
func (e *Editor) FlowOpen() bool { return e.cur().doc.Get().Kind == flowfile.Chart }
func (e *Editor) LeftToRight() bool {
	f := e.cur().doc.Get()
	which := toggled(f, e.cur().selected.Get())
	return len(which) > 0 && f.Flows[which[0]].Direction == flowfile.Right
}

// ZoomIn, ZoomOut, Fit and ActualSize move the camera of the canvas
// shown: the components, a design or the flow chart.
func (e *Editor) ZoomIn()     { e.canvas().state.ZoomIn() }
func (e *Editor) ZoomOut()    { e.canvas().state.ZoomOut() }
func (e *Editor) Fit()        { e.canvas().state.Fit() }
func (e *Editor) ActualSize() { e.canvas().actualSize() }

// canvas is the camera of the canvas shown, as the components cover the
// document and a design covers the flow pane.
func (e *Editor) canvas() *paneCamera {
	switch {
	case e.showcaseCanvas != nil:
		return e.showcaseCanvas
	case e.designCanvas != nil:
		return e.designCanvas
	case e.flowCanvas != nil:
		return e.flowCanvas
	}
	return &paneCamera{state: e.camera, size: func() core.Size { return core.Size{} }}
}

// paneCamera is a pane's camera and the pane's size.
type paneCamera struct {
	state *camera.State
	size  func() core.Size
}

// mount keeps c in *at while the pane of cx shows.
func (c *paneCamera) mount(cx *core.Scope, at **paneCamera) {
	*at = c
	cx.Cleanup(func() {
		if *at == c {
			*at = nil
		}
	})
}

// actualSize shows the content at its size around the middle of the
// pane: a fit is no larger than the content's size, so fitting a point
// shows it at 100%.
func (c *paneCamera) actualSize() {
	size := c.size()
	x, y, zoom := c.state.Camera()
	mid := core.Point{X: core.Points((float64(size.Width)/2 - x) / zoom), Y: core.Points((float64(size.Height)/2 - y) / zoom)}
	c.state.FitBounds(core.Bounds{Origin: core.Point{X: mid.X - .5, Y: mid.Y - .5}, Size: core.Size{Width: 1, Height: 1}})
}

// PanelsHidden says the Window hides its sidebar and assistant, which
// TogglePanels shows again, so the workspace takes the window's width.
func (e *Editor) PanelsHidden() bool { return e.panelsHidden.Get() }
func (e *Editor) TogglePanels()      { e.panelsHidden.Set(!e.panelsHidden.GetUntracked()) }

// OutlinesHidden says components draw without the dashed line marking
// their bounds, which ToggleOutlines draws again.
func (e *Editor) OutlinesHidden() bool { return e.outlinesHidden.Get() }
func (e *Editor) ToggleOutlines()      { e.outlinesHidden.Set(!e.outlinesHidden.GetUntracked()) }

// ShowComponents shows every component of the open folder together, and
// Showcase says they show; choosing a file shows it again.
func (e *Editor) ShowComponents() {
	if e.folder.GetUntracked() != "" {
		e.showcase.Set(true)
	}
}
func (e *Editor) Showcase() bool { return e.showcase.Get() }

// HasComponents says the open folder has components.
func (e *Editor) HasComponents() bool { return len(componentEntries(e.files.Get())) > 0 }

// NewChat shows a new conversation about the folder, keeping the others
// in the assistant's list.
func (e *Editor) NewChat() { e.a.newChat() }
