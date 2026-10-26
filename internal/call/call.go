// Package call is the heart of Belleke: it listens for calls, places them,
// moves sound both ways while one is up, carries chat, and tells the window
// what changed. It knows nothing about widgets; the window knows nothing
// about sockets.
package call

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/mmdmcy/belleke/internal/audio"
	"github.com/mmdmcy/belleke/internal/identity"
	"github.com/mmdmcy/belleke/internal/store"
	"github.com/mmdmcy/belleke/internal/wire"
)

// State of the one call this device can be in.
type State string

// States.
const (
	Idle     State = "idle"
	Calling  State = "calling"  // we rang them, waiting
	Ringing  State = "ringing"  // they ring us, waiting for the person
	InCall   State = "in call"
	Finished State = "finished" // briefly, before idle
)

// Event tells the window something changed.
type Event struct {
	Kind     string // "state", "chat", "devices", "error", "knock"
	State    State
	PeerID   string
	PeerName string
	Text     string
	Err      error
}

// Engine runs one device.
type Engine struct {
	Me       *identity.Identity
	Store    *store.Store
	Events   chan Event
	NoAudio  bool // tests and the self-test: no microphone or speaker
	tcp      net.Listener
	udp      *net.UDPConn
	disc     *wire.Discovery
	mu       sync.Mutex
	state    State
	session  *wire.Session
	peerAddr *net.UDPAddr
	device   *audio.Device
	jitter   *audio.Jitter
	started  time.Time
	outgoing bool
	stop     chan struct{}
	muted    bool
	// Knock is asked when an untrusted device calls; it may trust it (true).
	Knock func(id, name string) bool
	// Stats for the window.
	Sent, Received uint64
}

// New wires an engine; Start opens the ports.
func New(me *identity.Identity, st *store.Store) *Engine {
	return &Engine{Me: me, Store: st, Events: make(chan Event, 64), state: Idle, jitter: audio.NewJitter(3, 12)}
}

// Start listens on TCP and UDP (any free port) and begins announcing.
func (e *Engine) Start() error {
	tcp, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		return err
	}
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		tcp.Close()
		return err
	}
	e.tcp, e.udp = tcp, udp
	e.stop = make(chan struct{})
	disc, err := wire.NewDiscovery(e.beacon)
	if err != nil {
		log.Printf("discovery: %v (calls by address still work)", err)
	} else {
		e.disc = disc
		disc.Found = func(s wire.Seen) {
			if p, ok := e.Store.Peer(s.ID); ok && p.LastAddr != s.Addr.String() {
				p.LastAddr = s.Addr.String()
				_ = e.Store.Trust(p)
			}
			e.emit(Event{Kind: "devices"})
		}
	}
	go e.acceptLoop()
	go e.udpLoop()
	return nil
}

func (e *Engine) beacon() wire.Beacon {
	return wire.Beacon{Belleke: 1, Name: e.Store.Settings().Name, ID: e.Me.ID(), TCP: e.TCPPort(), UDP: e.UDPPort()}
}

// TCPPort and UDPPort are what we listen on.
func (e *Engine) TCPPort() int { return e.tcp.Addr().(*net.TCPAddr).Port }

// UDPPort is the sound port.
func (e *Engine) UDPPort() int { return e.udp.LocalAddr().(*net.UDPAddr).Port }

// Devices are the ones heard on the network right now.
func (e *Engine) Devices() []wire.Seen {
	if e.disc == nil {
		return nil
	}
	return e.disc.Devices()
}

// State now.
func (e *Engine) State() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// Peer of the current call.
func (e *Engine) Peer() (id, name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session == nil {
		return "", ""
	}
	return e.session.PeerID, e.session.PeerName
}

// Since is when the call went up.
func (e *Engine) Since() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.started
}

func (e *Engine) emit(ev Event) {
	select {
	case e.Events <- ev:
	default:
	}
}

func (e *Engine) setState(s State) {
	e.mu.Lock()
	e.state = s
	id, name := "", ""
	if e.session != nil {
		id, name = e.session.PeerID, e.session.PeerName
	}
	e.mu.Unlock()
	e.emit(Event{Kind: "state", State: s, PeerID: id, PeerName: name})
}

func (e *Engine) trusted(id, name string) bool {
	if _, ok := e.Store.Peer(id); ok {
		return true
	}
	if e.Knock != nil {
		e.emit(Event{Kind: "knock", PeerID: id, PeerName: name})
		return e.Knock(id, name)
	}
	return false
}

func (e *Engine) acceptLoop() {
	for {
		conn, err := e.tcp.Accept()
		if err != nil {
			return
		}
		go e.incoming(conn)
	}
}

func (e *Engine) incoming(conn net.Conn) {
	e.mu.Lock()
	busy := e.state != Idle
	e.mu.Unlock()
	if busy {
		conn.Close()
		return
	}
	session, err := wire.Accept(conn, e.Me, e.trusted, 20*time.Second)
	if err != nil {
		e.emit(Event{Kind: "error", Err: err})
		return
	}
	if p, ok := e.Store.Peer(session.PeerID); ok && p.Name != session.PeerName && session.PeerName != "" {
		p.Name = session.PeerName
		_ = e.Store.Trust(p)
	}
	e.mu.Lock()
	if e.state != Idle {
		e.mu.Unlock()
		_ = session.Send(wire.Message{Type: wire.MsgReject})
		session.Close()
		return
	}
	e.session = session
	e.outgoing = false
	host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	e.peerAddr = &net.UDPAddr{IP: net.ParseIP(host)}
	e.mu.Unlock()
	e.setState(Ringing)
	go e.controlLoop(session)
}

// Call rings a device by id (address from discovery or the last known one).
func (e *Engine) Call(peerID string) error {
	peer, ok := e.Store.Peer(peerID)
	if !ok {
		return errors.New("trust this device first")
	}
	addr, port := "", 0
	for _, s := range e.Devices() {
		if s.ID == peerID {
			addr, port = s.Addr.String(), s.TCP
		}
	}
	if addr == "" {
		return fmt.Errorf("%s is not on this network right now", peer.Name)
	}
	return e.CallAddr(peerID, net.JoinHostPort(addr, strconv.Itoa(port)))
}

// CallAddr rings a trusted device at a known TCP address.
func (e *Engine) CallAddr(peerID, tcpAddr string) error {
	e.mu.Lock()
	if e.state != Idle {
		e.mu.Unlock()
		return errors.New("already in a call")
	}
	e.state = Calling
	e.mu.Unlock()
	session, err := wire.Dial(tcpAddr, e.Me, e.Store.Settings().Name, peerID, 20*time.Second)
	if err != nil {
		e.mu.Lock()
		e.state = Idle
		e.mu.Unlock()
		e.emit(Event{Kind: "state", State: Idle})
		e.logCall(peerID, "failed", time.Now(), 0, true)
		return err
	}
	if p, ok := e.Store.Peer(peerID); ok {
		session.PeerName = p.Name
	}
	e.mu.Lock()
	e.session = session
	e.outgoing = true
	host, _, _ := net.SplitHostPort(tcpAddr)
	e.peerAddr = &net.UDPAddr{IP: net.ParseIP(host)}
	e.mu.Unlock()
	e.setState(Calling)
	if err := session.Send(wire.Message{Type: wire.MsgRing}); err != nil {
		e.end("failed")
		return err
	}
	go e.controlLoop(session)
	return nil
}

// Answer picks up a ringing call.
func (e *Engine) Answer() error {
	e.mu.Lock()
	session, state := e.session, e.state
	e.mu.Unlock()
	if session == nil || state != Ringing {
		return errors.New("nothing is ringing")
	}
	if err := session.Send(wire.Message{Type: wire.MsgAccept, Port: e.UDPPort()}); err != nil {
		return err
	}
	return e.goLive()
}

// Decline refuses a ringing call.
func (e *Engine) Decline() {
	e.mu.Lock()
	session, state := e.session, e.state
	e.mu.Unlock()
	if session != nil && state == Ringing {
		_ = session.Send(wire.Message{Type: wire.MsgReject})
		e.end("rejected")
	}
}

// Hangup ends whatever call there is.
func (e *Engine) Hangup() {
	e.mu.Lock()
	session := e.session
	e.mu.Unlock()
	if session != nil {
		_ = session.Send(wire.Message{Type: wire.MsgHangup})
	}
	e.end("hangup")
}

// SetMuted silences the microphone.
func (e *Engine) SetMuted(m bool) {
	e.mu.Lock()
	e.muted = m
	d := e.device
	e.mu.Unlock()
	if d != nil {
		d.SetMuted(m)
	}
}

// Muted says whether the microphone is silenced.
func (e *Engine) Muted() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.muted
}

// Chat sends a line to the peer of the current call (or ringing one).
func (e *Engine) Chat(text string) error {
	e.mu.Lock()
	session := e.session
	e.mu.Unlock()
	if session == nil {
		return errors.New("no call to chat in")
	}
	if err := session.Send(wire.Message{Type: wire.MsgChat, Text: text}); err != nil {
		return err
	}
	_ = e.Store.AddChat(session.PeerID, store.Chat{When: time.Now(), Mine: true, Text: text})
	e.emit(Event{Kind: "chat", PeerID: session.PeerID, PeerName: session.PeerName, Text: text})
	return nil
}

// Levels are the microphone and speaker loudness for the meters.
func (e *Engine) Levels() (in, out float64) {
	e.mu.Lock()
	d := e.device
	e.mu.Unlock()
	if d == nil {
		return 0, 0
	}
	return d.InLevel, d.OutLevel
}

func (e *Engine) goLive() error {
	e.mu.Lock()
	e.started = time.Now()
	e.jitter.Reset()
	e.mu.Unlock()
	if !e.NoAudio {
		device, err := audio.Open(e.jitter.Get)
		if err != nil {
			e.emit(Event{Kind: "error", Err: err})
			e.Hangup()
			return err
		}
		device.SetMuted(e.Muted())
		e.mu.Lock()
		e.device = device
		e.mu.Unlock()
		go e.sendLoop(device.Captured)
	}
	e.setState(InCall)
	return nil
}

func (e *Engine) controlLoop(session *wire.Session) {
	for {
		m, err := session.Receive()
		if err != nil {
			e.mu.Lock()
			current := e.session == session
			e.mu.Unlock()
			if current {
				e.end("hangup")
			}
			return
		}
		switch m.Type {
		case wire.MsgAccept:
			e.mu.Lock()
			if e.peerAddr != nil {
				e.peerAddr.Port = m.Port
			}
			e.mu.Unlock()
			_ = session.Send(wire.Message{Type: wire.MsgAudio, Port: e.UDPPort()})
			_ = e.goLive()
		case wire.MsgAudio:
			e.mu.Lock()
			if e.peerAddr != nil {
				e.peerAddr.Port = m.Port
			}
			e.mu.Unlock()
		case wire.MsgReject:
			e.end("rejected")
			return
		case wire.MsgHangup:
			e.end("hangup")
			return
		case wire.MsgChat:
			_ = e.Store.AddChat(session.PeerID, store.Chat{When: time.Now(), Mine: false, Text: m.Text})
			e.emit(Event{Kind: "chat", PeerID: session.PeerID, PeerName: session.PeerName, Text: m.Text})
		case wire.MsgRing, wire.MsgPing:
		}
	}
}

// SendFrame seals and sends one frame of sound (the self-test uses it).
func (e *Engine) SendFrame(frame []byte) {
	e.mu.Lock()
	session, addr := e.session, e.peerAddr
	e.mu.Unlock()
	if session == nil || addr == nil || addr.Port == 0 {
		return
	}
	if _, err := e.udp.WriteToUDP(session.SealAudio(frame), addr); err == nil {
		e.Sent++
	}
}

func (e *Engine) sendLoop(frames <-chan []byte) {
	for frame := range frames {
		e.mu.Lock()
		live := e.state == InCall
		e.mu.Unlock()
		if !live {
			return
		}
		e.SendFrame(frame)
	}
}

func (e *Engine) udpLoop() {
	buf := make([]byte, 4096)
	for {
		n, from, err := e.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		e.mu.Lock()
		session, addr := e.session, e.peerAddr
		e.mu.Unlock()
		if session == nil || addr == nil || !from.IP.Equal(addr.IP) {
			continue
		}
		seq, frame, err := session.OpenAudio(buf[:n])
		if err != nil || len(frame) != audio.FrameBytes {
			continue
		}
		e.Received++
		e.jitter.Put(seq, frame)
	}
}

// Jitter exposes the receive buffer (self-test reads it).
func (e *Engine) Jitter() *audio.Jitter { return e.jitter }

func (e *Engine) logCall(peerID, result string, start time.Time, seconds int, outgoing bool) {
	name := peerID
	if p, ok := e.Store.Peer(peerID); ok {
		name = p.Name
	}
	_ = e.Store.LogCall(store.Call{PeerID: peerID, PeerName: name, Start: start, Seconds: seconds, Outgoing: outgoing, Result: result})
}

func (e *Engine) end(reason string) {
	e.mu.Lock()
	session, device, state, started, outgoing := e.session, e.device, e.state, e.started, e.outgoing
	e.session, e.device, e.peerAddr = nil, nil, nil
	e.state = Idle
	e.mu.Unlock()
	if device != nil {
		device.Close()
	}
	if session != nil {
		session.Close()
		result, seconds := reason, 0
		switch state {
		case InCall:
			result, seconds = "answered", int(time.Since(started).Seconds())
		case Ringing:
			if reason == "hangup" {
				result = "missed"
			}
		case Calling:
			if reason == "hangup" && !outgoing {
				result = "missed"
			}
		}
		e.logCall(session.PeerID, result, started, seconds, outgoing)
		e.emit(Event{Kind: "state", State: Finished, PeerID: session.PeerID, PeerName: session.PeerName, Text: result})
	}
	e.emit(Event{Kind: "state", State: Idle})
}

// Close stops everything.
func (e *Engine) Close() {
	e.Hangup()
	if e.disc != nil {
		e.disc.Close()
	}
	if e.tcp != nil {
		e.tcp.Close()
	}
	if e.udp != nil {
		e.udp.Close()
	}
}
