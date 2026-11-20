package main

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
	agio "github.com/oneo-me/nsgo-agio"
	"github.com/oneo-me/nsgo-agio/wire"

	"github.com/oneo-me/nsgo-scenra/cmd/scenra/locales"
)

// The storage keys of the machine another runs the agents on, which the
// app reaches over Agio's wire protocol, through ssh when it names a host.
const (
	keyRemoteOn    = "remote.on"
	keyRemoteSSH   = "remote.ssh"
	keyRemoteAddr  = "remote.addr"
	keyRemoteToken = "remote.token"
)

// defaultRemoteAddr is where a Scenra serve listens, which the app dials
// when the settings give no address of their own.
const defaultRemoteAddr = "127.0.0.1:7788"

// tunnelWait bounds how long the app waits for ssh to forward its port,
// and dialWait how long it then waits to reach the server through it.
const (
	tunnelWait = 8 * time.Second
	dialWait   = 3 * time.Second
)

// The states of the agents the conversations run on, as the settings say
// them.
const (
	stateLocal = iota
	stateConnecting
	stateRemote
	stateFailed
)

// remote is the machine the agents run on: the ssh host to tunnel through,
// "" to dial Addr itself, and the token the server needs.
type remote struct {
	SSH   string
	Addr  string
	Token string
}

// session says where the window's conversations run their agents: on this
// machine, or on another one, which connecting and disconnecting rebuilds
// the window for. Its state, problem and rev are read by the settings and
// the window.
type session struct {
	cx      *core.Scope
	rev     *core.Signal[int]
	state   *core.Signal[int]
	problem *core.Signal[string]
	cfgMu   sync.Mutex
	remote  remote
	mu      sync.Mutex
	client  *wire.Client
	cmd     *exec.Cmd
}

// config is the machine the settings name.
func (s *session) config() remote {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	return s.remote
}

// setRemote changes the machine the settings name.
func (s *session) setRemote(change func(*remote)) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	change(&s.remote)
}

// newSession reads where the settings said the agents run and, when
// another machine's, connects to it before the window is built.
func newSession(cx *core.Scope) *session {
	st := cx.Storage()
	s := &session{cx: cx, rev: core.NewSignal(cx, 0), state: core.NewSignal(cx, stateLocal), problem: core.NewSignal(cx, "")}
	s.remote.SSH, _ = st.GetItem(keyRemoteSSH)
	s.remote.Addr, _ = st.GetItem(keyRemoteAddr)
	s.remote.Token, _ = st.GetItem(keyRemoteToken)
	if on, _ := st.GetItem(keyRemoteOn); on == "1" {
		s.state.Set(stateConnecting)
		if err := s.connect(s.config()); err != nil {
			s.problem.Set(err.Error())
			s.state.Set(stateFailed)
		} else {
			s.state.Set(stateRemote)
		}
	}
	return s
}

// backend is the agents' backend, nil while they run on this machine.
func (s *session) backend() agio.Backend {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		return nil
	}
	return s.client
}

// connect starts the agents on the machine r names, over ssh when it
// names one, closing the connection before.
func (s *session) connect(r remote) error {
	s.close()
	addr := r.Addr
	if addr == "" {
		addr = defaultRemoteAddr
	}
	var cmd *exec.Cmd
	if r.SSH != "" {
		local, err := freeAddr()
		if err != nil {
			return err
		}
		cmd = exec.Command("ssh", "-N", "-o", "ExitOnForwardFailure=yes", "-o", "BatchMode=yes",
			"-o", "ConnectTimeout=8", "-L", local+":"+addr, r.SSH)
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			return err
		}
		if err := waitAddr(local, tunnelWait); err != nil {
			kill(cmd)
			if msg := strings.TrimSpace(out.String()); msg != "" {
				return errors.New(msg)
			}
			return fmt.Errorf("ssh %s: %w", r.SSH, err)
		}
		addr = local
	}
	if err := waitAddr(addr, dialWait); err != nil {
		if cmd != nil {
			kill(cmd)
		}
		return fmt.Errorf("%s: %w", addr, err)
	}
	cl, err := wire.Dial(addr, r.Token)
	if err != nil {
		if cmd != nil {
			kill(cmd)
		}
		return err
	}
	s.mu.Lock()
	s.client, s.cmd = cl, cmd
	s.mu.Unlock()
	go s.lost(cl)
	return nil
}

// lost says the server cl registered with went away, resigning the agents
// to this machine.
func (s *session) lost(cl *wire.Client) {
	<-cl.Done()
	s.mu.Lock()
	gone := s.client == cl
	if gone {
		s.client = nil
	}
	cmd := s.cmd
	s.mu.Unlock()
	if !gone {
		return
	}
	if cmd != nil {
		kill(cmd)
	}
	cl.Close()
	s.cx.Post(func() {
		s.problem.Set(locales.For(s.cx).RemoteStopped())
		s.state.Set(stateFailed)
		s.rev.Set(s.rev.GetUntracked() + 1)
	})
}

// enable starts the agents on the machine the settings name, in the
// background, rebuilding the window for them once they answer.
func (s *session) enable() {
	s.state.Set(stateConnecting)
	s.problem.Set("")
	s.rememberOn(true)
	r := s.config()
	go func() {
		err := s.connect(r)
		s.cx.Post(func() {
			if err != nil {
				s.problem.Set(err.Error())
				s.state.Set(stateFailed)
				return
			}
			s.state.Set(stateRemote)
			s.rev.Set(s.rev.GetUntracked() + 1)
		})
	}()
}

// disable runs the agents on this machine again.
func (s *session) disable() {
	s.rememberOn(false)
	s.close()
	// The window rebuilds once the event that turned it off is done.
	s.cx.Post(func() {
		s.problem.Set("")
		s.state.Set(stateLocal)
		s.rev.Set(s.rev.GetUntracked() + 1)
	})
}

// reconnect tries the machine the settings name again.
func (s *session) reconnect() { s.enable() }

// rememberOn keeps the switch's state, so the next start runs the agents
// where this one did.
func (s *session) rememberOn(on bool) {
	st := s.cx.Storage()
	if on {
		st.SetItem(keyRemoteOn, "1")
		return
	}
	st.SetItem(keyRemoteOn, "0")
}

// close ends the connection to the other machine, whose ssh ends with it.
func (s *session) close() {
	s.mu.Lock()
	cl, cmd := s.client, s.cmd
	s.client, s.cmd = nil, nil
	s.mu.Unlock()
	if cl != nil {
		cl.Close()
	}
	if cmd != nil {
		kill(cmd)
	}
}

// freeAddr is an address of the loopback interface that nothing listens
// on, for ssh to forward the other machine's server to.
func freeAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	return ln.Addr().String(), nil
}

// waitAddr waits until addr accepts connections, for at most d.
func waitAddr(addr string, d time.Duration) error {
	deadline := time.Now().Add(d)
	var err error
	for {
		conn, dialErr := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return nil
		}
		err = dialErr
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// kill ends cmd and waits for it.
func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
	cmd.Wait()
}

// serveCommand is how the other machine serves the folder dir to the app.
func serveCommand(r remote, dir string) string {
	addr := r.Addr
	if addr == "" {
		addr = defaultRemoteAddr
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "7788"
	}
	token := r.Token
	if token == "" {
		token = "…"
	}
	if dir == "" {
		dir = "…"
	}
	return "scenra serve -port " + port + " -token " + token + ` -dir "` + dir + `"`
}

// remoteSection says where the agents the conversations run start, and
// connects the app to another machine's and back.
func remoteSection(cx *core.Scope, s *session) core.Node {
	tr := locales.For(cx)
	colors := ui.ColorsOf(cx)
	st := cx.Storage()
	row := func(title string, control core.Node) core.Node {
		return core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12}, Children: []core.Node{
			core.Text{Content: title, Style: core.Style{FontSize: 13, Width: core.Pt(96), FlexShrink: 0}, StyleFunc: ui.Colored(colors.Text)},
			control,
		}}
	}
	field := func(key string, title, placeholder string, masked bool) core.Node {
		initial, _ := st.GetItem(key)
		value := core.NewSignal(cx, initial)
		return row(title, ui.Input{Value: value.Get, Label: title, Placeholder: placeholder, Masked: masked,
			Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0)},
			OnChange: func(text string) {
				value.Set(text)
				if text = strings.TrimSpace(text); text == "" {
					st.RemoveItem(key)
				} else {
					st.SetItem(key, text)
				}
				s.setRemote(func(r *remote) {
					switch key {
					case keyRemoteSSH:
						r.SSH = text
					case keyRemoteAddr:
						r.Addr = text
					case keyRemoteToken:
						r.Token = text
					}
				})
			}})
	}
	on := func() bool { return s.state.Get() != stateLocal }
	status := func() string {
		switch s.state.Get() {
		case stateConnecting:
			return tr.RemoteConnecting()
		case stateRemote:
			return tr.RemoteConnected()
		case stateFailed:
			return tr.RemoteFailed(s.problem.Get())
		}
		return tr.RemoteLocal()
	}
	folder := ""
	if shown != nil {
		if e := shown.editorShown(); e != nil {
			folder = e.Folder()
		}
	}
	command := core.Text{ContentFunc: func() string { return serveCommand(s.config(), folder) },
		Style: ui.TypeCaption.Merge(core.Style{Padding: core.Symmetric(8, 10), CornerRadius: ui.RadiusControl}),
		StyleFunc: func() core.Style {
			return core.Style{Background: colors.Surface(), Color: colors.Text()}
		}}
	return core.View{Style: core.Style{Gap: 8}, Children: []core.Node{
		core.View{Style: core.Style{Gap: 2}, Children: []core.Node{
			ui.Text(tr.RemoteSection(), 15, ui.Semibold, colors.Text),
			core.Text{Content: tr.RemoteDetail(), Style: ui.TypeCaption, StyleFunc: ui.Colored(colors.Muted)},
		}},
		core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12}, Children: []core.Node{
			ui.Switch{Checked: on, Label: tr.RemoteOn(), OnChange: func(v bool) {
				if v {
					s.enable()
				} else {
					s.disable()
				}
			}},
			core.Text{Content: tr.RemoteOn(), Style: core.Style{FontSize: 13, FlexShrink: 1}, StyleFunc: ui.Colored(colors.Text)},
			ui.Spacer{},
			core.Show{When: func() bool { return s.state.Get() != stateLocal && s.state.Get() != stateConnecting },
				Then: func(*core.Scope) core.Node {
					return ui.Button{Text: tr.RemoteReconnect(), Variant: ui.ButtonOutline, OnPress: s.reconnect, TestID: "remote-reconnect"}
				}},
		}},
		field(keyRemoteSSH, tr.RemoteSSH(), tr.RemoteSSHPlaceholder(), false),
		field(keyRemoteAddr, tr.RemoteAddr(), defaultRemoteAddr, false),
		field(keyRemoteToken, tr.RemoteToken(), tr.RemoteTokenPlaceholder(), true),
		core.Text{ContentFunc: status, Style: ui.TypeCaption, StyleFunc: func() core.Style {
			if s.state.Get() == stateFailed {
				return core.Style{Color: colors.Danger()}
			}
			return core.Style{Color: colors.Muted()}
		}},
		core.Text{Content: tr.RemoteServe(), Style: ui.TypeCaption, StyleFunc: ui.Colored(colors.Muted)},
		core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8}, Children: []core.Node{
			core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0)}, Children: []core.Node{command}},
			ui.IconButton{Icon: lucide.Copy, Label: tr.RemoteCopy(), Size: 24, Tint: colors.Muted, OnPress: func() {
				cx.SetClipboard(serveCommand(s.config(), folder))
			}, TestID: "remote-copy"},
		}},
	}}
}
