package wire

import (
	"bytes"
	"testing"
)

func pair(t *testing.T) (*Sealer, *Sealer) {
	a, _ := NewEphemeral()
	b, _ := NewEphemeral()
	nonce := []byte("0123456789abcdef")
	ka, err := a.Agree(b.Public, nonce, "A", "B")
	if err != nil {
		t.Fatal(err)
	}
	kb, err := b.Agree(a.Public, nonce, "A", "B")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ka, kb) {
		t.Fatal("the two sides derived different keys")
	}
	sa, _ := NewSealer(ka, true)
	sb, _ := NewSealer(kb, false)
	return sa, sb
}

func TestControlMessagesRoundTripAndRefuseReplay(t *testing.T) {
	a, b := pair(t)
	sealed := a.SealControl([]byte(`{"t":"ring"}`))
	plain, err := b.OpenControl(sealed)
	if err != nil || string(plain) != `{"t":"ring"}` {
		t.Fatal("round trip failed", err)
	}
	if _, err := b.OpenControl(sealed); err == nil {
		t.Fatal("replay accepted")
	}
	// A message sealed by A cannot be fed back to A (reflection).
	if _, err := a.OpenControl(a.SealControl([]byte("x"))); err == nil {
		t.Fatal("reflection accepted")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := b.OpenControl(sealed); err == nil {
		t.Fatal("tampering accepted")
	}
}

func TestAudioToleratesReorderButNotReplay(t *testing.T) {
	a, b := pair(t)
	p1 := a.SealAudio([]byte("one"))
	p2 := a.SealAudio([]byte("two"))
	p3 := a.SealAudio([]byte("three"))
	if seq, plain, err := b.OpenAudio(p3); err != nil || seq != 3 || string(plain) != "three" {
		t.Fatal("first packet", err)
	}
	if _, _, err := b.OpenAudio(p1); err != nil {
		t.Fatal("late packet refused", err)
	}
	if _, _, err := b.OpenAudio(p2); err != nil {
		t.Fatal("late packet refused", err)
	}
	if _, _, err := b.OpenAudio(p2); err == nil {
		t.Fatal("replay accepted")
	}
}

func TestDifferentCallsHaveDifferentKeys(t *testing.T) {
	a, _ := NewEphemeral()
	b, _ := NewEphemeral()
	k1, _ := a.Agree(b.Public, []byte("n1"), "A", "B")
	k2, _ := a.Agree(b.Public, []byte("n2"), "A", "B")
	k3, _ := a.Agree(b.Public, []byte("n1"), "A", "C")
	if bytes.Equal(k1, k2) || bytes.Equal(k1, k3) {
		t.Fatal("keys not bound to nonce and ids")
	}
	var zero [32]byte
	if _, err := a.Agree(zero, []byte("n"), "A", "B"); err == nil {
		t.Fatal("all-zero peer key accepted")
	}
}
