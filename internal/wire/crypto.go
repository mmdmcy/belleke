// Package wire is how two Belleke devices talk: finding each other on the
// network, agreeing a key for one call, and sealing every message and every
// 20 ms of sound with it.
//
// Trust: a device is known by its Ed25519 key (package identity). A call is
// only set up between two devices that have trusted each other once (they
// compared the six-digit fingerprint). For each call both sides make a fresh
// X25519 key pair, sign it with their device key together with the other
// side's name and a nonce, and derive the call key with HKDF-SHA256 from the
// X25519 agreement. Everything after that is ChaCha20-Poly1305: control
// messages over TCP, sound over UDP, each direction with its own nonce space,
// so nothing can be replayed or reflected. Only the Go standard library and
// golang.org/x/crypto are used.
package wire

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Direction bytes: the first byte of every nonce says who sent it and over
// which channel, so the two sides never produce the same nonce.
const (
	dirControlAB byte = 0
	dirControlBA byte = 1
	dirAudioAB   byte = 2
	dirAudioBA   byte = 3
)

// Ephemeral is one call's X25519 key pair.
type Ephemeral struct {
	Public  [32]byte
	private [32]byte
}

// NewEphemeral makes a fresh X25519 key pair.
func NewEphemeral() (*Ephemeral, error) {
	var e Ephemeral
	if _, err := io.ReadFull(rand.Reader, e.private[:]); err != nil {
		return nil, err
	}
	pub, err := curve25519.X25519(e.private[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	copy(e.Public[:], pub)
	return &e, nil
}

// Agree derives the 32-byte call key from our ephemeral private key, their
// ephemeral public key, the call nonce and both device ids (so a key is bound
// to exactly this pair and this call).
func (e *Ephemeral) Agree(theirs [32]byte, nonce []byte, idA, idB string) ([]byte, error) {
	shared, err := curve25519.X25519(e.private[:], theirs[:])
	if err != nil {
		return nil, err
	}
	allZero := true
	for _, b := range shared {
		if b != 0 {
			allZero = false
		}
	}
	if allZero {
		return nil, errors.New("weak peer key")
	}
	reader := hkdf.New(sha256.New, shared, nonce, []byte("belleke call v1\n"+idA+"\n"+idB))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

// Sealer seals and opens for one call. `initiator` decides which nonce bytes
// are ours.
type Sealer struct {
	aead      interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	}
	initiator    bool
	controlOut   atomic.Uint64
	audioOut     atomic.Uint64
	controlIn    uint64
	audioWindow  replayWindow
}

// NewSealer wraps a call key.
func NewSealer(key []byte, initiator bool) (*Sealer, error) {
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead, initiator: initiator}, nil
}

func (s *Sealer) nonce(dir byte, counter uint64) []byte {
	n := make([]byte, chacha20poly1305.NonceSize)
	n[0] = dir
	binary.BigEndian.PutUint64(n[4:], counter)
	return n
}

func (s *Sealer) dirs() (controlOut, controlIn, audioOut, audioIn byte) {
	if s.initiator {
		return dirControlAB, dirControlBA, dirAudioAB, dirAudioBA
	}
	return dirControlBA, dirControlAB, dirAudioBA, dirAudioAB
}

// SealControl seals one control message; the counter travels in front so the
// reader uses the same nonce.
func (s *Sealer) SealControl(plain []byte) []byte {
	out, _, _, _ := s.dirs()
	counter := s.controlOut.Add(1)
	nonce := s.nonce(out, counter)
	buf := make([]byte, 8, 8+len(plain)+chacha20poly1305.Overhead)
	binary.BigEndian.PutUint64(buf, counter)
	return s.aead.Seal(buf, nonce, plain, nil)
}

// OpenControl opens a control message; counters must strictly increase.
func (s *Sealer) OpenControl(sealed []byte) ([]byte, error) {
	if len(sealed) < 8+chacha20poly1305.Overhead {
		return nil, errors.New("short control message")
	}
	_, in, _, _ := s.dirs()
	counter := binary.BigEndian.Uint64(sealed[:8])
	if counter <= s.controlIn {
		return nil, errors.New("replayed control message")
	}
	plain, err := s.aead.Open(nil, s.nonce(in, counter), sealed[8:], nil)
	if err != nil {
		return nil, err
	}
	s.controlIn = counter
	return plain, nil
}

// SealAudio seals one frame of sound; its sequence number is the counter.
func (s *Sealer) SealAudio(frame []byte) []byte {
	_, _, out, _ := s.dirs()
	counter := s.audioOut.Add(1)
	buf := make([]byte, 8, 8+len(frame)+chacha20poly1305.Overhead)
	binary.BigEndian.PutUint64(buf, counter)
	return s.aead.Seal(buf, s.nonce(out, counter), frame, nil)
}

// OpenAudio opens one frame and gives its sequence number. Sound may arrive
// out of order, so a 64-frame window decides what is a replay.
func (s *Sealer) OpenAudio(sealed []byte) (uint64, []byte, error) {
	if len(sealed) < 8+chacha20poly1305.Overhead {
		return 0, nil, errors.New("short audio packet")
	}
	_, _, _, in := s.dirs()
	counter := binary.BigEndian.Uint64(sealed[:8])
	if !s.audioWindow.fresh(counter) {
		return 0, nil, errors.New("replayed audio packet")
	}
	plain, err := s.aead.Open(nil, s.nonce(in, counter), sealed[8:], nil)
	if err != nil {
		return 0, nil, err
	}
	s.audioWindow.mark(counter)
	return counter, plain, nil
}

// replayWindow is the usual sliding bitmap (as in IPsec and WireGuard).
type replayWindow struct {
	highest uint64
	bits    uint64
}

func (w *replayWindow) fresh(seq uint64) bool {
	if seq == 0 {
		return false
	}
	if seq > w.highest {
		return true
	}
	if w.highest-seq >= 64 {
		return false
	}
	return w.bits&(1<<(w.highest-seq)) == 0
}

func (w *replayWindow) mark(seq uint64) {
	if seq > w.highest {
		shift := seq - w.highest
		if shift >= 64 {
			w.bits = 0
		} else {
			w.bits <<= shift
		}
		w.highest = seq
		w.bits |= 1
		return
	}
	w.bits |= 1 << (w.highest - seq)
}
