package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/oneo-me/nsgo-agio/host"
	"github.com/oneo-me/nsgo-agio/wire"
)

// serve runs the agents of a folder on this machine, for a Scenra window
// on another one to drive them over Agio's wire protocol, and prints how
// it listens: its window's settings connect to it through ssh, giving the
// address, the token and the folder to open.
//
//	scenra serve [-port 7788] [-bind 127.0.0.1] [-token TOKEN] [-dir DIR]
func serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	port := fs.Int("port", 7788, "the port to listen on")
	bind := fs.String("bind", "127.0.0.1", "the interface to listen on, 0.0.0.0 for every one")
	token := fs.String("token", "", "the token a Scenra window connects with, made up when empty")
	dir := fs.String("dir", "", "the folder whose agents to run, the working directory when empty")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		if wd, err := os.Getwd(); err == nil {
			*dir = wd
		}
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scenra serve:", err)
		return 1
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "scenra serve: %s is not a folder\n", abs)
		return 1
	}
	if *token == "" {
		key := make([]byte, 16)
		if _, err := rand.Read(key); err != nil {
			fmt.Fprintln(os.Stderr, "scenra serve:", err)
			return 1
		}
		*token = hex.EncodeToString(key)
	}
	// The agents the app's settings turned off are the window's, which
	// runs elsewhere, so this serves every one of them with the skills
	// beside this program and no tools of its own.
	h := host.New(host.Options{Dir: abs, Skills: skillDirs()})
	defer h.Close()
	addr := net.JoinHostPort(*bind, strconv.Itoa(*port))
	fmt.Printf("scenra serve: %s, token %s\n", addr, *token)
	if err := wire.ListenAndServe(h, addr, *token); err != nil {
		fmt.Fprintln(os.Stderr, "scenra serve:", err)
		return 1
	}
	return 0
}
