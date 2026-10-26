package call

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/mmdmcy/belleke/internal/audio"
	"github.com/mmdmcy/belleke/internal/identity"
	"github.com/mmdmcy/belleke/internal/store"
)

func engine(t *testing.T, name string) *Engine {
	id, _ := identity.Load(t.TempDir())
	st, _ := store.Open(t.TempDir())
	_ = st.SaveSettings(store.Settings{Name: name})
	e := New(id, st)
	e.NoAudio = true
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func waitState(t *testing.T, e *Engine, want State) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e.State() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: state %s, wanted %s", e.Store.Settings().Name, e.State(), want)
}

func TestACallFromRingToSoundToHangup(t *testing.T) {
	a, b := engine(t, "Rei"), engine(t, "Zoë")
	_ = a.Store.Trust(store.Peer{ID: b.Me.ID(), Name: "Zoë"})
	_ = b.Store.Trust(store.Peer{ID: a.Me.ID(), Name: "Rei"})
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(b.TCPPort()))
	if err := a.CallAddr(b.Me.ID(), addr); err != nil {
		t.Fatal(err)
	}
	waitState(t, b, Ringing)
	if _, name := b.Peer(); name != "Rei" {
		t.Fatalf("callee sees %q", name)
	}
	if err := b.Answer(); err != nil {
		t.Fatal(err)
	}
	waitState(t, a, InCall)
	waitState(t, b, InCall)
	// Give the audio port message a moment, then send sound both ways.
	time.Sleep(50 * time.Millisecond)
	for _, frame := range audio.Tone(440, 10) {
		a.SendFrame(frame)
		b.SendFrame(frame)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (a.Received < 10 || b.Received < 10) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.Received < 10 || b.Received < 10 {
		t.Fatalf("sound did not arrive both ways: %d / %d", a.Received, b.Received)
	}
	if b.Jitter().Get() == nil {
		t.Fatal("the receive buffer gave no frame")
	}
	if err := a.Chat("hoi"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(b.Store.Chats(a.Me.ID())) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if chats := b.Store.Chats(a.Me.ID()); len(chats) != 1 || chats[0].Text != "hoi" || chats[0].Mine {
		t.Fatalf("chat: %+v", chats)
	}
	a.Hangup()
	waitState(t, a, Idle)
	waitState(t, b, Idle)
	if calls := b.Store.Calls(); len(calls) != 1 || calls[0].Result != "answered" || calls[0].Outgoing {
		t.Fatalf("callee log: %+v", calls)
	}
	if calls := a.Store.Calls(); len(calls) != 1 || !calls[0].Outgoing {
		t.Fatalf("caller log: %+v", calls)
	}
}

func TestDeclinedAndUntrustedCalls(t *testing.T) {
	a, b := engine(t, "Rei"), engine(t, "Zoë")
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(b.TCPPort()))
	// b does not trust a and has no Knock: the call is refused at once.
	if err := a.CallAddr(b.Me.ID(), addr); err == nil {
		t.Fatal("an untrusted call went through")
	}
	waitState(t, a, Idle)
	if calls := a.Store.Calls(); len(calls) != 1 || calls[0].Result != "failed" {
		t.Fatalf("caller log after refusal: %+v", calls)
	}
	_ = a.Store.Trust(store.Peer{ID: b.Me.ID(), Name: "Zoë"})
	_ = b.Store.Trust(store.Peer{ID: a.Me.ID(), Name: "Rei"})
	if err := a.CallAddr(b.Me.ID(), addr); err != nil {
		t.Fatal(err)
	}
	waitState(t, b, Ringing)
	b.Decline()
	waitState(t, a, Idle)
	waitState(t, b, Idle)
	if calls := b.Store.Calls(); len(calls) != 1 || calls[0].Result != "rejected" {
		t.Fatalf("callee log: %+v", calls)
	}
}
