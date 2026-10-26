// Belleke: a private one-to-one voice call app for two people on the same
// network. No account, no server. See README.md.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mmdmcy/belleke/internal/audio"
	"github.com/mmdmcy/belleke/internal/call"
	"github.com/mmdmcy/belleke/internal/identity"
	"github.com/mmdmcy/belleke/internal/store"
	"github.com/mmdmcy/belleke/internal/ui"
)

// Version is set by the build (-ldflags "-X main.Version=...").
var Version = "dev"

func main() {
	selftest := flag.Bool("selftest", false, "run two devices in this process, pass sound between them and exit")
	dir := flag.String("dir", "", "config folder (default: the user's config folder, belleke)")
	version := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *version {
		fmt.Println("belleke", Version)
		return
	}
	if *selftest {
		if err := runSelftest(); err != nil {
			fmt.Fprintln(os.Stderr, "selftest failed:", err)
			os.Exit(1)
		}
		fmt.Println("selftest ok")
		return
	}
	if *dir == "" {
		*dir = store.DefaultDir()
	}
	st, err := store.Open(*dir)
	if err != nil {
		fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(*dir, "belleke.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		log.SetOutput(logFile)
	}
	me, err := identity.Load(*dir)
	if err != nil {
		fatal(err)
	}
	engine := call.New(me, st)
	if err := engine.Start(); err != nil {
		fatal(err)
	}
	defer engine.Close()
	log.Printf("belleke %s as %s, tcp %d udp %d", Version, st.Settings().Name, engine.TCPPort(), engine.UDPPort())
	ui.Run(engine, st, me, Version)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// runSelftest proves the whole path except the microphone and speaker: two
// identities, a call set up over TCP, sound sealed over UDP both ways, chat,
// hang up, call log.
func runSelftest() error {
	mk := func(name string) (*call.Engine, error) {
		dir, err := os.MkdirTemp("", "belleke-selftest-")
		if err != nil {
			return nil, err
		}
		id, err := identity.Load(dir)
		if err != nil {
			return nil, err
		}
		st, err := store.Open(dir)
		if err != nil {
			return nil, err
		}
		_ = st.SaveSettings(store.Settings{Name: name})
		e := call.New(id, st)
		e.NoAudio = true
		return e, e.Start()
	}
	a, err := mk("A")
	if err != nil {
		return err
	}
	b, err := mk("B")
	if err != nil {
		return err
	}
	defer a.Close()
	defer b.Close()
	_ = a.Store.Trust(store.Peer{ID: b.Me.ID(), Name: "B"})
	_ = b.Store.Trust(store.Peer{ID: a.Me.ID(), Name: "A"})
	if err := a.CallAddr(b.Me.ID(), net.JoinHostPort("127.0.0.1", strconv.Itoa(b.TCPPort()))); err != nil {
		return err
	}
	if err := wait(func() bool { return b.State() == call.Ringing }); err != nil {
		return fmt.Errorf("B never rang: %w", err)
	}
	if err := b.Answer(); err != nil {
		return err
	}
	if err := wait(func() bool { return a.State() == call.InCall && b.State() == call.InCall }); err != nil {
		return fmt.Errorf("call did not go live: %w", err)
	}
	time.Sleep(100 * time.Millisecond)
	for _, frame := range audio.Tone(440, 50) {
		a.SendFrame(frame)
		b.SendFrame(frame)
		time.Sleep(2 * time.Millisecond)
	}
	if err := wait(func() bool { return a.Received >= 50 && b.Received >= 50 }); err != nil {
		return fmt.Errorf("sound did not arrive both ways (%d/%d): %w", a.Received, b.Received, err)
	}
	if err := a.Chat("test"); err != nil {
		return err
	}
	if err := wait(func() bool { return len(b.Store.Chats(a.Me.ID())) == 1 }); err != nil {
		return fmt.Errorf("chat did not arrive: %w", err)
	}
	a.Hangup()
	if err := wait(func() bool { return a.State() == call.Idle && b.State() == call.Idle }); err != nil {
		return err
	}
	if calls := b.Store.Calls(); len(calls) != 1 || calls[0].Result != "answered" {
		return fmt.Errorf("call log wrong: %+v", calls)
	}
	fmt.Printf("call set up, %d + %d sound frames sealed and delivered, chat and log fine\n", a.Received, b.Received)
	return nil
}

func wait(ok func() bool) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timed out")
}
