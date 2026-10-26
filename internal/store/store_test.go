package store

import (
	"testing"
	"time"
)

func TestPeersCallsAndChatsRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Settings().Name == "" {
		t.Fatal("no default name")
	}
	if err := s.SaveSettings(Settings{Name: "Rei"}); err != nil || s.Settings().Name != "Rei" {
		t.Fatal("settings did not stick")
	}
	_ = s.Trust(Peer{ID: "b", Name: "Zoe"})
	_ = s.Trust(Peer{ID: "a", Name: "Anna"})
	_ = s.Trust(Peer{ID: "b", Name: "Zoë", LastAddr: "10.0.0.2"})
	peers := s.Peers()
	if len(peers) != 2 || peers[0].Name != "Anna" || peers[1].Name != "Zoë" || peers[1].LastAddr != "10.0.0.2" {
		t.Fatalf("peers: %+v", peers)
	}
	if _, ok := s.Peer("b"); !ok {
		t.Fatal("peer b missing")
	}
	_ = s.Forget("a")
	if len(s.Peers()) != 1 {
		t.Fatal("forget failed")
	}
	_ = s.LogCall(Call{PeerID: "b", PeerName: "Zoë", Start: time.Now().Add(-time.Hour), Result: "missed"})
	_ = s.LogCall(Call{PeerID: "b", PeerName: "Zoë", Start: time.Now(), Seconds: 90, Result: "answered", Outgoing: true})
	calls := s.Calls()
	if len(calls) != 2 || calls[0].Result != "answered" {
		t.Fatalf("calls: %+v", calls)
	}
	_ = s.AddChat("b/../x", Chat{When: time.Now(), Mine: true, Text: "hoi"})
	if chats := s.Chats("b/../x"); len(chats) != 1 || chats[0].Text != "hoi" {
		t.Fatal("chat did not round trip")
	}
	if chatFile("b/../x") != "chat-bx.json" {
		t.Fatal("chat file name not sanitised")
	}
}
