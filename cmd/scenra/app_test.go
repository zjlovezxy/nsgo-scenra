package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"github.com/oneo-me/nsgo-agio/agent"
	"github.com/oneo-me/nsgo-agio/host"
)

// TestMain gives the platform the main thread under nsgo test -target.
func TestMain(m *testing.M) { nsgotest.Main(m) }

// fake is an agent CLI that offers a model and starts no session.
type fake struct{}

func (fake) Tool() agent.Tool { return agent.Claude }
func (fake) Catalog(context.Context, string) (agent.Catalog, error) {
	return agent.Catalog{Model: "opus", Models: []agent.Model{{Tool: agent.Claude, ID: "opus", Name: "Opus"}}}, nil
}
func (fake) Start(agent.StartOptions, func(agent.Event), func(error)) (agent.Session, agent.Info, error) {
	return nil, agent.Info{}, errors.New("no sessions")
}
func (fake) History(string, string) ([]agent.Event, error) { return nil, nil }

// testRoot is the app with the fake agent.
func testRoot(t *testing.T) func(cx *core.Scope) core.Node {
	home := t.TempDir()
	return func(cx *core.Scope) core.Node {
		return root(cx, host.Options{Env: []string{}, Home: home, Tools: []agent.Tool{agent.Claude}, Drivers: map[agent.Tool]agent.Driver{agent.Claude: fake{}}}, newSession(cx))
	}
}

// testRootWith is the app with the fake agent and storage, where pick
// answers the folder picker.
func startApp(t *testing.T, opts ...nsgotest.Option) *nsgotest.App {
	return nsgotest.Start(t, testRoot(t), append([]nsgotest.Option{nsgotest.Size(1280, 800)}, opts...)...)
}

// folder makes a folder of name holding a design file.
func folder(t *testing.T, name string) string {
	dir := filepath.Join(t.TempDir(), name)
	os.Mkdir(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "home.scenra"), []byte("design\ntext \"Welcome to "+name+"\"\n"), 0o644)
	return dir
}

// tabNames are the names of the open tabs, in order.
func tabNames(app *nsgotest.App) []string {
	var names []string
	for _, v := range app.Query("id:tabs >> id:tab") {
		names = append(names, v.Props.Label)
	}
	return names
}

func TestSettingsTurnAgentsOff(t *testing.T) {
	app := startApp(t)
	app.Press("id:home-settings")
	app.WaitFor("id:settings", time.Second)
	// The agent's models load before it turns off.
	app.WaitFor("id:models-claude >> text:1/1 model", 5*time.Second)
	app.Press("id:agent-claude")
	app.WaitUntil(5*time.Second, func() bool { v, _ := app.Storage().GetItem(keyDisabled); return v == "claude" })
}

func TestSettingsTurnOffTheStatistics(t *testing.T) {
	app := startApp(t)
	app.Press("id:home-settings")
	app.WaitFor("id:settings", time.Second)
	app.Press("id:settings >> label:Send usage statistics")
	if v, _ := app.Storage().GetItem(keyStats); v != "off" {
		t.Fatalf("stats = %q, want off", v)
	}
	app.Press("id:settings >> label:Send usage statistics")
	if _, ok := app.Storage().GetItem(keyStats); ok {
		t.Fatal("stats still stored after turning them back on")
	}
}

// TestLanguageSetting shows the language chosen in the settings, keeping
// the open tabs, and starts in it again.
func TestLanguageSetting(t *testing.T) {
	app := startApp(t)
	a := folder(t, "alpha")
	app.PickDirectories = func(core.DirectoriesRequest) []core.Directory { return []core.Directory{{Path: a}} }
	app.Press("id:home-open-folder")
	app.WaitFor("id:files >> label:home", 5*time.Second)
	app.Press("id:files >> label:home")
	app.WaitFor("design >> text:Welcome to alpha", time.Second)
	app.Press("id:tab-home")
	app.Press("id:home-settings")
	app.WaitFor("id:settings", time.Second)
	app.Press("id:language")
	app.Press("text:简体中文")
	if v, _ := app.Storage().GetItem(keyLanguage); v != "zh-Hans" {
		t.Fatalf("language = %q, want zh-Hans", v)
	}
	// The settings stay open in the new language, and the tab with its
	// file.
	app.WaitFor("id:settings >> text:语言", time.Second)
	app.Press("id:tab")
	app.WaitFor("design >> text:Welcome to alpha", time.Second)

	again := startApp(t, nsgotest.Storage(map[string]string{keyLanguage: "zh-Hans"}))
	again.Press("id:home-settings")
	again.WaitFor("id:settings >> text:语言", time.Second)
	again.Press("id:language")
	again.Press("text:跟随系统")
	if _, ok := again.Storage().GetItem(keyLanguage); ok {
		t.Fatal("following the system keeps a language")
	}
	again.WaitFor("id:settings >> text:Language", time.Second)
}

func TestFoldersOpenInTabsFromTheHomePage(t *testing.T) {
	app := startApp(t)
	app.Find("id:recent-empty")
	a, b := folder(t, "alpha"), folder(t, "beta")
	pick := a
	app.PickDirectories = func(core.DirectoriesRequest) []core.Directory { return []core.Directory{{Path: pick}} }
	app.Press("id:home-open-folder")
	app.WaitFor("id:files >> label:home", 5*time.Second)
	app.Press("id:files >> label:home")
	app.WaitFor("design >> text:Welcome to alpha", time.Second)
	app.Press("id:tab-home")
	app.WaitFor("id:recent-alpha", time.Second)
	pick = b
	app.Press("id:home-open-folder")
	app.WaitFor("id:files >> label:home", 5*time.Second)
	app.Press("id:files >> label:home")
	app.WaitFor("design >> text:Welcome to beta", time.Second)
	if names := tabNames(app); !slices.Equal(names, []string{"alpha", "beta"}) {
		t.Fatalf("tabs = %v", names)
	}
	// Each tab's editor names its own folder to the agents.
	editors := shown.editors()
	if len(editors) != 2 || editors[0].ID() == "" || editors[0].ID() == editors[1].ID() {
		t.Fatal("the tabs' editors share an ID")
	}
	if app.Exists("text:Welcome to alpha") {
		t.Fatal("a hidden tab shows")
	}

	// A recent folder opens in the tab it has, which shows again.
	app.Press("id:tab-home")
	app.WaitFor("id:recent-beta", time.Second)
	app.Find("id:recent-alpha >> id:recent-cover >> text:Welcome to alpha")
	app.Press("id:recent-alpha")
	app.WaitFor("design >> text:Welcome to alpha", time.Second)
	if names := tabNames(app); len(names) != 2 {
		t.Fatalf("opening an open folder opened another tab: %v", names)
	}

	// The tabs open again at the next start, the one shown too.
	tabs, _ := app.Storage().GetItem(keyTabs)
	recent, _ := app.Storage().GetItem(keyRecent)
	again := startApp(t, nsgotest.Storage(map[string]string{keyTabs: tabs, keyShown: a, keyRecent: recent}))
	again.WaitFor("design >> text:Welcome to alpha", 5*time.Second)
	if names := tabNames(again); !slices.Equal(names, []string{"alpha", "beta"}) {
		t.Fatalf("tabs again = %v", names)
	}

	// Closing the tab shown shows the next, then the home page.
	again.Press("id:tab >> nth=0 >> id:close-tab")
	again.WaitFor("design >> text:Welcome to beta", time.Second)
	again.Press("id:close-tab")
	again.WaitFor("id:home", time.Second)
	if names := tabNames(again); len(names) != 0 {
		t.Fatalf("tabs left = %v", names)
	}
}
