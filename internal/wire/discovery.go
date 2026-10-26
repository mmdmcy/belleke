package wire

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

// DiscoveryPort is where every Belleke on the network listens for beacons.
const DiscoveryPort = 47547

// Beacon is what a device says about itself every two seconds, in a UDP
// broadcast. Nothing in it is secret: the public key is the device's name.
type Beacon struct {
	Belleke int    `json:"belleke"`
	Name    string `json:"name"`
	ID      string `json:"id"`
	TCP     int    `json:"tcp"`
	UDP     int    `json:"udp"`
}

// Seen is a device heard on the network.
type Seen struct {
	Beacon
	Addr     net.IP
	LastSeen time.Time
}

// Discovery broadcasts our beacon and collects the others'.
type Discovery struct {
	conn  *net.UDPConn
	mine  func() Beacon
	mu    sync.Mutex
	seen  map[string]Seen
	stop  chan struct{}
	Found func(Seen)
}

// NewDiscovery binds the discovery port (reusable, so two instances on one
// machine can both listen during tests) and starts listening.
func NewDiscovery(mine func() Beacon) (*Discovery, error) {
	conn, err := listenBroadcast(DiscoveryPort)
	if err != nil {
		return nil, err
	}
	d := &Discovery{conn: conn, mine: mine, seen: map[string]Seen{}, stop: make(chan struct{})}
	go d.listen()
	go d.announce()
	return d, nil
}

func (d *Discovery) listen() {
	buf := make([]byte, 2048)
	for {
		n, from, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-d.stop:
				return
			default:
				continue
			}
		}
		var b Beacon
		if json.Unmarshal(buf[:n], &b) != nil || b.Belleke != 1 || b.ID == "" || b.ID == d.mine().ID {
			continue
		}
		if len(b.Name) > 40 {
			b.Name = b.Name[:40]
		}
		s := Seen{Beacon: b, Addr: from.IP, LastSeen: time.Now()}
		d.mu.Lock()
		d.seen[b.ID] = s
		d.mu.Unlock()
		if d.Found != nil {
			d.Found(s)
		}
	}
}

func (d *Discovery) announce() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		d.Announce()
		select {
		case <-d.stop:
			return
		case <-ticker.C:
		}
	}
}

// Announce sends our beacon once to every broadcast address we have.
func (d *Discovery) Announce() {
	raw, err := json.Marshal(d.mine())
	if err != nil {
		return
	}
	for _, addr := range broadcastAddrs() {
		_, _ = d.conn.WriteToUDP(raw, &net.UDPAddr{IP: addr, Port: DiscoveryPort})
	}
}

// Devices lists what was heard in the last 10 seconds.
func (d *Discovery) Devices() []Seen {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Seen
	for id, s := range d.seen {
		if time.Since(s.LastSeen) > 10*time.Second {
			delete(d.seen, id)
			continue
		}
		out = append(out, s)
	}
	return out
}

// Close stops listening and announcing.
func (d *Discovery) Close() {
	close(d.stop)
	_ = d.conn.Close()
}

// broadcastAddrs is the limited broadcast plus each interface's own, so
// routers that drop 255.255.255.255 still carry the beacon.
func broadcastAddrs() []net.IP {
	out := []net.IP{net.IPv4bcast}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			ip := ipnet.IP.To4()
			mask := ipnet.Mask
			if len(mask) == 16 {
				mask = mask[12:]
			}
			b := make(net.IP, 4)
			for i := range b {
				b[i] = ip[i] | ^mask[i]
			}
			out = append(out, b)
		}
	}
	return out
}

// ErrNoNetwork says the discovery port could not be bound.
var ErrNoNetwork = errors.New("cannot listen for other devices")
