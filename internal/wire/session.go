package wire

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/mmdmcy/belleke/internal/identity"
)

// MaxControl is the largest sealed control message we read (chat is text).
const MaxControl = 64 * 1024

// Hello opens a call: the caller's name, its fresh key for this call, a nonce,
// and a signature by its device key over all of it plus the callee's id, so
// the callee knows who is calling and that the hello was meant for it.
type Hello struct {
	Belleke   int    `json:"belleke"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Ephemeral []byte `json:"eph"`
	Nonce     []byte `json:"nonce"`
	Signature []byte `json:"sig"`
}

// Welcome answers a hello: the callee's fresh key, signed together with the
// caller's key and nonce.
type Welcome struct {
	Ephemeral []byte `json:"eph"`
	Signature []byte `json:"sig"`
	Refused   string `json:"refused,omitempty"`
}

// Message is one control message inside a call.
type Message struct {
	Type string `json:"t"`
	Text string `json:"text,omitempty"`
	Port int    `json:"port,omitempty"`
}

// Message types.
const (
	MsgRing   = "ring"   // caller: I am calling you
	MsgAccept = "accept" // callee: picked up; Port is my UDP port for sound
	MsgReject = "reject" // callee: not now
	MsgHangup = "hangup"
	MsgChat   = "chat"
	MsgPing   = "ping"
	MsgAudio  = "audio" // caller: Port is my UDP port for sound
)

// Session is one established call channel over TCP.
type Session struct {
	Conn      net.Conn
	PeerID    string
	PeerName  string
	Initiator bool
	sealer    *Sealer
	reader    *bufio.Reader
}

func helloBytes(h *Hello, calleeID string) []byte {
	return []byte("belleke hello v1\n" + h.ID + "\n" + calleeID + "\n" + string(h.Ephemeral) + string(h.Nonce))
}

func welcomeBytes(w *Welcome, h *Hello, calleeID string) []byte {
	return []byte("belleke welcome v1\n" + calleeID + "\n" + h.ID + "\n" + string(w.Ephemeral) + string(h.Ephemeral) + string(h.Nonce))
}

func writeJSON(conn net.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFrame(conn, raw)
}

func writeFrame(w io.Writer, raw []byte) error {
	if len(raw) > MaxControl {
		return errors.New("message too large")
	}
	buf := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(buf, uint32(len(raw)))
	copy(buf[4:], raw)
	_, err := w.Write(buf)
	return err
}

func readFrame(r *bufio.Reader) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n == 0 || n > MaxControl {
		return nil, errors.New("bad frame size")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// Dial opens a call channel to a device at addr whose id we trust.
func Dial(addr string, me *identity.Identity, myName string, peerID string, timeout time.Duration) (*Session, error) {
	peerPub, err := identity.Decode(peerID)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	eph, err := NewEphemeral()
	if err != nil {
		conn.Close()
		return nil, err
	}
	nonce := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		conn.Close()
		return nil, err
	}
	hello := &Hello{Belleke: 1, ID: me.ID(), Name: myName, Ephemeral: eph.Public[:], Nonce: nonce}
	hello.Signature = me.Sign(helloBytes(hello, peerID))
	if err := writeJSON(conn, hello); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	raw, err := readFrame(reader)
	if err != nil {
		conn.Close()
		return nil, err
	}
	var welcome Welcome
	if err := json.Unmarshal(raw, &welcome); err != nil {
		conn.Close()
		return nil, err
	}
	if welcome.Refused != "" {
		conn.Close()
		return nil, fmt.Errorf("refused: %s", welcome.Refused)
	}
	if len(welcome.Ephemeral) != 32 || !identity.Verify(peerPub, welcomeBytes(&welcome, hello, peerID), welcome.Signature) {
		conn.Close()
		return nil, errors.New("the other side did not prove who it is")
	}
	var theirs [32]byte
	copy(theirs[:], welcome.Ephemeral)
	key, err := eph.Agree(theirs, nonce, me.ID(), peerID)
	if err != nil {
		conn.Close()
		return nil, err
	}
	sealer, err := NewSealer(key, true)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &Session{Conn: conn, PeerID: peerID, Initiator: true, sealer: sealer, reader: reader}, nil
}

// Trusted answers whether a device id is one we talk to; the caller's name is
// given so the UI can show who is knocking.
type Trusted func(id string, name string) bool

// Accept answers a hello on an incoming connection. Untrusted callers get a
// refusal and nothing else: no key, no name.
func Accept(conn net.Conn, me *identity.Identity, trusted Trusted, timeout time.Duration) (*Session, error) {
	_ = conn.SetDeadline(time.Now().Add(timeout))
	reader := bufio.NewReader(conn)
	raw, err := readFrame(reader)
	if err != nil {
		conn.Close()
		return nil, err
	}
	var hello Hello
	if err := json.Unmarshal(raw, &hello); err != nil || hello.Belleke != 1 || len(hello.Ephemeral) != 32 || len(hello.Nonce) < 8 {
		conn.Close()
		return nil, errors.New("not a belleke hello")
	}
	if len(hello.Name) > 40 {
		hello.Name = hello.Name[:40]
	}
	peerPub, err := identity.Decode(hello.ID)
	if err != nil || !identity.Verify(peerPub, helloBytes(&hello, me.ID()), hello.Signature) {
		conn.Close()
		return nil, errors.New("the caller did not prove who it is")
	}
	if !trusted(hello.ID, hello.Name) {
		_ = writeJSON(conn, &Welcome{Refused: "pair first"})
		conn.Close()
		return nil, fmt.Errorf("untrusted caller %s", hello.Name)
	}
	eph, err := NewEphemeral()
	if err != nil {
		conn.Close()
		return nil, err
	}
	welcome := &Welcome{Ephemeral: eph.Public[:]}
	welcome.Signature = me.Sign(welcomeBytes(welcome, &hello, me.ID()))
	if err := writeJSON(conn, welcome); err != nil {
		conn.Close()
		return nil, err
	}
	var theirs [32]byte
	copy(theirs[:], hello.Ephemeral)
	key, err := eph.Agree(theirs, hello.Nonce, hello.ID, me.ID())
	if err != nil {
		conn.Close()
		return nil, err
	}
	sealer, err := NewSealer(key, false)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &Session{Conn: conn, PeerID: hello.ID, PeerName: hello.Name, Initiator: false, sealer: sealer, reader: reader}, nil
}

// Send seals and writes one control message.
func (s *Session) Send(m Message) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFrame(s.Conn, s.sealer.SealControl(raw))
}

// Receive reads and opens the next control message.
func (s *Session) Receive() (Message, error) {
	raw, err := readFrame(s.reader)
	if err != nil {
		return Message{}, err
	}
	plain, err := s.sealer.OpenControl(raw)
	if err != nil {
		return Message{}, err
	}
	var m Message
	if err := json.Unmarshal(plain, &m); err != nil {
		return Message{}, err
	}
	if len(m.Text) > 4000 {
		m.Text = m.Text[:4000]
	}
	return m, nil
}

// SealAudio and OpenAudio seal sound for the UDP side of this call.
func (s *Session) SealAudio(frame []byte) []byte { return s.sealer.SealAudio(frame) }

// OpenAudio opens a sound packet of this call.
func (s *Session) OpenAudio(packet []byte) (uint64, []byte, error) { return s.sealer.OpenAudio(packet) }

// Close ends the channel.
func (s *Session) Close() error { return s.Conn.Close() }

// PeerPublic is the peer's device key.
func (s *Session) PeerPublic() ed25519.PublicKey {
	pub, _ := identity.Decode(s.PeerID)
	return pub
}
