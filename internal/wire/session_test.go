package wire

import (
	"net"
	"testing"
	"time"

	"github.com/mmdmcy/belleke/internal/identity"
)

func TestCallSetupBetweenTrustedDevices(t *testing.T) {
	a, _ := identity.Load(t.TempDir())
	b, _ := identity.Load(t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan *Session, 1)
	go func() {
		conn, _ := ln.Accept()
		s, err := Accept(conn, b, func(id, name string) bool { return id == a.ID() }, 5*time.Second)
		if err != nil {
			t.Error(err)
			done <- nil
			return
		}
		done <- s
	}()
	caller, err := Dial(ln.Addr().String(), a, "Rei", b.ID(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	callee := <-done
	if callee == nil {
		t.Fatal("callee failed")
	}
	if callee.PeerName != "Rei" || callee.PeerID != a.ID() || caller.PeerID != b.ID() {
		t.Fatal("names and ids did not travel")
	}
	if err := caller.Send(Message{Type: MsgChat, Text: "hoi"}); err != nil {
		t.Fatal(err)
	}
	m, err := callee.Receive()
	if err != nil || m.Type != MsgChat || m.Text != "hoi" {
		t.Fatal("chat did not arrive", err, m)
	}
	if err := callee.Send(Message{Type: MsgAccept, Port: 5000}); err != nil {
		t.Fatal(err)
	}
	if m, err := caller.Receive(); err != nil || m.Type != MsgAccept || m.Port != 5000 {
		t.Fatal("accept did not arrive", err)
	}
	seq, plain, err := callee.OpenAudio(caller.SealAudio([]byte("pcm")))
	if err != nil || seq != 1 || string(plain) != "pcm" {
		t.Fatal("audio sealing across the session failed", err)
	}
}

func TestUntrustedCallerIsRefusedWithoutAKey(t *testing.T) {
	a, _ := identity.Load(t.TempDir())
	b, _ := identity.Load(t.TempDir())
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		conn, _ := ln.Accept()
		_, _ = Accept(conn, b, func(id, name string) bool { return false }, 5*time.Second)
	}()
	if _, err := Dial(ln.Addr().String(), a, "Stranger", b.ID(), 5*time.Second); err == nil {
		t.Fatal("an untrusted caller got a session")
	}
}

func TestWrongCalleeIdIsNoticed(t *testing.T) {
	a, _ := identity.Load(t.TempDir())
	b, _ := identity.Load(t.TempDir())
	c, _ := identity.Load(t.TempDir())
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		conn, _ := ln.Accept()
		_, _ = Accept(conn, b, func(id, name string) bool { return true }, 5*time.Second)
	}()
	// A dials thinking it reaches C, but B answers: the signatures do not match.
	if _, err := Dial(ln.Addr().String(), a, "Rei", c.ID(), 5*time.Second); err == nil {
		t.Fatal("a different device was accepted as the callee")
	}
}
