// Package store keeps what Belleke remembers, as small JSON files in the
// user's config folder: the device's name, the devices it trusts, the call
// log and the chat with each device. Nothing leaves the machine.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Settings are the person's choices.
type Settings struct {
	Name string `json:"name"`
}

// Peer is a device this one trusts.
type Peer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TrustedAt time.Time `json:"trusted_at"`
	LastAddr  string    `json:"last_addr,omitempty"`
}

// Call is one line of the call log.
type Call struct {
	PeerID   string    `json:"peer"`
	PeerName string    `json:"name"`
	Start    time.Time `json:"start"`
	Seconds  int       `json:"seconds"`
	Outgoing bool      `json:"outgoing"`
	// `answered`, `missed`, `rejected`, `failed`.
	Result string `json:"result"`
}

// Chat is one line of the chat with a device.
type Chat struct {
	When time.Time `json:"when"`
	Mine bool      `json:"mine"`
	Text string    `json:"text"`
}

// Store is the folder.
type Store struct {
	Dir string
	mu  sync.Mutex
}

// Open makes the folder when needed.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

// DefaultDir is the platform's config folder plus "belleke".
func DefaultDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "belleke")
}

func (s *Store) read(name string, v any) error {
	raw, err := os.ReadFile(filepath.Join(s.Dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (s *Store) write(name string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.Dir, name+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.Dir, name))
}

// Settings reads the settings; a missing file gives the host name as name.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	var set Settings
	_ = s.read("settings.json", &set)
	if set.Name == "" {
		set.Name, _ = os.Hostname()
		if set.Name == "" {
			set.Name = "Belleke"
		}
	}
	return set
}

// SaveSettings writes them.
func (s *Store) SaveSettings(set Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(set.Name) > 40 {
		set.Name = set.Name[:40]
	}
	return s.write("settings.json", set)
}

// Peers lists the trusted devices, by name.
func (s *Store) Peers() []Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peers()
}

func (s *Store) peers() []Peer {
	var peers []Peer
	_ = s.read("peers.json", &peers)
	sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
	return peers
}

// Peer finds a trusted device by id.
func (s *Store) Peer(id string) (Peer, bool) {
	for _, p := range s.Peers() {
		if p.ID == id {
			return p, true
		}
	}
	return Peer{}, false
}

// Trust adds or updates a device.
func (s *Store) Trust(peer Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	peers := s.peers()
	found := false
	for i := range peers {
		if peers[i].ID == peer.ID {
			peers[i].Name, peers[i].LastAddr = peer.Name, peer.LastAddr
			found = true
		}
	}
	if !found {
		if peer.TrustedAt.IsZero() {
			peer.TrustedAt = time.Now()
		}
		peers = append(peers, peer)
	}
	return s.write("peers.json", peers)
}

// Forget removes a device from the trusted list.
func (s *Store) Forget(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	peers := s.peers()
	kept := peers[:0]
	for _, p := range peers {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	return s.write("peers.json", kept)
}

// Calls lists the call log, newest first.
func (s *Store) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var calls []Call
	_ = s.read("calls.json", &calls)
	sort.Slice(calls, func(i, j int) bool { return calls[i].Start.After(calls[j].Start) })
	return calls
}

// LogCall appends to the call log, keeping the last 500.
func (s *Store) LogCall(call Call) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var calls []Call
	_ = s.read("calls.json", &calls)
	calls = append(calls, call)
	if len(calls) > 500 {
		calls = calls[len(calls)-500:]
	}
	return s.write("calls.json", calls)
}

func chatFile(peerID string) string {
	safe := make([]byte, 0, len(peerID))
	for _, b := range []byte(peerID) {
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_' {
			safe = append(safe, b)
		}
	}
	return "chat-" + string(safe) + ".json"
}

// Chats lists the chat with a device, oldest first.
func (s *Store) Chats(peerID string) []Chat {
	s.mu.Lock()
	defer s.mu.Unlock()
	var chats []Chat
	_ = s.read(chatFile(peerID), &chats)
	return chats
}

// AddChat appends one line, keeping the last 2000.
func (s *Store) AddChat(peerID string, chat Chat) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var chats []Chat
	_ = s.read(chatFile(peerID), &chats)
	chats = append(chats, chat)
	if len(chats) > 2000 {
		chats = chats[len(chats)-2000:]
	}
	return s.write(chatFile(peerID), chats)
}
