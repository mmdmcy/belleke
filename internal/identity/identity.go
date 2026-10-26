// Package identity is who this device is: one Ed25519 key, made on first start
// and never moved. Its public key is the device's name on the wire; a short
// fingerprint of two keys is what two people compare when they trust each
// other (like Bluetooth's pairing code).
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Identity is this device's long-term signing key.
type Identity struct {
	Public  ed25519.PublicKey
	private ed25519.PrivateKey
}

type file struct {
	Private string `json:"private"`
}

// Load reads the identity from dir, making one when there is none.
func Load(dir string) (*Identity, error) {
	path := filepath.Join(dir, "identity.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return create(dir, path)
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priv, err := base64.StdEncoding.DecodeString(f.Private)
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s: not an identity", path)
	}
	key := ed25519.PrivateKey(priv)
	return &Identity{Public: key.Public().(ed25519.PublicKey), private: key}, nil
}

func create(dir, path string) (*Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(file{Private: base64.StdEncoding.EncodeToString(priv)})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, err
	}
	return &Identity{Public: pub, private: priv}, nil
}

// Sign signs a message with the device key.
func (id *Identity) Sign(message []byte) []byte {
	return ed25519.Sign(id.private, message)
}

// ID is the public key as text, the device's name on the wire.
func (id *Identity) ID() string { return Encode(id.Public) }

// Encode gives a public key as text.
func Encode(pub ed25519.PublicKey) string { return base64.RawURLEncoding.EncodeToString(pub) }

// Decode reads a public key from text.
func Decode(text string) (ed25519.PublicKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("not a device id")
	}
	return ed25519.PublicKey(raw), nil
}

// Verify checks a signature by the device with public key pub.
func Verify(pub ed25519.PublicKey, message, signature []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, message, signature)
}

// Fingerprint is the six-digit code both people see when they trust each
// other: the same on both sides whatever the order of the keys, and hard to
// make collide on purpose (SHA-256 over both keys).
func Fingerprint(a, b ed25519.PublicKey) string {
	keys := []string{Encode(a), Encode(b)}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte("belleke fingerprint v1\n" + keys[0] + "\n" + keys[1]))
	n := binary.BigEndian.Uint32(sum[:4]) % 1_000_000
	return fmt.Sprintf("%03d %03d", n/1000, n%1000)
}
