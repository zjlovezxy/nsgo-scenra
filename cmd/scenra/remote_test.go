package main

import (
	"net"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"github.com/oneo-me/nsgo-agio/agent"
	"github.com/oneo-me/nsgo-agio/host"
	"github.com/oneo-me/nsgo-agio/wire"
)

// serveAgents runs the fake agent's host for another window to drive, as
// `scenra serve` does for a real one, and returns the address it listens
// on.
func serveAgents(t *testing.T) (string, *host.Host) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	h := host.New(host.Options{Env: []string{}, Home: t.TempDir(), Tools: []agent.Tool{agent.Claude},
		Drivers: map[agent.Tool]agent.Driver{agent.Claude: fake{}}})
	t.Cleanup(func() { h.Close() })
	go wire.ListenAndServe(h, addr, "tok")
	wait := time.Now().Add(5 * time.Second)
	for {
		if conn, err := net.Dial("tcp", addr); err == nil {
			conn.Close()
			break
		}
		if time.Now().After(wait) {
			t.Fatalf("nothing listens on %s", addr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return addr, h
}

// TestConversationsRunOnAnotherMachinesAgents says a window whose settings
// name another machine's server runs its folders' conversations there, and
// keeps them on this machine once it stops.
func TestConversationsRunOnAnotherMachinesAgents(t *testing.T) {
	addr, h := serveAgents(t)
	home := t.TempDir()
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		return root(cx, host.Options{Env: []string{}, Home: home, Tools: []agent.Tool{agent.Claude},
			Drivers: map[agent.Tool]agent.Driver{agent.Claude: fake{}}}, newSession(cx))
	}, nsgotest.Size(1280, 800), nsgotest.Storage(map[string]string{
		keyRemoteOn: "1", keyRemoteAddr: addr, keyRemoteToken: "tok"}))
	dir := folder(t, "remote")
	app.PickDirectories = func(core.DirectoriesRequest) []core.Directory { return []core.Directory{{Path: dir}} }
	app.Press("id:home-open-folder")
	app.WaitFor("id:files >> label:home", 5*time.Second)
	// The folder's conversation is the other machine's.
	app.WaitUntil(5*time.Second, func() bool { return len(h.State().Conversations) == 1 })
	if c := h.State().Conversations[0]; c.Dir != dir {
		t.Fatalf("the other machine runs a conversation in %q, want %q", c.Dir, dir)
	}
	app.Press("id:tab-home")
	app.Press("id:home-settings")
	app.WaitFor("id:settings", 1*time.Second)
	app.WaitFor("text:Connected", 5*time.Second)
	// Turning it off runs the agents here again.
	app.Press("label:Run the agents on another machine")
	app.WaitFor("text:Running on this machine.", 5*time.Second)
}

// TestServeCommandNamesTheTokenAndFolder answers the command the settings
// offer the other machine.
func TestServeCommandNamesTheTokenAndFolder(t *testing.T) {
	got := serveCommand(remote{Addr: "10.0.0.2:7799", Token: "tok"}, "/work/design")
	want := `scenra serve -port 7799 -token tok -dir "/work/design"`
	if got != want {
		t.Fatalf("serve command = %q, want %q", got, want)
	}
}
