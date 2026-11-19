package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/nsgo-dev/nsgo/core"
	agio "github.com/oneo-me/nsgo-agio"
	"github.com/oneo-me/nsgo-agio/agent"
	"github.com/oneo-me/nsgo-agio/chatview"
	"github.com/oneo-me/nsgo-agio/host"

	"github.com/oneo-me/nsgo-scenra/scenraview"
)

// The storage keys of the agents and models the settings turned off, a
// line each.
const (
	keyDisabled       = "agents.disabled"
	keyDisabledModels = "agents.disabledModels"
)

// skillDirs are the skills among the app's resources, scenra teaching
// the files and scenra-app working in the app, which NSGo puts in the
// bundle's Contents/Resources on macOS and beside the executable
// elsewhere, as NSGo names that directory to no app; none when they are
// not there, as in tests.
func skillDirs() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	root := filepath.Dir(exe)
	if runtime.GOOS == "darwin" {
		root = filepath.Join(root, "..", "Resources")
	}
	var dirs []string
	for _, name := range []string{"scenra", "scenra-app"} {
		dir := filepath.Join(root, "skills", name)
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// lines splits a stored list.
func lines(s string) []string {
	return slices.DeleteFunc(strings.Split(s, "\n"), func(s string) bool { return s == "" })
}

// startAgents runs the coding agents for the window's scope, offering
// each the scenra skill and the server telling what the user sees, and
// keeps the agents and models turned off in the storage; while sess runs
// them on another machine, the backend it reached drives them instead, and
// no server of this one tells them anything. opts.Drivers, when set,
// stands for the agent CLIs, as in tests.
func startAgents(cx *core.Scope, opts host.Options, sess *session) (*chatview.Model, *scenraview.Server) {
	st := cx.Storage()
	disabled, _ := st.GetItem(keyDisabled)
	models, _ := st.GetItem(keyDisabledModels)
	for _, t := range lines(disabled) {
		opts.Disabled = append(opts.Disabled, agent.Tool(t))
	}
	opts.DisabledModels = lines(models)
	// Another machine's agents edit the copy of the folder it serves, and
	// take the skills it has there.
	if remote := sess.backend(); remote != nil {
		m := chatview.Bind(cx, remote)
		m.Do(func(b agio.Backend) error { b.Discover(); return nil }, nil)
		keepDisabled(cx, m)
		return m, nil
	}
	opts.Skills = append(opts.Skills, skillDirs()...)
	srv, err := scenraview.Listen()
	if err == nil {
		opts.MCPServers = append(opts.MCPServers, srv.Agent())
	}
	h := host.New(opts)
	cx.Cleanup(func() {
		h.Close()
		if srv != nil {
			srv.Close()
		}
	})
	m := chatview.Bind(cx, h)
	keepDisabled(cx, m)
	return m, srv
}

// keepDisabled keeps the agents and models turned off in the storage, as
// the backend's state shows them.
func keepDisabled(cx *core.Scope, m *chatview.Model) {
	st := cx.Storage()
	cx.Effect(func() {
		var tools, keys []string
		for _, t := range m.State().Tools {
			if t.Disabled {
				tools = append(tools, string(t.Tool))
			}
			for _, id := range t.DisabledModels {
				keys = append(keys, agent.Key(t.Tool, id))
			}
		}
		core.Untracked(cx, func() bool {
			st.SetItem(keyDisabled, strings.Join(tools, "\n"))
			st.SetItem(keyDisabledModels, strings.Join(keys, "\n"))
			return true
		})
	})
}
